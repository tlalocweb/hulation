package unified

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	proxyproto "github.com/pires/go-proxyproto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/server/static"
	"github.com/tlalocweb/hulation/pkg/tune"
)

var unifiedLogger = log.GetTaggedLogger("unified", "Unified HTTPS server")

// Server provides a unified HTTPS server that handles both gRPC and REST on the same port
// This follows the izcr pattern where a single http.Server handles both protocols
type Server struct {
	grpcServer             *grpc.Server
	gatewayMux             *runtime.ServeMux               // grpc-gateway mux for REST
	httpMiddleware         func(http.Handler) http.Handler // Optional HTTP middleware for REST
	httpRegistryMiddleware func(http.Handler) http.Handler // Optional HTTP middleware for Registry
	registryServer         http.Handler                    // Registry server handler
	registryHostnames      map[string]bool                 // Hostnames that route to registry
	staticHandler          *static.StaticHandler           // Static file server handler
	hostCerts              map[string]*tls.Certificate     // Per-host TLS certificates (for static sites)
	httpServer             *http.Server
	httpRedirectServer     *http.Server // HTTP server that redirects to HTTPS
	address                string
	tlsCertFile            string
	tlsKeyFile             string
	logger                 *log.TaggedLogger
	externalCert           *tls.Certificate // External/well-known CA cert for API clients
	internalCert           *tls.Certificate // Internal CA cert for izcragent mTLS
	customHandlers         map[string]http.HandlerFunc // Custom HTTP handlers — exact path match
	customMux              *http.ServeMux              // Go 1.22+ pattern ServeMux for path-parameter routes
	// dynamicGetCert is a caller-supplied TLS cert source consulted AFTER
	// per-host static certs and BEFORE the externalCert fallback. Primary
	// use: ACME autocert.Manager.GetCertificate.
	dynamicGetCert func(*tls.ClientHelloInfo) (*tls.Certificate, error)
}

// Config holds the configuration for the unified server
type Config struct {
	// Address to bind the server (e.g., "0.0.0.0:8443")
	Address string

	// TLS certificate file path (external/well-known CA)
	TLSCertFile string

	// TLS key file path (external/well-known CA)
	TLSKeyFile string

	// ClientCAs is the CA pool for validating client certificates (mTLS)
	// If set, client certificate verification is enabled but not required
	// This allows agents with valid certificates to authenticate via mTLS
	// while still allowing regular clients without certificates
	ClientCAs *x509.CertPool

	// InternalCertData is the PEM-encoded internal server certificate
	// Used for mTLS connections with izcragent (signed by internal CA)
	InternalCertData string

	// InternalKeyData is the PEM-encoded internal server private key
	InternalKeyData string

	// Additional gRPC server options
	GRPCServerOptions []grpc.ServerOption

	// Additional ServeMux options
	MuxOptions []runtime.ServeMuxOption

	// GetCertificate, if non-nil, is used as the last-resort TLS
	// certificate selector — after per-host static certs attached via
	// AttachStaticHandler and after the internal cert for mTLS clients.
	//
	// Typical use: plug an autocert.Manager for ACME / Let's Encrypt
	// issuance. When GetCertificate is set, TLSCertFile and TLSKeyFile
	// may be left empty; the server will not require on-disk cert
	// files. When TLSCertFile/TLSKeyFile ARE set alongside
	// GetCertificate, the static cert is kept in the map as a fallback
	// for hostnames the provided function rejects (useful for localhost
	// probes during ACME issuance).
	GetCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error)
}

