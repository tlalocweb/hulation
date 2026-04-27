package http

import (
	"context"
	"fmt"
	"net/http"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/tlalocweb/hulation/log"
)

var httpLogger = log.GetTaggedLogger("http", "HTTP gateway")

// Gateway provides HTTP/REST access to gRPC services
type Gateway struct {
	mux        *runtime.ServeMux
	server     *http.Server
	grpcAddr   string
	httpAddr   string
	logger     *log.TaggedLogger
	registrars []ServiceRegistrar
}

// Config holds the configuration for the HTTP gateway
type Config struct {
	// Address to bind the HTTP server (e.g., "0.0.0.0:8080")
	HTTPAddress string

	// Address of the gRPC server to proxy to (e.g., "localhost:9090")
	GRPCAddress string

	// Additional ServeMux options
	MuxOptions []runtime.ServeMuxOption
}

// ServiceRegistrar is a function that registers a service with the gateway
type ServiceRegistrar func(ctx context.Context, mux *runtime.ServeMux, endpoint string, opts []grpc.DialOption) error

// NewGateway creates a new HTTP gateway
func NewGateway(cfg *Config) (*Gateway, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	if cfg.HTTPAddress == "" {
		cfg.HTTPAddress = "0.0.0.0:8080"
	}

	if cfg.GRPCAddress == "" {
		cfg.GRPCAddress = "localhost:9090"
	}

	// Create ServeMux with custom options
	mux := runtime.NewServeMux(cfg.MuxOptions...)

	gateway := &Gateway{
		mux:        mux,
		grpcAddr:   cfg.GRPCAddress,
		httpAddr:   cfg.HTTPAddress,
		logger:     httpLogger,
		registrars: make([]ServiceRegistrar, 0),
	}

	httpLogger.Infof("HTTP gateway created (HTTP: %s, gRPC: %s)", cfg.HTTPAddress, cfg.GRPCAddress)
	return gateway, nil
}

// RegisterService adds a service registrar to the gateway
func (g *Gateway) RegisterService(registrar ServiceRegistrar) {
	g.registrars = append(g.registrars, registrar)
}

// Start begins serving HTTP requests
func (g *Gateway) Start(ctx context.Context) error {
	// Register all services
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}

	for _, registrar := range g.registrars {
		if err := registrar(ctx, g.mux, g.grpcAddr, opts); err != nil {
			return fmt.Errorf("failed to register service: %w", err)
		}
	}

	g.logger.Infof("All services registered, starting HTTP server on %s", g.httpAddr)

	// Create HTTP server
	g.server = &http.Server{
		Addr:    g.httpAddr,
		Handler: g.mux,
	}

	// Start server
	if err := g.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("HTTP server failed: %w", err)
	}

	return nil
}

// Stop gracefully stops the HTTP gateway
func (g *Gateway) Stop(ctx context.Context) error {
	g.logger.Infof("Stopping HTTP gateway")
	if g.server != nil {
		return g.server.Shutdown(ctx)
	}
	return nil
}

// GetMux returns the underlying ServeMux for custom handler registration
func (g *Gateway) GetMux() *runtime.ServeMux {
	return g.mux
}
