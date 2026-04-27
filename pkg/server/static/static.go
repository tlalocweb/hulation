package static

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
)

var logstatic = log.GetTaggedLogger("static", "Static file server")

// hostHandler handles static file serving for a single host
type hostHandler struct {
	server            string
	root              string
	spa               bool
	handler           http.Handler
	cert              *tls.Certificate  // Optional per-host TLS certificate
	logAccess         bool              // Log access to static files
	logAccessLogLevel string            // Log level for access logs
	mimeOverrides     map[string]string // Custom MIME type overrides by extension
}

// StaticHandler handles hostname-based static file serving
type StaticHandler struct {
	hosts      map[string]*hostHandler
	debugLevel int
}

// NewStaticHandler creates a new static file handler from the given configurations
func NewStaticHandler(configs []*config.StaticHostConfig, debugLevel int) (*StaticHandler, error) {
	s := &StaticHandler{
		hosts:      make(map[string]*hostHandler),
		debugLevel: debugLevel,
	}

	for _, cfg := range configs {
		if cfg.Server == "" {
			return nil, fmt.Errorf("static host config missing 'server' field")
		}
		if cfg.Root == "" {
			return nil, fmt.Errorf("static host config for %s missing 'root' field", cfg.Server)
		}

		// Validate that the root directory exists
		info, err := os.Stat(cfg.Root)
		if err != nil {
			return nil, fmt.Errorf("static host %s: root path %s error: %v", cfg.Server, cfg.Root, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("static host %s: root path %s is not a directory", cfg.Server, cfg.Root)
		}

		// Parse MIME overrides from config (list of single-key maps to flat map)
		mimeOverrides := make(map[string]string)
		for _, override := range cfg.MimeOverrides {
			for ext, mimeType := range override {
				mimeOverrides[ext] = mimeType
			}
		}

		// Create the file server handler with MIME type wrapper
		fileServer := mimeTypeHandler(http.FileServer(http.Dir(cfg.Root)), mimeOverrides)

		// Wrap with SPA handler if enabled
		var handler http.Handler
		if cfg.SPA {
			handler = spaHandler(cfg.Root, fileServer)
		} else {
			handler = fileServer
		}

		// Set log level, defaulting to "debug" if not specified
		logLevel := cfg.LogAccessLogLevel
		if logLevel == "" {
			logLevel = "debug"
		}

		host := &hostHandler{
			server:            cfg.Server,
			root:              cfg.Root,
			spa:               cfg.SPA,
			handler:           handler,
			logAccess:         cfg.LogAccess,
			logAccessLogLevel: logLevel,
			mimeOverrides:     mimeOverrides,
		}

		// Load per-host TLS certificate if configured
		if cfg.TLSCert != "" && cfg.TLSKey != "" {
			cert, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
			if err != nil {
				return nil, fmt.Errorf("static host %s: failed to load TLS certificate: %v", cfg.Server, err)
			}
			host.cert = &cert
			logstatic.Infof("Static host configured with per-host TLS: %s -> %s (SPA: %v)", cfg.Server, cfg.Root, cfg.SPA)
		} else {
			logstatic.Infof("Static host configured: %s -> %s (SPA: %v)", cfg.Server, cfg.Root, cfg.SPA)
		}

		s.hosts[cfg.Server] = host
	}

	return s, nil
}

// Default MIME type mappings for common static file types
var defaultMimeTypes = map[string]string{
	// JavaScript and CSS
	".js":   "application/javascript",
	".mjs":  "application/javascript",
	".css":  "text/css",
	".map":  "application/json",
	".json": "application/json",

	// HTML and text
	".html": "text/html",
	".htm":  "text/html",
	".txt":  "text/plain",
	".xml":  "application/xml",

	// Bitmap image formats
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".bmp":  "image/bmp",
	".webp": "image/webp",
	".avif": "image/avif",
	".ico":  "image/x-icon",
	".tiff": "image/tiff",
	".tif":  "image/tiff",

	// Vector image formats
	".svg":  "image/svg+xml",
	".svgz": "image/svg+xml",

	// Fonts
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".otf":   "font/otf",
	".eot":   "application/vnd.ms-fontobject",

	// Other common web formats
	".pdf":  "application/pdf",
	".wasm": "application/wasm",
}

// mimeTypeHandler wraps a handler to ensure correct Content-Type headers for static file types.
// Custom overrides take precedence over defaults.
func mimeTypeHandler(next http.Handler, overrides map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ext := strings.ToLower(filepath.Ext(r.URL.Path))

		// Check custom overrides first
		if overrides != nil {
			if mimeType, ok := overrides[ext]; ok {
				w.Header().Set("Content-Type", mimeType)
				next.ServeHTTP(w, r)
				return
			}
		}

		// Fall back to defaults
		if mimeType, ok := defaultMimeTypes[ext]; ok {
			w.Header().Set("Content-Type", mimeType)
		}

		next.ServeHTTP(w, r)
	})
}

// spaHandler wraps a file server to serve index.html for paths that don't exist
func spaHandler(root string, fileServer http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Clean the path
		path := filepath.Clean(r.URL.Path)
		if path == "" || path == "." {
			path = "/"
		}

		// Check if the file exists
		fullPath := filepath.Join(root, path)
		_, err := os.Stat(fullPath)

		if os.IsNotExist(err) {
			// File doesn't exist, serve index.html instead
			indexPath := filepath.Join(root, "index.html")
			if _, indexErr := os.Stat(indexPath); indexErr == nil {
				http.ServeFile(w, r, indexPath)
				return
			}
		}

		// File exists or is a different error, let the file server handle it
		fileServer.ServeHTTP(w, r)
	})
}

// ServeIfMatch checks if the request matches a configured static host and serves the request.
// Returns true if the request was handled, false if it should be passed to the next handler.
func (s *StaticHandler) ServeIfMatch(w http.ResponseWriter, r *http.Request) bool {
	// Extract hostname without port
	hostname := r.Host
	if idx := strings.LastIndex(hostname, ":"); idx != -1 {
		hostname = hostname[:idx]
	}

	host, ok := s.hosts[hostname]
	if !ok {
		return false
	}

	if s.debugLevel > 1 {
		logstatic.Debugf("[Static] Serving %s%s from %s", hostname, r.URL.Path, host.root)
	}

	// Log access if enabled for this host
	if host.logAccess {
		host.logAccessMessage(r.URL.Path)
	}

	host.handler.ServeHTTP(w, r)
	return true
}

// logAccessMessage logs an access message at the configured log level
func (h *hostHandler) logAccessMessage(urlPath string) {
	msg := fmt.Sprintf("[Static] %s: %s", h.server, urlPath)

	switch h.logAccessLogLevel {
	case "trace":
		logstatic.Tracef(msg)
	case "log":
		logstatic.Printf(msg)
	case "info":
		logstatic.Infof(msg)
	default: // "debug" or any other value defaults to debug
		logstatic.Debugf(msg)
	}
}

// GetHostCertificates returns a map of hostnames to their per-host TLS certificates.
// Only hosts with per-host certificates are included.
func (s *StaticHandler) GetHostCertificates() map[string]*tls.Certificate {
	certs := make(map[string]*tls.Certificate)
	for hostname, host := range s.hosts {
		if host.cert != nil {
			certs[hostname] = host.cert
		}
	}
	return certs
}

// Hosts returns a list of all configured hostnames
func (s *StaticHandler) Hosts() []string {
	hosts := make([]string, 0, len(s.hosts))
	for h := range s.hosts {
		hosts = append(hosts, h)
	}
	return hosts
}
