package internal

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgproto3"
)

// Connection represents a single client connection
type Connection struct {
	conn      net.Conn
	backend   *pgproto3.Backend
	llmClient LLMClient
}

// NewConnection creates a new Connection instance
func NewConnection(conn net.Conn) *Connection {
	backend := pgproto3.NewBackend(conn, conn)
	return &Connection{
		conn:    conn,
		backend: backend,
	}
}

// Handle processes the connection lifecycle
func (c *Connection) Handle(ctx context.Context) error {
	logger := LoggerFromContext(ctx)

	// Ensure connection is closed on exit
	defer func() {
		c.conn.Close()
		logger.Info("connection closed")
	}()

	// Defer sending termination message if context cancelled during operation
	defer func() {
		select {
		case <-ctx.Done():
			c.backend.Send(&pgproto3.ErrorResponse{
				Severity: "FATAL",
				Code:     "57P01", // admin_shutdown
				Message:  "server shutting down",
			})
			c.backend.Flush()
		default:
		}
	}()

	// Perform authentication
	username, password, database, options, err := authenticate(ctx, c.conn, c.backend)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	// Validate provider (username)
	provider := username
	if provider != "openai" && provider != "anthropic" {
		c.backend.Send(&pgproto3.ErrorResponse{
			Severity: "FATAL",
			Code:     "28000", // invalid_authorization_specification
			Message:  fmt.Sprintf("unsupported provider %q (must be 'openai' or 'anthropic')", provider),
		})
		c.backend.Flush()
		return fmt.Errorf("unsupported provider: %s", provider)
	}

	// Model is required (specified as database name)
	model := database
	if model == "" {
		c.backend.Send(&pgproto3.ErrorResponse{
			Severity: "FATAL",
			Code:     "3D000", // invalid_catalog_name
			Message:  "model is required (specify as database name)",
		})
		c.backend.Flush()
		return fmt.Errorf("no model specified")
	}

	// Parse all options into a map
	opts := parseOptions(options)

	// Add connection-specific fields to logger
	logger = LoggerFromContext(ctx).With("provider", provider, "model", model)
	if len(opts) > 0 {
		logger = logger.With("options", opts)
	}
	ctx = ContextWithLogger(ctx, logger)
	logger.Info("connection authenticated")

	// Send startup messages
	if err := sendStartupMessages(ctx, c.backend); err != nil {
		return fmt.Errorf("failed to send startup messages: %w", err)
	}

	// Create per-connection LLM client based on provider
	var llmClient LLMClient
	switch provider {
	case "openai":
		llmClient = NewOpenAILLMClient(password, model, opts)
	case "anthropic":
		llmClient = NewAnthropicLLMClient(password, model, opts)
	}
	c.llmClient = llmClient

	// Enter query loop
	for {
		// Check context cancellation before receiving
		select {
		case <-ctx.Done():
			c.backend.Send(&pgproto3.ErrorResponse{
				Severity: "FATAL",
				Code:     "57P01", // admin_shutdown
				Message:  "server shutting down",
			})
			c.backend.Flush()
			return nil
		default:
		}

		// Receive message from client
		msg, err := c.backend.Receive()
		if err != nil {
			return fmt.Errorf("failed to receive message: %w", err)
		}

		// Handle message based on type
		switch msg := msg.(type) {
		case *pgproto3.Query:
			queryString := msg.String
			queryLogger := LoggerFromContext(ctx).With("query", queryString)
			queryCtx := ContextWithLogger(ctx, queryLogger)
			if err := handleQuery(queryCtx, c.backend, c.llmClient, queryString); err != nil {
				logger.Error("query handling failed", "error", err)
				// Error response is already sent by handleQuery
			}
		case *pgproto3.Terminate:
			logger.Info("client requested termination")
			return nil
		default:
			logger.Warn("unsupported message type", "type", fmt.Sprintf("%T", msg))
		}
	}
}

// parseOptions parses a space-separated options string into a map of key=value pairs.
// For example: "reasoning_effort=medium effort=low" -> map["reasoning_effort"]="medium", map["effort"]="low"
func parseOptions(options string) map[string]string {
	result := make(map[string]string)
	if options == "" {
		return result
	}

	for _, part := range strings.Fields(options) {
		if key, value, ok := strings.Cut(part, "="); ok {
			result[key] = value
		}
	}

	return result
}
