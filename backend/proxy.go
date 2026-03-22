package backend

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/tlalocweb/hulation/log"
	"github.com/valyala/fasthttp"
)

// ProxyHandler handles reverse proxying requests to a backend container.
type ProxyHandler struct {
	backend    *BackendConfig
	hostClient *fasthttp.HostClient
}

// NewProxyHandler creates a reverse proxy handler for a backend.
func NewProxyHandler(b *BackendConfig) *ProxyHandler {
	return &ProxyHandler{
		backend: b,
		hostClient: &fasthttp.HostClient{
			Addr: b.GetResolvedAddr(),
		},
	}
}

// Handle proxies the request to the backend container with path rewriting.
//
// Path rewriting example:
//
//	VirtualPath="/api", ContainerPath="/api/v2"
//	Incoming: GET /api/users -> Backend: GET /api/v2/users
func (p *ProxyHandler) Handle(c *fiber.Ctx) error {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Rewrite path
	originalPath := c.Path()
	newPath := p.rewritePath(originalPath)

	// Build the backend request URL
	req.SetRequestURI(newPath)
	if qs := c.Request().URI().QueryString(); len(qs) > 0 {
		req.URI().SetQueryStringBytes(qs)
	}

	// Copy method
	req.Header.SetMethodBytes(c.Request().Header.Method())

	// Copy request headers
	c.Request().Header.VisitAll(func(key, value []byte) {
		// Skip hop-by-hop headers
		k := string(key)
		switch strings.ToLower(k) {
		case "connection", "keep-alive", "transfer-encoding", "te", "trailer", "upgrade":
			return
		}
		req.Header.SetBytesKV(key, value)
	})

	// Set proxy headers
	req.Header.Set("X-Forwarded-For", c.IP())
	req.Header.Set("X-Forwarded-Host", c.Hostname())
	if c.Protocol() != "" {
		req.Header.Set("X-Forwarded-Proto", c.Protocol())
	}
	req.Header.Set("X-Real-IP", c.IP())

	// Set the Host header to the backend address
	req.SetHost(p.backend.GetResolvedAddr())

	// Copy request body
	if body := c.Body(); len(body) > 0 {
		req.SetBody(body)
	}

	log.Debugf("backend proxy: %s %s -> %s%s", c.Method(), originalPath, p.backend.GetProxyTarget(), newPath)

	// Perform the request
	err := p.hostClient.Do(req, resp)
	if err != nil {
		log.Errorf("backend proxy error for %s: %s", p.backend.ContainerName, err)
		return c.Status(fiber.StatusBadGateway).SendString("Backend unavailable")
	}

	// Copy response status
	c.Status(resp.StatusCode())

	// Copy response headers
	resp.Header.VisitAll(func(key, value []byte) {
		k := string(key)
		switch strings.ToLower(k) {
		case "connection", "keep-alive", "transfer-encoding", "te", "trailer":
			return
		}
		c.Set(k, string(value))
	})

	// Copy response body
	return c.Send(resp.Body())
}

// rewritePath transforms the incoming request path.
// Strips VirtualPath prefix and prepends ContainerPath.
func (p *ProxyHandler) rewritePath(originalPath string) string {
	// Strip the virtual path prefix
	path := strings.TrimPrefix(originalPath, p.backend.VirtualPath)

	// Prepend the container path
	containerPath := p.backend.ContainerPath
	if containerPath == "" {
		containerPath = p.backend.VirtualPath
	}

	// Ensure proper path joining
	containerPath = strings.TrimSuffix(containerPath, "/")
	if path == "" || path == "/" {
		return containerPath + "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return containerPath + path
}