// NewServer creates a new unified HTTPS server
func NewServer(cfg *Config) (*Server, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	if cfg.Address == "" {
		cfg.Address = "0.0.0.0:18443"
	}

	// Either static cert files OR a GetCertificate callback (or both) must
	// be supplied. GetCertificate on its own is sufficient for ACME-style
	// managers that issue certs on demand.
	if (cfg.TLSCertFile == "" || cfg.TLSKeyFile == "") && cfg.GetCertificate == nil {
		return nil, fmt.Errorf("TLS certificate and key files are required (or supply GetCertificate)")
	}

	// Load the static external cert if files were supplied. Otherwise
	// externalCert stays at its zero value — the GetCertificate callback
	// is the sole source of truth.
	var externalCert tls.Certificate
	if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
		var err error
		externalCert, err = tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load TLS certificate: %w", err)
		}
	}

	// Load internal certificate if provided (for izcragent mTLS)
	var internalCert *tls.Certificate
	if cfg.InternalCertData != "" && cfg.InternalKeyData != "" {
		unifiedLogger.Debugf("Loading internal certificate data (cert length: %d, key length: %d)",
			len(cfg.InternalCertData), len(cfg.InternalKeyData))
		cert, err := tls.X509KeyPair([]byte(cfg.InternalCertData), []byte(cfg.InternalKeyData))
		if err != nil {
			return nil, fmt.Errorf("failed to load internal certificate: %w", err)
		}
		internalCert = &cert
		unifiedLogger.Infof("Successfully loaded internal server certificate for mTLS")
	} else {
		unifiedLogger.Warnf("No internal certificate data provided - agents will use external certificate")
	}

	// Create gRPC server (no TLS credentials here - handled at HTTP level)
	grpcServer := grpc.NewServer(cfg.GRPCServerOptions...)

	// Enable reflection for debugging
	reflection.Register(grpcServer)

	// Create grpc-gateway mux for REST
	gatewayMux := runtime.NewServeMux(cfg.MuxOptions...)

	// Create server instance first (needed for selectCertificate method reference)
	server := &Server{
		grpcServer:       grpcServer,
		gatewayMux:       gatewayMux,
		address:          cfg.Address,
		tlsCertFile:      cfg.TLSCertFile,
		tlsKeyFile:       cfg.TLSKeyFile,
		logger:           unifiedLogger,
		internalCert:     internalCert,
		hostCerts:        make(map[string]*tls.Certificate),
		customHandlers:   make(map[string]http.HandlerFunc),
		customMux:        http.NewServeMux(),
		dynamicGetCert:   cfg.GetCertificate,
	}
	if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
		server.externalCert = &externalCert
	}

	// Create TLS config with dynamic certificate selection
	tlsConfig := &tls.Config{
		NextProtos: []string{"h2", "http/1.1"}, // Support HTTP/2 for gRPC
	}

	// Always use the dynamic selector. selectCertificate handles the
	// full priority pipeline: per-host SNI → internal mTLS → caller-
	// supplied GetCertificate (e.g. ACME) → static external cert. This
	// lets callers add per-host certs AFTER NewServer via
	// AddHostCertificate / AttachStaticHandler without rewriting the
	// TLS config.
	tlsConfig.GetCertificate = func(clientHello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		return server.selectCertificate(clientHello)
	}
	if cfg.GetCertificate != nil {
		unifiedLogger.Infof("Dynamic certificate selection enabled (caller-supplied GetCertificate, e.g. ACME)")
	} else {
		unifiedLogger.Infof("Dynamic certificate selection enabled")
	}

	// Enable mTLS if ClientCAs is configured
	// VerifyClientCertIfGiven allows clients without certificates to connect
	// while also validating certificates when they are provided (for agents)
	if cfg.ClientCAs != nil {
		tlsConfig.ClientAuth = tls.VerifyClientCertIfGiven
		tlsConfig.ClientCAs = cfg.ClientCAs
		unifiedLogger.Infof("mTLS enabled for agent authentication")
	}

	// Create HTTP server with routing handler.
	//
	// The dispatcher must be re-evaluated on each request because the
	// middleware stack (AttachHTTPMiddleware) and late-registered
	// handlers (backend proxies, per-host static sites) are wired up
	// AFTER NewServer returns but BEFORE Start. Capturing the result of
	// grpcHandlerOrPassFunc() once at construction time would freeze the
	// middleware chain as "nil", silently dropping every middleware
	// attached during boot.
	server.httpServer = &http.Server{
		Addr: cfg.Address,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			server.grpcHandlerOrPassFunc().ServeHTTP(w, r)
		}),
		TLSConfig: tlsConfig,
	}

	unifiedLogger.Infof("Unified HTTPS server created on %s", cfg.Address)
	return server, nil
}

