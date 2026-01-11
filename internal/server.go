package internal

import (
	"context"
	"fmt"
	"net"
	"sync"
)

// Server handles incoming TCP connections and manages connection lifecycle
type Server struct {
	host       string
	port       int
	wg         sync.WaitGroup
	connCtx    context.Context
	connCancel context.CancelFunc
}

// NewServer creates a new Server instance
func NewServer(host string, port int) *Server {
	return &Server{
		host: host,
		port: port,
	}
}

// Start begins accepting connections and blocks until ctx is cancelled
func (s *Server) Start(ctx context.Context) error {
	logger := LoggerFromContext(ctx)

	// Create child context for all connections
	s.connCtx, s.connCancel = context.WithCancel(ctx)

	// Create TCP listener
	address := fmt.Sprintf("%s:%d", s.host, s.port)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", address, err)
	}
	defer listener.Close()

	logger.Info("server listening", "address", address)

	// Accept connections in a loop
	for {
		// Check if context is cancelled
		select {
		case <-ctx.Done():
			logger.Info("server stopping accept loop")
			return nil
		default:
		}

		// Accept connection (this will block)
		conn, err := listener.Accept()
		if err != nil {
			// Check if we should stop due to context cancellation
			select {
			case <-ctx.Done():
				return nil
			default:
				logger.Error("failed to accept connection", "error", err)
				continue
			}
		}

		// Create child logger with remote address
		connLogger := logger.With("remote_addr", conn.RemoteAddr().String())
		connCtx := ContextWithLogger(s.connCtx, connLogger)

		// Spawn goroutine to handle connection
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			connection := NewConnection(conn)
			if err := connection.Handle(connCtx); err != nil {
				LoggerFromContext(connCtx).Error("connection error", "error", err)
			}
		}()
	}
}

// Close terminates all active connections and waits for cleanup
func (s *Server) Close() {
	// Cancel all connection contexts
	if s.connCancel != nil {
		s.connCancel()
	}
	// Wait for all connections to finish
	s.wg.Wait()
}
