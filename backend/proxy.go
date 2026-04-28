package backend

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
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
func NewProxyHandler(b *BackendConfig) *ProxyHandler {
	targetHost := b.GetResolvedAddr()
	targetURL, _ := url.Parse(fmt.Sprintf("http://%s", targetHost))
	ph := &ProxyHandler{backend: b}
	ph.rp = &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			originalPath := req.URL.Path
			newPath := ph.rewritePath(originalPath)
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
			req.URL.Path = newPath
			req.Host = targetHost
			req.Header.Set("X-Forwarded-For", clientIP(req))
			req.Header.Set("X-Forwarded-Host", req.Host)
			if req.TLS != nil {
				req.Header.Set("X-Forwarded-Proto", "https")
			} else {
				req.Header.Set("X-Forwarded-Proto", "http")
			}
			req.Header.Set("X-Real-IP", clientIP(req))
			log.Debugf("backend proxy: %s %s -> %s%s", req.Method, originalPath, b.GetProxyTarget(), newPath)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
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
		if i := strings.LastIndex(ra, ":"); i >= 0 {
			return ra[:i]
		}
		return ra
	}
	return ""
}