// AttachGRPCServer allows setting the gRPC server (for testing or advanced use)
func (s *Server) AttachGRPCServer(grpcServer *grpc.Server) {
	s.grpcServer = grpcServer
}

// GetGRPCServer returns the underlying gRPC server for service registration
func (s *Server) GetGRPCServer() *grpc.Server {
	return s.grpcServer
}

// GetGatewayMux returns the grpc-gateway mux for REST endpoint registration
func (s *Server) GetGatewayMux() *runtime.ServeMux {
	return s.gatewayMux
}

func (s *Server) AttachRegistryMiddleware(middleware func(http.Handler) http.Handler) {
	s.httpRegistryMiddleware = middleware
	s.logger.Infof("Registry middleware attached")
}

// AttachHTTPMiddleware appends HTTP middleware that will wrap REST API
// requests. Multiple calls compose — the most-recently-attached
// middleware is outermost (runs first), so later handlers can decide
// to pass control to earlier ones via next.ServeHTTP.
func (s *Server) AttachHTTPMiddleware(middleware func(http.Handler) http.Handler) {
	if s.httpMiddleware == nil {
		s.httpMiddleware = middleware
	} else {
		prev := s.httpMiddleware
		s.httpMiddleware = func(next http.Handler) http.Handler {
			return middleware(prev(next))
		}
	}
	s.logger.Infof("HTTP middleware attached")
}

// AttachRegistryServer configures the registry server with hostname-based routing
// hostnames is a comma-separated list of hostnames that should route to the registry
func (s *Server) AttachRegistryServer(registryServer http.Handler, hostnames string) {
	s.registryServer = registryServer
	s.registryHostnames = make(map[string]bool)

	if hostnames != "" {
		for _, hostname := range strings.Split(hostnames, ",") {
			hostname = strings.TrimSpace(hostname)
			if hostname != "" {
				s.registryHostnames[hostname] = true
				s.logger.Infof("Registry server will handle requests for hostname: %s", hostname)
			}
		}
	}
}

// AttachStaticHandler configures the static file server with per-host TLS certificates
func (s *Server) AttachStaticHandler(handler *static.StaticHandler) {
	s.staticHandler = handler

	// Merge host certificates from static handler
	for hostname, cert := range handler.GetHostCertificates() {
		s.hostCerts[hostname] = cert
		s.logger.Infof("Static host TLS certificate registered for: %s", hostname)
	}

	s.logger.Infof("Static file handler attached for hosts: %v", handler.Hosts())
}

// RegisterCustomHandler registers a custom HTTP handler for a specific
// path. Patterns may use Go 1.22+ ServeMux syntax including method
// prefixes ("POST /v/sub/{formid}") and path parameters; those are
// dispatched by an internal http.ServeMux that preserves those
// semantics. Plain paths without method prefix or parameters hit a
// simpler map (exact-match) for the common case.
func (s *Server) RegisterCustomHandler(path string, handler http.HandlerFunc) {
	// Route to ServeMux when the pattern uses method prefix or path
	// parameters; otherwise store in the map for O(1) exact lookup.
	if strings.ContainsAny(path, " {") {
		s.customMux.HandleFunc(path, handler)
	} else {
		s.customHandlers[path] = handler
	}
	s.logger.Infof("Custom HTTP handler registered for path: %s", path)
}

