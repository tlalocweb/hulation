package handler

import (
	"context"
	"net/http"
)

// Cookie represents a cookie to set on the response.
type Cookie struct {
	Name     string
	Value    string
	Domain   string
	Path     string
	MaxAge   int
	Secure   bool
	HTTPOnly bool
	SameSite string // "Strict", "Lax", "None", or ""
}

// RequestCtx is the protocol-agnostic request/response context.
// Implementations exist for Fiber (HTTP/1.1) and net/http (HTTP/2).
type RequestCtx interface {
	// --- Request reading ---
	Body() []byte
	BodyParser(out interface{}) error
	Cookie(name string) string
	Header(name string) string
	Query(name string) string
	QueryString() string
	Param(name string) string
	IP() string
	// Country returns a 2-letter ISO country code from a trusted
	// edge proxy header (e.g. CF-IPCountry from Cloudflare) when
	// the upstream connection is from a verified edge. Returns
	// "" when no trusted source is available — callers fall
	// back to async geo-IP lookup in that case.
	Country() string
	Hostname() string
	Method() string
	Path() string
	OriginalURL() string
	Protocol() string
	Referer() string

	// VisitHeaders calls fn for each request header.
	VisitHeaders(fn func(key, value string))

	// --- Response writing ---
	// Status sets the response status code. Returns self for chaining:
	//   ctx.Status(400).SendString("bad request")
	Status(code int) RequestCtx
	SetHeader(name, value string)
	SetCookie(cookie *Cookie)
	SetContentType(v string)
	SendBytes(data []byte) error
	SendString(s string) error
	SendJSON(v interface{}) error
	Redirect(url string, code ...int) error
	SendFile(root http.FileSystem, path string) error

	// --- Request-scoped state ---
	Locals(key string) interface{}
	SetLocals(key string, value interface{})
	Next() error
	Context() context.Context
}

// Handler is the unified handler function signature.
type Handler func(ctx RequestCtx) error

// Middleware wraps a Handler, returning a new Handler.
type Middleware func(next Handler) Handler

// BuildChain applies middlewares to a final handler, outermost first.
func BuildChain(handler Handler, middlewares ...Middleware) Handler {
	h := handler
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}
