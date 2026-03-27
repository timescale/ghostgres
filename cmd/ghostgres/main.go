package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/timescale/ghostgres/internal"
)

func main() {
	// Parse command-line flags
	host := flag.String("host", "", "hostname/interface to bind to (default: all interfaces)")
	port := flag.Int("port", 5432, "port to listen on")
	logLevel := flag.String("log-level", "info", "log level (debug, info, warn, error)")
	promptFile := flag.String("prompt", "", "path to a file containing a custom system prompt")
	flag.Parse()

	// Parse log level
	var level slog.Level
	switch *logLevel {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		fmt.Fprintf(os.Stderr, "invalid log level: %s\n", *logLevel)
		os.Exit(1)
	}

	// Initialize logger
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))

	// Set up context with cancellation for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Add logger to context
	ctx = internal.ContextWithLogger(ctx, logger)

	// Determine system prompt: custom file or built-in default
	systemPrompt := internal.DefaultSystemPrompt
	if *promptFile != "" {
		data, err := os.ReadFile(*promptFile)
		if err != nil {
			logger.Error("failed to read system prompt file", "error", err)
			os.Exit(1)
		}
		systemPrompt = string(data)
	}

	// Create server
	server := internal.NewServer(*host, *port, systemPrompt)

	// Start server in goroutine
	go func() {
		if err := server.Start(ctx); err != nil {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigChan

	logger.Info("received shutdown signal", "signal", sig.String())

	// Cancel context to stop accepting new connections
	cancel()

	// Close server and wait for all connections to finish
	server.Close()

	logger.Info("shutdown complete")
}