// HasRoute reports whether the unified server has a registered handler
// (customHandlers exact match, customMux pattern match, or grpc-gateway
// /api/v1 prefix) that would claim this request in the core dispatcher.
//
// Middleware (e.g., per-host backend proxies) should call this before
// claiming a request so reserved admin paths keep their handlers rather
// than being greedily proxied to a container mounted on /api or another
// overlapping prefix. This preserves the legacy Fiber behavior where
// named admin routes took precedence over /api/* virtual paths.
func (s *Server) HasRoute(r *http.Request) bool {
	if _, ok := s.customHandlers[r.URL.Path]; ok {
		return true
	}
	if _, pattern := s.customMux.Handler(r); pattern != "" {
		return true
	}
	// grpc-gateway REST endpoints live under /api/v1.
	if strings.HasPrefix(r.URL.Path, "/api/v1/") || r.URL.Path == "/api/v1" {
		return true
	}
	return false
}

// AddHostCertificate registers a per-host TLS certificate. Requests
// whose SNI ServerName matches the given hostname receive this cert;
// all other SNIs fall through to the rest of the selection pipeline
// (internal cert → dynamicGetCert → static externalCert).
//
// Must be called AFTER NewServer and BEFORE Start. The server's TLS
// config is already set up to consult selectCertificate when any
// per-host cert is registered.
func (s *Server) AddHostCertificate(hostname string, cert *tls.Certificate) {
	if cert == nil || hostname == "" {
		return
	}
	s.hostCerts[hostname] = cert
	s.logger.Infof("Host TLS certificate registered for: %s", hostname)
}

// LoadHostCertificate loads a cert+key pair from disk and registers it
// for the given hostname. Convenience wrapper over AddHostCertificate.
func (s *Server) LoadHostCertificate(hostname, certFile, keyFile string) error {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return fmt.Errorf("load cert for %s: %w", hostname, err)
	}
	s.AddHostCertificate(hostname, &cert)
	return nil
}

// grpcHandlerOrPassFunc routes requests with five-tier priority:
// 1. HIGHEST: gRPC (HTTP/2 with application/grpc content-type)
// 2. Custom HTTP handlers (e.g., OAuth callbacks)
// 3. REST API via grpc-gateway (paths starting with /api/)
// 4. Registry (based on hostname match + registry paths)
// 5. Static file serving (hostname match)
// 6. DEFAULT: 404 for unmatched requests
// This follows the izcr pattern
func (s *Server) grpcHandlerOrPassFunc() http.Handler {
	// Core dispatcher: gRPC → customHandlers → gateway → registry → static → 404.
	core := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. HIGHEST PRIORITY: Check if this is a gRPC request
		if r.ProtoMajor == 2 && strings.Contains(r.Header.Get("Content-Type"), "application/grpc") {
			if s.grpcServer != nil {
				s.grpcServer.ServeHTTP(w, r)
			} else {
				s.logger.Errorf("gRPC not configured")
				http.Error(w, "gRPC not configured", http.StatusInternalServerError)
			}
			return
		}

		// 2. Custom HTTP handlers (e.g., OAuth callbacks). Exact path
		// map first, then the pattern-capable ServeMux for registrations
		// that use method prefix or {param} placeholders.
		if handler, ok := s.customHandlers[r.URL.Path]; ok {
			handler(w, r)
			return
		}
		if _, pattern := s.customMux.Handler(r); pattern != "" {
			s.customMux.ServeHTTP(w, r)
			return
		}

		// 3. REST API via grpc-gateway (paths starting with /api/)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.gatewayMux.ServeHTTP(w, r)
			return
		}

		// 3. Registry (based on hostname match + registry paths)
		if s.registryServer != nil && len(s.registryHostnames) > 0 {
			hostname := strings.Split(r.Host, ":")[0] // Strip port
			if s.registryHostnames[hostname] {
				// Only route to registry for OCI registry paths (/v2/, /token)
				// This allows localhost to be used for both API and registry
				path := r.URL.Path
				isRegistryPath := strings.HasPrefix(path, "/v2") ||
					path == "/token" ||
					strings.HasPrefix(path, "/token?") ||
					path == "/auth/token" ||
					strings.HasPrefix(path, "/auth/token?")
				if isRegistryPath {
					// Apply registry middleware if configured, otherwise pass directly to registry
					handler := s.registryServer
					if s.httpRegistryMiddleware != nil {
						handler = s.httpRegistryMiddleware(s.registryServer)
					}
					handler.ServeHTTP(w, r)
					return
				}
			}
		}

		// 4. Static file serving (hostname match)
		if s.staticHandler != nil && s.staticHandler.ServeIfMatch(w, r) {
			return
		}

		// 5. DEFAULT: 404 for unmatched requests
		http.NotFound(w, r)
	})

	// Wrap the core dispatcher with any attached HTTP middleware so host-
	// level routers (per-host backend proxies, static-site serving,
	// visitor-tracking endpoints registered by the caller) see every
	// incoming request — not just /api/* — and can decide to handle it
	// themselves or pass through to the core router via next.
	if s.httpMiddleware != nil {
		return s.httpMiddleware(core)
	}
	return core
}

