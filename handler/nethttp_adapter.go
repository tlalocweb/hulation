package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/log"
	"sync"
)

// debugCFHeaders, when HULA_DEBUG_CF_HEADERS=1, makes Country() log
// what it sees on every call: the immediate RemoteAddr, whether
// that's in the loaded Cloudflare CIDR set, and the value of the
// CF-IPCountry header. Useful to diagnose "country column always
// empty behind Cloudflare" without having to dump every request.
var debugCFHeaders = os.Getenv("HULA_DEBUG_CF_HEADERS") == "1"

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
	// Trust CF-Connecting-IP only if RemoteAddr is a verified Cloudflare IP
	cfRanges := app.GetConfig().GetCloudflareIPs()
	if cfRanges != nil {
		if cfip := n.r.Header.Get("CF-Connecting-IP"); cfip != "" {
			if cfRanges.ContainsString(n.r.RemoteAddr) {
				return strings.TrimSpace(cfip)
			}
		}
	}
	// Standard proxy headers
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

func (n *NetHTTPCtx) Country() string {
	// Trust CF-IPCountry only if RemoteAddr is a verified Cloudflare
	// edge IP. Without that check a malicious client could spoof the
	// header on a direct connection.
	cfRanges := app.GetConfig().GetCloudflareIPs()
	cfRangesLoaded := cfRanges != nil
	cfTrusted := cfRangesLoaded && cfRanges.ContainsString(n.r.RemoteAddr)
	cc := strings.TrimSpace(n.r.Header.Get("CF-IPCountry"))

	if debugCFHeaders {
		// Only log on actual visitor endpoints to keep noise low.
		p := n.r.URL.Path
		if strings.HasPrefix(p, "/v/") || strings.Contains(p, "/api/v/") {
			log.Infof("country-debug path=%s remote=%s cf_ranges_loaded=%v cf_trusted=%v cf_ipcountry=%q cf_connecting_ip=%q",
				p, n.r.RemoteAddr, cfRangesLoaded, cfTrusted, cc, n.r.Header.Get("CF-Connecting-IP"))
		}
	}

	if !cfTrusted {
		return ""
	}
	// Cloudflare uses "XX" or "T1" for unknown / Tor. Treat those
	// as "no country" so downstream code can fall back to geo-IP
	// lookup instead of writing a synthetic value.
	if cc == "" || cc == "XX" || cc == "T1" {
		return ""
	}
	return cc
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

// JWTVerifier verifies a bearer token and returns the parsed permissions.
// Wired from the server package so handler can stay model-free at this
// layer; matches the signature of model.VerifyJWTClaims + a nil check
// compacted into one callback.
type JWTVerifier func(token string) (valid bool, perms interface{}, err error)

// WrapAdminForNetHTTP is WrapForNetHTTP with a mandatory admin bearer
// token check. The verified token and parsed permissions are stashed on
// the ctx via Locals("jwt") and Locals("perms"), matching the contract
// that handlers like StatusAuthOK, FormModify, and UserCreate already
// rely on.
//
// verify is expected to ensure the token is valid AND the caller holds
// the "admin" capability; return valid=false to reject. A nil verify
// rejects every request.
func WrapAdminForNetHTTP(h Handler, verify JWTVerifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		authz := r.Header.Get("Authorization")
		if len(authz) <= len(prefix) || authz[:len(prefix)] != prefix {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if verify == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		token := authz[len(prefix):]
		ok, perms, err := verify(token)
		if err != nil || !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := NewNetHTTPCtx(w, r)
		ctx.SetLocals("jwt", token)
		ctx.SetLocals("perms", perms)
		if err := h(ctx); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}
