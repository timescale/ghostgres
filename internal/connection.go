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
	llmClient *LLMClient
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

	// Validate username
	if username != "openai" {
		c.backend.Send(&pgproto3.ErrorResponse{
			Severity: "FATAL",
			Code:     "28000", // invalid_authorization_specification
			Message:  "invalid username (must be 'openai')",
		})
		c.backend.Flush()
		return fmt.Errorf("invalid username: %s", username)
	}

	// Determine model from database name
	model := database
	if model == "" {
		model = "gpt-4o-2024-08-06" // default model
	}

	// Parse options for reasoning_effort
	// Format: "reasoning_effort=medium" or "param1=value1 reasoning_effort=medium"
	reasoningEffort := parseOptionValue(options, "reasoning_effort")

	// Add connection-specific fields to logger
	logger = LoggerFromContext(ctx).With("username", username, "database", database, "model", model)
	if reasoningEffort != "" {
		logger = logger.With("reasoning_effort", reasoningEffort)
	}
	ctx = ContextWithLogger(ctx, logger)
	logger.Info("connection authenticated")

	// Send startup messages
	if err := sendStartupMessages(ctx, c.backend); err != nil {
		return fmt.Errorf("failed to send startup messages: %w", err)
	}

	// Create per-connection LLM client with API key from password
	c.llmClient = NewLLMClient(password, model, reasoningEffort)

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

// parseOptionValue extracts a key=value pair from a space-separated options string
// For example: "reasoning_effort=medium other_param=value" -> parseOptionValue(..., "reasoning_effort") returns "medium"
func parseOptionValue(options, key string) string {
	if options == "" {
		return ""
	}

	// Split by spaces to get individual key=value pairs
	parts := strings.Fields(options)
	prefix := key + "="

	for _, part := range parts {
		if value, found := strings.CutPrefix(part, prefix); found {
			return value
		}
	}

	return ""
}
