package main

import (
	"context"
	"crypto/tls"
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
	tlsCert := flag.String("tls-cert", "", "path to TLS certificate PEM file (enables TLS when set with -tls-key)")
	tlsKey := flag.String("tls-key", "", "path to TLS private key PEM file (enables TLS when set with -tls-cert)")
	acmeDomain := flag.String("acme-domain", "", "domain to obtain a Let's Encrypt certificate for (enables TLS; requires -acme-cache-dir)")
	acmeCacheDir := flag.String("acme-cache-dir", "", "directory to persist ACME certificates and account key in")
	acmeEmail := flag.String("acme-email", "", "contact email for the ACME account (optional)")
	acmeHTTPAddr := flag.String("acme-http-addr", ":80", "address to serve ACME HTTP-01 challenges on")
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

	// Load TLS config from cert and key files, or obtain certificates via ACME
	var tlsConfig *tls.Config
	if *acmeDomain != "" {
		if *tlsCert != "" || *tlsKey != "" {
			logger.Fatal("-acme-domain cannot be combined with -tls-cert or -tls-key")
		}
		if *acmeCacheDir == "" {
			logger.Fatal("-acme-cache-dir is required with -acme-domain")
		}
		acme := internal.NewACME(*acmeDomain, *acmeCacheDir, *acmeEmail, *acmeHTTPAddr)
		tlsConfig = acme.TLSConfig()
		go func() {
			if err := acme.Serve(ctx); err != nil {
				logger.Fatal("ACME error", zap.Error(err))
			}
		}()
		logger.Info("TLS enabled via ACME", zap.String("domain", *acmeDomain))
	} else if *tlsCert != "" && *tlsKey != "" {
		cert, err := tls.LoadX509KeyPair(*tlsCert, *tlsKey)
		if err != nil {
			logger.Fatal("Failed to load TLS certificate", zap.Error(err))
		}
		tlsConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
		}
		logger.Info("TLS enabled")
	} else if *tlsCert != "" || *tlsKey != "" {
		logger.Fatal("Both -tls-cert and -tls-key must be provided to enable TLS")
	}

	// Create server
	server := internal.NewServer(*host, *port, systemPrompt, tlsConfig)

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
