package internal

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"go.uber.org/zap"
	"golang.org/x/crypto/acme/autocert"
)

// ACME obtains and renews a TLS certificate for a single domain from Let's
// Encrypt, answering TLS-ALPN-01 challenges on its own TLS listener.
type ACME struct {
	domain  string
	tlsAddr string
	manager *autocert.Manager
}

// NewACME creates an ACME certificate manager for domain. Certificates and the
// account key are persisted in cacheDir, which should survive restarts to avoid
// hitting Let's Encrypt rate limits.
func NewACME(domain, cacheDir, email, tlsAddr string) *ACME {
	return &ACME{
		domain:  domain,
		tlsAddr: tlsAddr,
		manager: &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Cache:      autocert.DirCache(cacheDir),
			HostPolicy: autocert.HostWhitelist(domain),
			Email:      email,
		},
	}
}

// TLSConfig returns a TLS config that serves the managed certificate to every
// client, regardless of the SNI it sends (if any). Clients that connected via a
// different name then fail their own hostname verification with a clear error,
// rather than getting an opaque handshake failure from the server.
func (a *ACME) TLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			h := *hello
			h.ServerName = a.domain
			return a.manager.GetCertificate(&h)
		},
	}
}

// Serve answers TLS-ALPN-01 challenges until ctx is cancelled. It also obtains
// (or renews) the certificate in the background at startup, so the first
// client doesn't have to wait for issuance.
func (a *ACME) Serve(ctx context.Context) error {
	logger := LoggerFromContext(ctx)

	listener, err := net.Listen("tcp", a.tlsAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", a.tlsAddr, err)
	}
	// Handshake errors from internet scanners are common and uninteresting.
	errorLog, err := zap.NewStdLogAt(logger, zap.DebugLevel)
	if err != nil {
		return fmt.Errorf("failed to create ACME challenge server logger: %w", err)
	}

	// Challenges are answered during the TLS handshake; nothing is served
	// over HTTP. Not calling manager.HTTPHandler keeps autocert from trying
	// HTTP-01.
	server := &http.Server{
		Handler:           http.NotFoundHandler(),
		TLSConfig:         a.manager.TLSConfig(),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          errorLog,
	}
	go func() {
		// The cipher suite makes autocert pick the ECDSA certificate that
		// real clients will be served.
		_, err := a.manager.GetCertificate(&tls.ClientHelloInfo{
			ServerName:   a.domain,
			CipherSuites: []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
		})
		if err != nil {
			logger.Error("Failed to obtain ACME certificate", zap.Error(err))
			return
		}
		logger.Info("ACME certificate ready", zap.String("domain", a.domain))
	}()

	logger.Info("ACME challenge server listening", zap.String("address", a.tlsAddr))
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.ServeTLS(listener, "", "")
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("ACME challenge server failed: %w", err)
	case <-ctx.Done():
		if err := server.Close(); err != nil {
			return fmt.Errorf("failed to close ACME challenge server: %w", err)
		}
		if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("ACME challenge server failed: %w", err)
		}
		return nil
	}
}
