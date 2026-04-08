package server

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
)

// H2Handler handles HTTP/2 requests using Go's net/http.
// It serves static files and proxies backend requests for each configured server.
type H2Handler struct {
	listener *config.Listener
	mux      *http.ServeMux
}

// NewH2Handler creates a new HTTP/2 handler for the given listener.
func NewH2Handler(l *config.Listener) *H2Handler {
	h := &H2Handler{
		listener: l,
		mux:      http.NewServeMux(),
	}
	h.setupRoutes()
	return h
}

func (h *H2Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func (h *H2Handler) setupRoutes() {
	for _, server := range h.listener.GetServers() {
		// Register backend proxy routes
		for _, b := range server.Backends {
			if !b.IsReady() {
				continue
			}
			target, err := url.Parse(b.GetProxyTarget())
			if err != nil {
				log.Errorf("h2: bad proxy target for %s: %s", b.ContainerName, err)
				continue
			}
			proxy := httputil.NewSingleHostReverseProxy(target)
			virtualPath := b.VirtualPath
			containerPath := b.ContainerPath
			serverHost := server.Host

			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Host check: only proxy if the request matches this server
				if r.Host != serverHost {
					http.NotFound(w, r)
					return
				}
				// Rewrite path: strip virtualPath, prepend containerPath
				if containerPath != "" && containerPath != virtualPath {
					r.URL.Path = strings.Replace(r.URL.Path, virtualPath, containerPath, 1)
				}
				proxy.ServeHTTP(w, r)
			})

			h.mux.Handle(virtualPath+"/", handler)
			h.mux.Handle(virtualPath, handler)
			log.Infof("h2: registered proxy %s -> %s (container: %s)", virtualPath, target, b.ContainerName)
		}

		// Serve static files if configured
		if server.Root != "" {
			serverHost := server.Host
			fileServer := http.FileServer(http.Dir(server.Root))
			h.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				if r.Host != serverHost {
					http.NotFound(w, r)
					return
				}
				fileServer.ServeHTTP(w, r)
			})
			log.Infof("h2: serving static files from %s for %s", server.Root, serverHost)
		}
	}
}