// Start begins serving HTTPS requests (both gRPC and REST)
// Also handles HTTP requests by redirecting them to HTTPS (for Docker compatibility)
func (s *Server) Start(ctx context.Context) error {
	s.logger.Infof("Starting unified HTTPS server on %s", s.address)

	// Bind to the port synchronously so we can return an error immediately if it fails
	listener, err := net.Listen("tcp", s.address)
	if err != nil {
		return fmt.Errorf("failed to bind to %s: %w", s.address, err)
	}

	// Wrap with PROXY protocol listener if enabled (auto-detect mode)
	// This allows load balancers (nginx stream, HAProxy, AWS NLB) to pass original client IP
	if tune.GetProxyProtocolEnabled() {
		listener = &proxyproto.Listener{
			Listener: listener,
			Policy: func(upstream net.Addr) (proxyproto.Policy, error) {
				// USE policy: auto-detect PROXY header, use if present, pass through if absent
				return proxyproto.USE, nil
			},
		}
		s.logger.Infof("PROXY protocol auto-detection enabled")
	}

	// Create a protocol-detecting listener that can handle both HTTP and HTTPS
	// This is needed for Docker clients that try HTTP first on non-standard ports
	protoListener := &protocolDetectingListener{
		Listener:  listener,
		tlsConfig: s.httpServer.TLSConfig,
		logger:    s.logger,
		address:   s.address,
	}

	// Start serving with protocol detection in a goroutine
	go func() {
		if err := s.httpServer.Serve(protoListener); err != nil && err != http.ErrServerClosed {
			s.logger.Errorf("Unified HTTPS server error: %v", err)
		}
	}()

	return nil
}

// Stop gracefully stops the unified server
func (s *Server) Stop(ctx context.Context) error {
	s.logger.Infof("Stopping unified HTTPS server")

	// Stop gRPC server
	s.grpcServer.GracefulStop()

	// Stop HTTP server
	if s.httpServer != nil {
		if err := s.httpServer.Shutdown(ctx); err != nil {
			return fmt.Errorf("failed to shutdown HTTP server: %w", err)
		}
	}

	// Stop HTTP redirect server if it was started
	if s.httpRedirectServer != nil {
		if err := s.httpRedirectServer.Shutdown(ctx); err != nil {
			s.logger.Warnf("Failed to shutdown HTTP redirect server: %v", err)
		}
	}

	return nil
}

// GetAddress returns the address the server is listening on
func (s *Server) GetAddress() string {
	return s.address
}

