package server

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/badactor"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/handler"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
)

// H2Handler handles HTTP/2 requests using Go's net/http.
// It serves static files, proxies backend requests, and handles
// visitor tracking via the unified handler layer.
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

// responseRecorder captures the status code for logging.
type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (rr *responseRecorder) WriteHeader(code int) {
	rr.status = code
	rr.ResponseWriter.WriteHeader(code)
}

func (h *H2Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	clientIP := extractIPFromRequest(r)
	// Bad actor check — uses real client IP (CF-Connecting-IP when behind Cloudflare)
	if badactor.IsEnabled() {
		if block, _ := badactor.GetStore().CheckAndBlock(clientIP, r.UserAgent(), r.Method, r.URL.Path, r.URL.RawQuery, r.Host); block {
			panic(http.ErrAbortHandler) // abort silently
		}
	}
	// Redirect alias check — before any routing
	ctx := handler.NewNetHTTPCtx(w, r)
	if redirected, _ := handler.CheckRedirectAlias(ctx); redirected {
		return
	}
	start := time.Now()
	rec := &responseRecorder{ResponseWriter: w, status: 200}
	h.mux.ServeHTTP(rec, r)
	serverID := ""
	if srv := h.listener.GetServer(r.Host); srv != nil {
		serverID = srv.ID
	}
	if log.IsVerbose() && clientIP != r.RemoteAddr {
		log.Infof("h2 [%s] %s (%s) %s %s %d %s %s", serverID, clientIP, r.RemoteAddr, r.Method, r.URL.Path, rec.status, time.Since(start), r.UserAgent())
	} else {
		log.Infof("h2 [%s] %s %s %s %d %s %s", serverID, clientIP, r.Method, r.URL.Path, rec.status, time.Since(start), r.UserAgent())
	}
}

