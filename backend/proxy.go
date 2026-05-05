package backend

import (
	"net"
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/tlalocweb/hulation/log"
)

// ProxyHandler reverse-proxies requests to a backend container.
// Net/http implementation via httputil.ReverseProxy.
type ProxyHandler struct {
	backend *BackendConfig
	rp      *httputil.ReverseProxy
}

// NewProxyHandler creates a reverse proxy handler for a backend.
//
// The Director re-reads b.GetResolvedAddr() on every request rather
// than capturing it at construction time. This lets us register the
// proxy route eagerly at boot — before the backend container has
// started and SetResolvedAddr has run — and have it start working
// the moment the backend manager finishes startup. Until then, an
// unresolved address makes the request fail in the ErrorHandler with
// a clean 502.
func NewProxyHandler(b *BackendConfig) *ProxyHandler {
	ph := &ProxyHandler{backend: b}
	ph.rp = &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			originalPath := req.URL.Path
			originalHost := req.Host
			newPath := ph.rewritePath(originalPath)
			targetHost := b.GetResolvedAddr()
			req.URL.Scheme = "http"
			req.URL.Host = targetHost
			req.URL.Path = newPath
			req.Host = targetHost
			req.Header.Set("X-Forwarded-For", clientIP(req))
			req.Header.Set("X-Forwarded-Host", originalHost)
			if req.TLS != nil {
				req.Header.Set("X-Forwarded-Proto", "https")
			} else {
				req.Header.Set("X-Forwarded-Proto", "http")
			}
			req.Header.Set("X-Real-IP", clientIP(req))
			log.Debugf("backend proxy: %s %s -> http://%s%s", req.Method, originalPath, targetHost, newPath)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if b.GetResolvedAddr() == "" {
				log.Warnf("backend proxy %s: backend not ready yet (addr unresolved)", b.ContainerName)
				http.Error(w, "Backend starting up — try again in a few seconds", http.StatusBadGateway)
				return
			}
			log.Errorf("backend proxy error for %s: %s", b.ContainerName, err)
			http.Error(w, "Backend unavailable", http.StatusBadGateway)
		},
	}
	return ph
}

// ServeHTTP makes ProxyHandler implement http.Handler.
func (p *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.rp.ServeHTTP(w, r)
}

// rewritePath strips the VirtualPath prefix and prepends ContainerPath.
func (p *ProxyHandler) rewritePath(originalPath string) string {
	path := strings.TrimPrefix(originalPath, p.backend.VirtualPath)
	containerPath := p.backend.ContainerPath
	if containerPath == "" {
		containerPath = p.backend.VirtualPath
	}
	containerPath = strings.TrimSuffix(containerPath, "/")
	if path == "" || path == "/" {
		return containerPath + "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return containerPath + path
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.Index(xff, ","); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if ra := r.RemoteAddr; ra != "" {
		if host, _, err := net.SplitHostPort(ra); err == nil {
			return host
		}
		return ra
	}
	return ""
}