// StartHTTPRedirect starts an HTTP server that redirects all requests to HTTPS
// This is useful for Docker clients that try HTTP first on non-standard ports
func (s *Server) StartHTTPRedirect(ctx context.Context, httpAddr string) error {
	if httpAddr == "" {
		return nil // No HTTP redirect configured
	}

	s.logger.Infof("Starting HTTP redirect server on %s -> HTTPS", httpAddr)

	redirectHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Build the HTTPS URL
		host := r.Host
		// Handle case where port is in host - replace with HTTPS port
		if colonPos := strings.LastIndex(host, ":"); colonPos != -1 {
			host = host[:colonPos]
		}
		// Append HTTPS port if not 443
		httpsPort := strings.TrimPrefix(s.address, ":")
		if httpsPort != "443" && httpsPort != "" {
			host = host + ":" + httpsPort
		}

		target := "https://" + host + r.URL.RequestURI()
		s.logger.Debugf("Redirecting HTTP request to: %s", target)
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})

	s.httpRedirectServer = &http.Server{
		Addr:    httpAddr,
		Handler: redirectHandler,
	}

	go func() {
		if err := s.httpRedirectServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Errorf("HTTP redirect server error: %v", err)
		}
	}()

	return nil
}

// selectCertificate dynamically chooses between external and internal certificates
// based on the TLS handshake information (SNI ServerName)
func (s *Server) selectCertificate(clientHello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	serverName := clientHello.ServerName
	s.logger.VDebugf(2, "Certificate selection for ServerName: '%s' (length: %d)", serverName, len(serverName))

	// Check for per-host certificate (static sites)
	if cert, ok := s.hostCerts[serverName]; ok {
		s.logger.VDebugf(2, "Using per-host certificate for ServerName: %s", serverName)
		return cert, nil
	}

	// FUTURE STUB: FleetCA support would check fleetID here
	// When FleetCA APIs are implemented, selection logic would check if ServerName
	// matches a fleet ID and use the appropriate fleet-specific certificate.
	// if isFleetID(serverName) && s.fleetCertCallback != nil {
	//     return s.fleetCertCallback(serverName)
	// }

	// Agent node_id format: node-<base64_random> (e.g., node-abc123XYZ...)
	// Server node_id format is the same: node-<base64_random>
	// Standard hostnames contain dots (e.g., api.example.com)
	// Use internal certificate for any SNI with "node-" prefix
	if strings.HasPrefix(serverName, "node-") {
		if s.internalCert != nil {
			s.logger.Infof("Using internal certificate for agent ServerName: %s", serverName)
			return s.internalCert, nil
		}
		s.logger.Warnf("Internal certificate requested but not configured for ServerName: %s", serverName)
	}

	// Caller-supplied dynamic cert source (e.g., autocert.Manager for
	// ACME). Consulted before the static externalCert so hostnames the
	// manager knows about get freshly-issued certs; hostnames it rejects
	// fall through to the static cert below (useful for localhost probes,
	// health checks, and any SNI the ACME manager isn't configured for).
	if s.dynamicGetCert != nil {
		cert, err := s.dynamicGetCert(clientHello)
		if err == nil && cert != nil {
			s.logger.VDebugf(2, "Using caller-supplied dynamic certificate for ServerName: %s", serverName)
			return cert, nil
		}
		if err != nil {
			s.logger.VDebugf(2, "dynamicGetCert rejected ServerName %q: %v", serverName, err)
		}
	}

	// Default to external certificate for hostname-based connections
	if s.externalCert != nil {
		s.logger.VDebugf(2, "Using external certificate for hostname ServerName: %s", serverName)
		return s.externalCert, nil
	}
	return nil, fmt.Errorf("no certificate available for ServerName %q", serverName)
}

// protocolDetectingListener wraps a net.Listener to detect HTTP vs TLS connections
// This allows handling Docker clients that try HTTP first on non-standard ports
type protocolDetectingListener struct {
	net.Listener
	tlsConfig *tls.Config
	logger    *log.TaggedLogger
	address   string
}

