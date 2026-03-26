package internal

import (
	"context"
	"fmt"
	"math/rand"
	"net"

	"github.com/jackc/pgx/v5/pgproto3"
)

// authenticate performs the authentication flow and returns username, password, database, and options
func authenticate(ctx context.Context, conn net.Conn, backend *pgproto3.Backend) (username, password, database, options string, err error) {
	logger := LoggerFromContext(ctx)

	// Receive startup message
	msg, err := backend.ReceiveStartupMessage()
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to receive startup message: %w", err)
	}

	// Handle SSLRequest by denying SSL and reading the real startup message
	if _, ok := msg.(*pgproto3.SSLRequest); ok {
		// Send 'N' to indicate SSL is not supported
		if _, err := conn.Write([]byte("N")); err != nil {
			return "", "", "", "", fmt.Errorf("failed to send SSL denial: %w", err)
		}
		logger.Debug("denied SSL request")

		// Now receive the actual startup message
		msg, err = backend.ReceiveStartupMessage()
		if err != nil {
			return "", "", "", "", fmt.Errorf("failed to receive startup message after SSL denial: %w", err)
		}
	}

	// Type assert to StartupMessage
	startupMsg, ok := msg.(*pgproto3.StartupMessage)
	if !ok {
		return "", "", "", "", fmt.Errorf("expected StartupMessage, got %T", msg)
	}

	// Extract username, database, and options from parameters
	username = startupMsg.Parameters["user"]
	database = startupMsg.Parameters["database"]
	options = startupMsg.Parameters["options"]

	logger.Info("authentication attempt", "username", username, "database", database, "options", options)

	// Send cleartext password request
	backend.Send(&pgproto3.AuthenticationCleartextPassword{})
	if err := backend.Flush(); err != nil {
		return "", "", "", "", fmt.Errorf("failed to send auth request: %w", err)
	}

	// Receive password message
	msg, err = backend.Receive()
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to receive password: %w", err)
	}

	// Type assert to PasswordMessage
	passwordMsg, ok := msg.(*pgproto3.PasswordMessage)
	if !ok {
		return "", "", "", "", fmt.Errorf("expected PasswordMessage, got %T", msg)
	}

	password = passwordMsg.Password

	return username, password, database, options, nil
}

// sendStartupMessages sends the startup sequence to the client
func sendStartupMessages(ctx context.Context, backend *pgproto3.Backend) error {
	logger := LoggerFromContext(ctx)

	// Send authentication OK
	backend.Send(&pgproto3.AuthenticationOk{})

	// Send backend key data (random values for MVP)
	backend.Send(&pgproto3.BackendKeyData{
		ProcessID: uint32(rand.Int31()),
		SecretKey: uint32(rand.Int31()),
	})

	// Send parameter statuses
	backend.Send(&pgproto3.ParameterStatus{
		Name:  "server_version",
		Value: "16.0 (agentic-postgres)",
	})
	backend.Send(&pgproto3.ParameterStatus{
		Name:  "server_encoding",
		Value: "UTF8",
	})
	backend.Send(&pgproto3.ParameterStatus{
		Name:  "client_encoding",
		Value: "UTF8",
	})
	backend.Send(&pgproto3.ParameterStatus{
		Name:  "DateStyle",
		Value: "ISO, MDY",
	})
	backend.Send(&pgproto3.ParameterStatus{
		Name:  "TimeZone",
		Value: "UTC",
	})

	// Send ReadyForQuery with idle transaction status
	backend.Send(&pgproto3.ReadyForQuery{
		TxStatus: 'I', // idle
	})

	// Flush all messages
	if err := backend.Flush(); err != nil {
		return fmt.Errorf("failed to flush startup messages: %w", err)
	}

	logger.Info("startup sequence complete")
	return nil
}
