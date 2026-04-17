package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

// NetHTTPCtx adapts http.ResponseWriter + *http.Request to the RequestCtx interface.
type NetHTTPCtx struct {
	w       http.ResponseWriter
	r       *http.Request
	code    int
	locals  map[string]interface{}
	next    Handler
	body    []byte
	bodyOnce sync.Once
	written bool
}

// NewNetHTTPCtx wraps a net/http request/response pair.
func NewNetHTTPCtx(w http.ResponseWriter, r *http.Request) *NetHTTPCtx {
	return &NetHTTPCtx{
		w:      w,
		r:      r,
		code:   200,
		locals: make(map[string]interface{}),
	}
}

func (n *NetHTTPCtx) readBody() {
	n.bodyOnce.Do(func() {
		if n.r.Body != nil {
			n.body, _ = io.ReadAll(n.r.Body)
			n.r.Body.Close()
		}
	})
}

func (n *NetHTTPCtx) Body() []byte {
	n.readBody()
	return n.body
}

func (n *NetHTTPCtx) BodyParser(out interface{}) error {
	n.readBody()
	if len(n.body) == 0 {
		return fmt.Errorf("empty body")
	}
	return json.Unmarshal(n.body, out)
}

func (n *NetHTTPCtx) Cookie(name string) string {
	c, err := n.r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

func (n *NetHTTPCtx) Header(name string) string {
	return n.r.Header.Get(name)
}

func (n *NetHTTPCtx) Query(name string) string {
	return n.r.URL.Query().Get(name)
}

func (n *NetHTTPCtx) QueryString() string {
	return n.r.URL.RawQuery
}

func (n *NetHTTPCtx) Param(name string) string {
	return n.r.PathValue(name)
}

func (n *NetHTTPCtx) IP() string {
	// Check standard proxy headers
	if xff := n.r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xri := n.r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	host, _, err := net.SplitHostPort(n.r.RemoteAddr)
	if err != nil {
		return n.r.RemoteAddr
	}
	return host
}

func (n *NetHTTPCtx) Hostname() string {
	host := n.r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

func (n *NetHTTPCtx) Method() string {
	return n.r.Method
}

func (n *NetHTTPCtx) Path() string {
	return n.r.URL.Path
}

func (n *NetHTTPCtx) OriginalURL() string {
	return n.r.RequestURI
}

func (n *NetHTTPCtx) Protocol() string {
	if n.r.TLS != nil {
		return "https"
	}
	if proto := n.r.Header.Get("X-Forwarded-Proto"); proto != "" {
		return proto
	}
	return "http"
}

func (n *NetHTTPCtx) Referer() string {
	return n.r.Referer()
}

func (n *NetHTTPCtx) VisitHeaders(fn func(key, value string)) {
	for key, values := range n.r.Header {
		for _, v := range values {
			fn(key, v)
		}
	}
}

func (n *NetHTTPCtx) Status(code int) RequestCtx {
	n.code = code
	return n
}

func (n *NetHTTPCtx) SetHeader(name, value string) {
	n.w.Header().Set(name, value)
}

func (n *NetHTTPCtx) SetCookie(cookie *Cookie) {
	hc := &http.Cookie{
		Name:     cookie.Name,
		Value:    cookie.Value,
		Domain:   cookie.Domain,
		Path:     cookie.Path,
		MaxAge:   cookie.MaxAge,
		Secure:   cookie.Secure,
		HttpOnly: cookie.HTTPOnly,
	}
	switch cookie.SameSite {
	case "Strict":
		hc.SameSite = http.SameSiteStrictMode
	case "Lax":
		hc.SameSite = http.SameSiteLaxMode
	case "None":
		hc.SameSite = http.SameSiteNoneMode
	}
	http.SetCookie(n.w, hc)
}

func (n *NetHTTPCtx) SetContentType(v string) {
	n.w.Header().Set("Content-Type", v)
}

func (n *NetHTTPCtx) writeHeader() {
	if !n.written {
		n.written = true
		n.w.WriteHeader(n.code)
	}
}

func (n *NetHTTPCtx) SendBytes(data []byte) error {
	n.writeHeader()
	_, err := n.w.Write(data)
	return err
}

func (n *NetHTTPCtx) SendString(s string) error {
	return n.SendBytes([]byte(s))
}

func (n *NetHTTPCtx) SendJSON(v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	n.w.Header().Set("Content-Type", "application/json")
	return n.SendBytes(data)
}

func (n *NetHTTPCtx) Redirect(url string, code ...int) error {
	c := http.StatusFound
	if len(code) > 0 {
		c = code[0]
	}
	http.Redirect(n.w, n.r, url, c)
	return nil
}

func (n *NetHTTPCtx) SendFile(root http.FileSystem, path string) error {
	f, err := root.Open(path)
	if err != nil {
		n.code = http.StatusNotFound
		return n.SendString("not found")
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	http.ServeContent(n.w, n.r, stat.Name(), stat.ModTime(), f.(io.ReadSeeker))
	return nil
}

func (n *NetHTTPCtx) Locals(key string) interface{} {
	return n.locals[key]
}

func (n *NetHTTPCtx) SetLocals(key string, value interface{}) {
	n.locals[key] = value
}

func (n *NetHTTPCtx) Next() error {
	if n.next != nil {
		return n.next(n)
	}
	return nil
}

func (n *NetHTTPCtx) Context() context.Context {
	return n.r.Context()
}

// SetNext sets the next handler for middleware chaining.
func (n *NetHTTPCtx) SetNext(h Handler) {
	n.next = h
}

// WrapForNetHTTP converts a unified Handler into an http.HandlerFunc.
func WrapForNetHTTP(h Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := NewNetHTTPCtx(w, r)
		if err := h(ctx); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}
