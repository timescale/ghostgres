package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/timescale/ghostgres/internal"
)

func main() {
	// Parse command-line flags
	host := flag.String("host", "", "hostname/interface to bind to (default: all interfaces)")
	port := flag.Int("port", 5432, "port to listen on")
	logLevel := flagLevel("log-level", zapcore.InfoLevel, "log level (debug, info, warn, error)")
	logFormat := flagFormat("log-format", internal.LogFormatConsole, "log format (console, json)")
	promptFile := flag.String("prompt", "", "path to custom system prompt file")
	flag.Parse()

	// Initialize logger
	logger, err := internal.NewLogger(*logLevel, *logFormat)
	if err != nil {
		log.Fatalf("Failed to initialize logger: %s", err)
	}

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
			logger.Fatal("Failed to read system prompt file", zap.Error(err))
		}
		systemPrompt = string(data)
	}

	// Create server
	server := internal.NewServer(*host, *port, systemPrompt)

	// Start server in goroutine
	go func() {
		if err := server.Start(ctx); err != nil {
			logger.Fatal("Server error", zap.Error(err))
		}
	}()

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigChan

	logger.Info("Received shutdown signal", zap.String("signal", sig.String()))

	// Cancel context to stop accepting new connections
	cancel()

	// Close server and wait for all connections to finish
	server.Close()

	logger.Info("Shutdown complete")
}

func flagLevel(name string, value zapcore.Level, usage string) *zapcore.Level {
	p := new(zapcore.Level)
	flag.TextVar(p, name, value, usage)
	return p
}

func flagFormat(name string, value internal.LogFormat, usage string) *internal.LogFormat {
	p := new(internal.LogFormat)
	flag.TextVar(p, name, value, usage)
	return p
}