func extractIPFromRequest(r *http.Request) string {
	// Trust CF-Connecting-IP only if RemoteAddr is a verified Cloudflare IP
	cfRanges := app.GetConfig().GetCloudflareIPs()
	if cfRanges != nil {
		if cfip := r.Header.Get("CF-Connecting-IP"); cfip != "" {
			if cfRanges.ContainsString(r.RemoteAddr) {
				return strings.TrimSpace(cfip)
			}
		}
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

const opaModule = `
package hulation.authz

import rego.v1

default allow := false

allow if {
	some cap in input.attrs
	cap == "admin"
}
`

func newH2OpaMiddleware() handler.Middleware {
	return handler.NewOpaMiddleware(handler.OpaConfig{
		RegoQuery:             "data.hulation.authz.allow",
		RegoPolicy:            bytes.NewBufferString(opaModule),
		IncludeQueryString:    true,
		DeniedStatusCode:      http.StatusForbidden,
		DeniedResponseMessage: "status forbidden",
		IncludeHeaders:        []string{"Authorization"},
		InputCreationMethod: func(ctx handler.RequestCtx) (map[string]interface{}, int, string, error) {
			ahdr := ctx.Header("Authorization")
			var token string
			n, err := fmt.Sscanf(ahdr, "Bearer %s", &token)
			if err != nil {
				return nil, http.StatusUnauthorized, "error parsing token", fmt.Errorf("error parsing token: %w", err)
			}
			if n < 1 {
				return nil, http.StatusUnauthorized, "no token", fmt.Errorf("no token")
			}
			ok, perms, err := model.VerifyJWTClaims(model.GetDB(), token)
			if err != nil {
				return nil, http.StatusUnauthorized, "error verifying token", fmt.Errorf("error verifying token: %w", err)
			}
			if !ok {
				return nil, http.StatusUnauthorized, "token not valid", fmt.Errorf("token not valid")
			}
			ctx.SetLocals("jwt", token)
			ctx.SetLocals("perms", perms)
			return map[string]interface{}{
				"method":   ctx.Method(),
				"path":     ctx.Path(),
				"jwt":      token,
				"jwtkey":   app.GetConfig().JWTKey,
				"rootname": app.GetConfig().Admin.Username,
				"userid":   perms.UserID,
				"attrs":    perms.ListCaps(),
				"ip":       ctx.IP(),
			}, 0, "", nil
		},
	})
}

func (h *H2Handler) setupRoutes() {
	cfg := app.GetConfig()
	visitorPrefix := cfg.VisitorPrefix

	// OPA middleware for admin API protection on h2
	opa := newH2OpaMiddleware()
	wrapOpa := func(h handler.Handler) http.HandlerFunc {
		return handler.WrapForNetHTTP(opa(h))
	}

	// --- Unified handler routes (visitor tracking, scripts) ---

	// Visitor: POST /v/hello
	h.mux.Handle("POST "+visitorPrefix+"/hello", handler.WrapForNetHTTP(handler.Hello))

	// Visitor: GET /v/{iframehello}
	h.mux.Handle("GET "+visitorPrefix+"/"+cfg.PublishedIFrameHelloFileName, handler.WrapForNetHTTP(handler.HelloIframe))

	// Visitor: GET /v/{noscript}
	h.mux.Handle("GET "+visitorPrefix+"/"+cfg.PublishedIFrameNoScriptFilename, handler.WrapForNetHTTP(handler.HelloNoScript))

	// Scripts
	h.mux.Handle("GET /scripts/"+cfg.PublishedHelloScriptFilename, handler.WrapForNetHTTP(handler.HelloScriptFile))
	h.mux.Handle("GET /scripts/"+cfg.PublishedFormsScriptFilename, handler.WrapForNetHTTP(handler.FormsScriptFile))

	// Form submission: POST /v/sub/{formid}
	h.mux.Handle("POST "+visitorPrefix+"/sub/{formid}", handler.WrapForNetHTTP(handler.FormSubmit))

	// Landing pages
	landerPath := cfg.LanderPath
	h.mux.Handle("GET "+visitorPrefix+landerPath+"/{landerid}", handler.WrapForNetHTTP(handler.DoLanding))

	// Admin API — login is not OPA-protected, everything else is
	h.mux.Handle("POST /api/auth/login", handler.WrapForNetHTTP(handler.Login))
	h.mux.Handle("GET /hulastatus", handler.WrapForNetHTTP(handler.Status))
	h.mux.Handle("GET /api/status", wrapOpa(handler.Status))
	h.mux.Handle("POST /api/auth/logout", wrapOpa(handler.Logout))
	h.mux.Handle("POST /api/auth/user", wrapOpa(handler.NewUser))
	h.mux.Handle("GET /api/auth/user/{userlookup}", wrapOpa(handler.GetUser))
	h.mux.Handle("PATCH /api/auth/user/{userid}", wrapOpa(handler.ModifyUser))
	h.mux.Handle("GET /api/auth/ok", wrapOpa(handler.StatusAuthOK))
	h.mux.Handle("POST /api/form/create", wrapOpa(handler.FormCreate))
	h.mux.Handle("DELETE /api/form/{formid}", wrapOpa(handler.FormDelete))
	h.mux.Handle("PATCH /api/form/{formid}", wrapOpa(handler.FormModify))
	h.mux.Handle("POST /api/lander/create", wrapOpa(handler.LanderCreate))
	h.mux.Handle("DELETE /api/lander/{landerid}", wrapOpa(handler.LanderDelete))
	h.mux.Handle("PATCH /api/lander/{landerid}", wrapOpa(handler.LanderModify))

	// TOTP - validate does NOT require OPA (uses totp_pending token)
	h.mux.Handle("POST /api/auth/totp/validate", handler.WrapForNetHTTP(handler.TotpValidate))
	h.mux.Handle("POST /api/auth/totp/setup", wrapOpa(handler.TotpSetup))
	h.mux.Handle("POST /api/auth/totp/verify-setup", wrapOpa(handler.TotpVerifySetup))
	h.mux.Handle("POST /api/auth/totp/disable", wrapOpa(handler.TotpDisable))
	h.mux.Handle("GET /api/auth/totp/status", wrapOpa(handler.TotpStatus))

	// Site deployment
	h.mux.Handle("POST /api/site/trigger-build", wrapOpa(handler.TriggerBuild))
	h.mux.Handle("GET /api/site/build-status/{buildid}", wrapOpa(handler.BuildStatus))
	h.mux.Handle("GET /api/site/builds/{serverid}", wrapOpa(handler.ListBuilds))

	// Bad actor admin API
	if badactor.IsEnabled() {
		h.mux.Handle("GET /api/badactor/list", wrapOpa(badactor.ListBadActors))
		h.mux.Handle("DELETE /api/badactor/block/{ip}", wrapOpa(badactor.EvictBadActor))
		h.mux.Handle("POST /api/badactor/block", wrapOpa(badactor.ManualBlock))
		h.mux.Handle("GET /api/badactor/allowlist", wrapOpa(badactor.ListAllowlistHandler))
		h.mux.Handle("POST /api/badactor/allowlist", wrapOpa(badactor.AddToAllowlistHandler))
		h.mux.Handle("DELETE /api/badactor/allowlist/{ip}", wrapOpa(badactor.RemoveFromAllowlistHandler))
		h.mux.Handle("GET /api/badactor/stats", wrapOpa(badactor.BadActorStats))
		h.mux.Handle("GET /api/badactor/signatures", wrapOpa(badactor.ListSignaturesHandler))
	}

	log.Infof("h2: registered all unified handler routes")

	// --- Backend proxy and static file routes (per-server) ---
	// Collect handlers by pattern to avoid duplicate ServeMux registration
	// when multiple virtual hosts share the same path (e.g., both have /api).
	type hostHandler struct {
		host    string
		handler http.Handler
	}
	patternHandlers := make(map[string][]hostHandler) // pattern -> handlers by host

	for _, server := range h.listener.GetServers() {
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

			proxyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if containerPath != "" && containerPath != virtualPath {
					r.URL.Path = strings.Replace(r.URL.Path, virtualPath, containerPath, 1)
				}
				proxy.ServeHTTP(w, r)
			})

			patternHandlers[virtualPath+"/"] = append(patternHandlers[virtualPath+"/"], hostHandler{serverHost, proxyHandler})
			patternHandlers[virtualPath] = append(patternHandlers[virtualPath], hostHandler{serverHost, proxyHandler})
			log.Infof("h2: registered proxy %s -> %s (container: %s) for %s", virtualPath, target, b.ContainerName, serverHost)
		}

		if server.Root != "" {
			serverHost := server.Host
			fileServer := http.FileServer(http.Dir(server.Root))
			patternHandlers["/"] = append(patternHandlers["/"], hostHandler{serverHost, fileServer})
			log.Infof("h2: serving static files from %s for %s", server.Root, serverHost)
		}
	}

	// Register a single dispatching handler per pattern
	for pattern, handlers := range patternHandlers {
		handlers := handlers // capture for closure
		h.mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, hh := range handlers {
				if r.Host == hh.host {
					hh.handler.ServeHTTP(w, r)
					return
				}
			}
			http.NotFound(w, r)
		}))
	}
}
