package grpc

import (
	"fmt"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"github.com/tlalocweb/hulation/log"
)

var grpcLogger = log.GetTaggedLogger("grpc", "gRPC server")

// Server wraps the gRPC server with configuration and lifecycle management
type Server struct {
	grpcServer *grpc.Server
	listener   net.Listener
	address    string
	logger     *log.TaggedLogger
}

// Config holds the configuration for the gRPC server
type Config struct {
	// Address to bind the gRPC server (e.g., "0.0.0.0:9090")
	Address string

	// Additional gRPC server options
	ServerOptions []grpc.ServerOption
}

// NewServer creates a new gRPC server
func NewServer(cfg *Config) (*Server, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	if cfg.Address == "" {
		cfg.Address = "0.0.0.0:9090"
	}

	// Create listener
	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("failed to create listener: %w", err)
	}

	// Create gRPC server with options
	grpcServer := grpc.NewServer(cfg.ServerOptions...)

	// Enable reflection for debugging (can be disabled in production)
	reflection.Register(grpcServer)

	server := &Server{
		grpcServer: grpcServer,
		listener:   listener,
		address:    cfg.Address,
		logger:     grpcLogger,
	}

	grpcLogger.Infof("gRPC server created on %s", cfg.Address)
	return server, nil
}

// GetGRPCServer returns the underlying gRPC server for service registration
func (s *Server) GetGRPCServer() *grpc.Server {
	return s.grpcServer
}

// Start begins serving gRPC requests
func (s *Server) Start() error {
	s.logger.Infof("Starting gRPC server on %s", s.address)

	if err := s.grpcServer.Serve(s.listener); err != nil {
		return fmt.Errorf("gRPC server failed: %w", err)
	}

	return nil
}

// Stop gracefully stops the gRPC server
func (s *Server) Stop() error {
	s.logger.Infof("Stopping gRPC server")
	s.grpcServer.GracefulStop()
	return nil
}

// GetAddress returns the address the server is listening on
func (s *Server) GetAddress() string {
	return s.listener.Addr().String()
}
