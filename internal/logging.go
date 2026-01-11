package internal

import (
	"context"
	"log/slog"
)

// loggerKey is an unexported key type for storing the logger in context
type loggerKey struct{}

// ContextWithLogger adds a logger to the context
func ContextWithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, logger)
}

// LoggerFromContext extracts the logger from the context
func LoggerFromContext(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return logger
	}
	// Return a default logger if none is found in context
	return slog.Default()
}
