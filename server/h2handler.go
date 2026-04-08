package server

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/handler"
	"github.com/tlalocweb/hulation/log"
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
	start := time.Now()
	rec := &responseRecorder{ResponseWriter: w, status: 200}
	h.mux.ServeHTTP(rec, r)
	log.Infof("h2 %s %s %s %d %s %s", r.RemoteAddr, r.Method, r.URL.Path, rec.status, time.Since(start), r.UserAgent())
}

func (h *H2Handler) setupRoutes() {
	cfg := app.GetConfig()
	visitorPrefix := cfg.VisitorPrefix

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

	// Admin API (behind auth — OPA middleware not yet wired for h2)
	h.mux.Handle("POST /api/auth/login", handler.WrapForNetHTTP(handler.Login))
	h.mux.Handle("GET /hulastatus", handler.WrapForNetHTTP(handler.Status))
	h.mux.Handle("GET /api/status", handler.WrapForNetHTTP(handler.Status))
	h.mux.Handle("POST /api/auth/logout", handler.WrapForNetHTTP(handler.Logout))
	h.mux.Handle("POST /api/auth/user", handler.WrapForNetHTTP(handler.NewUser))
	h.mux.Handle("GET /api/auth/user/{userlookup}", handler.WrapForNetHTTP(handler.GetUser))
	h.mux.Handle("PATCH /api/auth/user/{userid}", handler.WrapForNetHTTP(handler.ModifyUser))
	h.mux.Handle("GET /api/auth/ok", handler.WrapForNetHTTP(handler.StatusAuthOK))
	h.mux.Handle("POST /api/form/create", handler.WrapForNetHTTP(handler.FormCreate))
	h.mux.Handle("DELETE /api/form/{formid}", handler.WrapForNetHTTP(handler.FormDelete))
	h.mux.Handle("PATCH /api/form/{formid}", handler.WrapForNetHTTP(handler.FormModify))
	h.mux.Handle("POST /api/lander/create", handler.WrapForNetHTTP(handler.LanderCreate))
	h.mux.Handle("DELETE /api/lander/{landerid}", handler.WrapForNetHTTP(handler.LanderDelete))
	h.mux.Handle("PATCH /api/lander/{landerid}", handler.WrapForNetHTTP(handler.LanderModify))

	log.Infof("h2: registered all unified handler routes")

	// --- Backend proxy and static file routes (per-server) ---

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

			proxyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Host != serverHost {
					http.NotFound(w, r)
					return
				}
				if containerPath != "" && containerPath != virtualPath {
					r.URL.Path = strings.Replace(r.URL.Path, virtualPath, containerPath, 1)
				}
				proxy.ServeHTTP(w, r)
			})

			h.mux.Handle(virtualPath+"/", proxyHandler)
			h.mux.Handle(virtualPath, proxyHandler)
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