// Accept waits for and returns the next connection to the listener
// It peeks at the first byte to determine if it's HTTP or TLS
func (l *protocolDetectingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}

	// Create a peeking connection to read the first byte without consuming it
	peekConn := &peekableConn{Conn: conn}

	// Read the first byte to detect protocol. A client that RSTs
	// before sending any bytes (scanners, half-open probes, TLS
	// handshake errors mid-peek) lands here — wrap in temporaryError
	// so http.Server.Serve keeps the outer listener alive. The Phase-0
	// version returned the raw err, which Go's Serve treats as fatal
	// and propagates into closing the listener, taking the whole
	// process off-line.
	firstByte, err := peekConn.Peek()
	if err != nil {
		conn.Close()
		l.logger.Debugf("protocol peek failed: %v", err)
		return nil, &temporaryError{msg: "protocol peek failed: " + err.Error()}
	}

	// TLS handshake starts with 0x16 (handshake) followed by version bytes
	// HTTP requests start with ASCII characters (GET, POST, HEAD, etc.)
	if firstByte == 0x16 {
		// This is a TLS connection - wrap with TLS and return
		return tls.Server(peekConn, l.tlsConfig), nil
	}

	// This is an HTTP connection - send redirect and close
	l.logger.Debugf("Detected plain HTTP connection, sending redirect to HTTPS")
	l.handleHTTPRedirect(peekConn)
	return nil, &temporaryError{msg: "redirected HTTP to HTTPS"}
}

// handleHTTPRedirect reads the HTTP request and sends a redirect response
func (l *protocolDetectingListener) handleHTTPRedirect(conn net.Conn) {
	defer conn.Close()

	// Read the HTTP request line to get the path
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return
	}

	request := string(buf[:n])
	lines := strings.Split(request, "\r\n")
	if len(lines) == 0 {
		return
	}

	// Parse the request line: "GET /path HTTP/1.1"
	parts := strings.Fields(lines[0])
	path := "/"
	if len(parts) >= 2 {
		path = parts[1]
	}

	// Get Host header
	host := ""
	for _, line := range lines {
		if strings.HasPrefix(strings.ToLower(line), "host:") {
			host = strings.TrimSpace(line[5:])
			break
		}
	}

	// Build redirect URL
	httpsPort := strings.TrimPrefix(l.address, ":")
	if host != "" {
		// Strip port from host if present
		if colonPos := strings.LastIndex(host, ":"); colonPos != -1 {
			host = host[:colonPos]
		}
		if httpsPort != "443" {
			host = host + ":" + httpsPort
		}
	} else {
		host = "localhost"
		if httpsPort != "443" {
			host = host + ":" + httpsPort
		}
	}

	redirectURL := "https://" + host + path

	// Send HTTP redirect response
	body := fmt.Sprintf("<a href=\"%s\">Moved Permanently</a>.\n", redirectURL)
	response := fmt.Sprintf("HTTP/1.1 301 Moved Permanently\r\n"+
		"Location: %s\r\n"+
		"Content-Type: text/html\r\n"+
		"Content-Length: %d\r\n"+
		"Connection: close\r\n"+
		"\r\n"+
		"%s",
		redirectURL, len(body), body)

	conn.Write([]byte(response))
}

// temporaryError is used to signal a temporary condition (redirect sent)
type temporaryError struct {
	msg string
}

func (e *temporaryError) Error() string   { return e.msg }
func (e *temporaryError) Temporary() bool { return true }
func (e *temporaryError) Timeout() bool   { return false }

// peekableConn wraps a net.Conn to allow peeking at the first byte
type peekableConn struct {
	net.Conn
	firstByte byte
	peeked    bool
	consumed  bool
}

// Peek reads and stores the first byte without consuming it
func (c *peekableConn) Peek() (byte, error) {
	if c.peeked {
		return c.firstByte, nil
	}

	buf := make([]byte, 1)
	n, err := c.Conn.Read(buf)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, fmt.Errorf("no data read")
	}

	c.firstByte = buf[0]
	c.peeked = true
	return c.firstByte, nil
}

// Read implements net.Conn.Read, prepending the peeked byte if not consumed
func (c *peekableConn) Read(b []byte) (int, error) {
	if c.peeked && !c.consumed {
		c.consumed = true
		if len(b) == 0 {
			return 0, nil
		}
		b[0] = c.firstByte
		if len(b) == 1 {
			return 1, nil
		}
		n, err := c.Conn.Read(b[1:])
		return n + 1, err
	}
	return c.Conn.Read(b)
}
