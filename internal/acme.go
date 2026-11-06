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
// Encrypt, answering HTTP-01 challenges on its own HTTP listener.
type ACME struct {
	domain   string
	httpAddr string
	manager  *autocert.Manager
}

// NewACME creates an ACME certificate manager for domain. Certificates and the
// account key are persisted in cacheDir, which should survive restarts to avoid
// hitting Let's Encrypt rate limits.
func NewACME(domain, cacheDir, email, httpAddr string) *ACME {
	return &ACME{
		domain:   domain,
		httpAddr: httpAddr,
		manager: &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Cache:      autocert.DirCache(cacheDir),
			HostPolicy: autocert.HostWhitelist(domain),
			Email:      email,
		},
	}
}

// TLSConfig returns a TLS config that serves the managed certificate.
func (a *ACME) TLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			// Clients connecting by IP address don't send SNI.
			if hello.ServerName == "" {
				hello.ServerName = a.domain
			}
			return a.manager.GetCertificate(hello)
		},
	}
}

// Serve answers HTTP-01 challenges until ctx is cancelled. It also obtains (or
// renews) the certificate in the background at startup, so the first client
// doesn't have to wait for issuance.
func (a *ACME) Serve(ctx context.Context) error {
	logger := LoggerFromContext(ctx)

	listener, err := net.Listen("tcp", a.httpAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", a.httpAddr, err)
	}
	server := &http.Server{
		Handler:           a.manager.HTTPHandler(http.NotFoundHandler()),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		server.Close()
	}()

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

	logger.Info("ACME HTTP challenge server listening", zap.String("address", a.httpAddr))
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("ACME HTTP challenge server failed: %w", err)
	}
	return nil
}
