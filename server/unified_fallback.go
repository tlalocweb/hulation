package server

// Registers all non-gRPC HTTP routes on a unified.Server. This is the
// ServeMux side of the "one listener to rule them all" rule — WebDAV,
// visitor tracking, scripts, /hulastatus, and backend proxies all mount
// here. Called from BootUnifiedServer after the gRPC services are
// registered.
//
// When server/run.go finishes migrating off Fiber, the calls below will
// replace the corresponding router.SetupRoutesFiber entries. For now,
// this file is the authoritative mapping of HTTP endpoints → unified
// server handlers, and can be wired up when the unified server is the
// primary listener.

import (
	"fmt"
	"net/http"

	hulaapp "github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/badactor"
	"github.com/tlalocweb/hulation/handler"
	"github.com/tlalocweb/hulation/model"
	"github.com/tlalocweb/hulation/pkg/server/unified"
)

// RegisterFallbackRoutes wires every non-gRPC HTTP endpoint onto the
// unified server's ServeMux fallback. Routes here do NOT pass through
// the grpc-gateway; they're served as plain HTTP handlers on the same
// listener.
//
// Covered:
//   - /hulastatus (unauthenticated liveness probe)
//   - /v/hello, /v/<iframe>, /v/<noscript>, /v/sub/{formid},
//     /v/<landerpath>/{landerid} (visitor tracking, browser-facing)
//   - /scripts/<hello>.js, /scripts/<forms>.js (tracking JS)
//
// WebDAV (staging-update / staging-mount / custom PATCH) and per-host
// site / backend routing are registered separately by their owning
// subsystems (handler/staging.go for WebDAV; backend.Manager for
// backends) — they require host-level dispatch that lives outside the
// path-only ServeMux.
func RegisterFallbackRoutes(srv *unified.Server) {
	if srv == nil {
		return
	}
	cfg := hulaapp.GetConfig()
	if cfg == nil {
		return
	}

	visitorPrefix := cfg.VisitorPrefix
	if visitorPrefix == "" {
		visitorPrefix = "/v"
	}

	// /hulastatus — unauthenticated liveness probe.
	srv.RegisterCustomHandler("/hulastatus", handler.WrapForNetHTTP(handler.Status))

	// Visitor tracking endpoints.
	srv.RegisterCustomHandler(visitorPrefix+"/hello", handler.WrapForNetHTTP(handler.Hello))

	if name := cfg.PublishedIFrameHelloFileName; name != "" {
		srv.RegisterCustomHandler(visitorPrefix+"/"+name, handler.WrapForNetHTTP(handler.HelloIframe))
	}
	if name := cfg.PublishedIFrameNoScriptFilename; name != "" {
		srv.RegisterCustomHandler(visitorPrefix+"/"+name, handler.WrapForNetHTTP(handler.HelloNoScript))
	}

	// Form submission — uses Go 1.22+ pattern syntax for the formid
	// path parameter. The param is resolved inside the handler via
	// NetHTTPCtx.Param("formid").
	srv.RegisterCustomHandler(fmt.Sprintf("POST %s/sub/{formid}", visitorPrefix),
		handler.WrapForNetHTTP(handler.FormSubmit))

	// Lander redirect.
	if landerPath := cfg.LanderPath; landerPath != "" {
		srv.RegisterCustomHandler(fmt.Sprintf("GET %s%s/{landerid}", visitorPrefix, landerPath),
			handler.WrapForNetHTTP(handler.DoLanding))
	}

	// Tracking scripts.
	if name := cfg.PublishedHelloScriptFilename; name != "" {
		srv.RegisterCustomHandler("/scripts/"+name, handler.WrapForNetHTTP(handler.HelloScriptFile))
	}
	if name := cfg.PublishedFormsScriptFilename; name != "" {
		srv.RegisterCustomHandler("/scripts/"+name, handler.WrapForNetHTTP(handler.FormsScriptFile))
	}

	// --- Legacy /api/* routes (TRANSITIONAL) ---
	//
	// These mirror the old router.SetupRoutesFiber admin routes so
	// hulactl builds predating the gRPC migration (Stage 0.8) keep
	// working against the unified server. Each route registered here
	// is also available via gRPC or the REST gateway at /api/v1/*.
	// Once Stage 0.8 ships and the e2e harness confirms hulactl uses
	// /api/v1, this block can be deleted.
	registerLegacyAPIRoutes(srv)

	// WebDAV staging endpoints. Supports PUT, PATCH with X-Update-Range,
	// PATCH with X-Patch-Format: diff, and the rest of the emersion
	// WebDAV verbs. Authentication is handled inside the handler via
	// the supplied verifyToken callback (Bearer-token → JWT validation).
	//
	// Path: /api/staging/{serverID}/dav/<file-path>
	//
	// ServeMux pattern with /{serverid}/dav/ requires Go 1.22+ enhanced
	// routing — covered by hula's go.mod (go 1.25).
	webdavHandler := handler.StagingWebDAVNetHTTP(verifyJWTAdmin)
	srv.RegisterCustomHandler("/api/staging/{serverid}/dav/", func(w http.ResponseWriter, r *http.Request) {
		webdavHandler.ServeHTTP(w, r)
	})
	srv.RegisterCustomHandler("/api/staging/{serverid}/dav", func(w http.ResponseWriter, r *http.Request) {
		webdavHandler.ServeHTTP(w, r)
	})

	unifiedLog.Infof("Registered non-gRPC HTTP fallback routes on unified server")
}

// registerLegacyAPIRoutes mounts the pre-gRPC /api/* admin routes on
// the unified server's ServeMux. Lift-and-shift from the deleted
// router/router.go — same handlers, same paths, no Fiber. Transitional
// only; removed once hulactl and the e2e harness move to /api/v1/*.
func registerLegacyAPIRoutes(srv *unified.Server) {
	// Auth (login is the critical one — hulactl needs it to bootstrap).
	srv.RegisterCustomHandler("POST /api/auth/login", handler.WrapForNetHTTP(handler.Login))
	srv.RegisterCustomHandler("POST /api/auth/logout", handler.WrapForNetHTTP(handler.Logout))
	srv.RegisterCustomHandler("GET /api/auth/ok", handler.WrapForNetHTTP(handler.StatusAuthOK))
	srv.RegisterCustomHandler("POST /api/auth/user", handler.WrapForNetHTTP(handler.NewUser))
	srv.RegisterCustomHandler("GET /api/auth/user/{userlookup}", handler.WrapForNetHTTP(handler.GetUser))
	srv.RegisterCustomHandler("PATCH /api/auth/user/{userid}", handler.WrapForNetHTTP(handler.ModifyUser))

	// Status probe paths. The /api/ namespace is admin-scoped, so unlike
	// /hulastatus (liveness, unauth) this one requires a valid admin
	// bearer token — matches what hulactl and monitoring probes expect.
	srv.RegisterCustomHandler("GET /api/status", adminOnly(handler.WrapForNetHTTP(handler.Status)))

	// TOTP — validate is noauth; the rest assume a valid token.
	srv.RegisterCustomHandler("POST /api/auth/totp/validate", handler.WrapForNetHTTP(handler.TotpValidate))
	srv.RegisterCustomHandler("POST /api/auth/totp/setup", handler.WrapForNetHTTP(handler.TotpSetup))
	srv.RegisterCustomHandler("POST /api/auth/totp/verify-setup", handler.WrapForNetHTTP(handler.TotpVerifySetup))
	srv.RegisterCustomHandler("POST /api/auth/totp/disable", handler.WrapForNetHTTP(handler.TotpDisable))
	srv.RegisterCustomHandler("GET /api/auth/totp/status", handler.WrapForNetHTTP(handler.TotpStatus))

	// Forms.
	srv.RegisterCustomHandler("POST /api/form/create", handler.WrapForNetHTTP(handler.FormCreate))
	srv.RegisterCustomHandler("PATCH /api/form/{formid}", handler.WrapForNetHTTP(handler.FormModify))
	srv.RegisterCustomHandler("DELETE /api/form/{formid}", handler.WrapForNetHTTP(handler.FormDelete))

	// Landers.
	srv.RegisterCustomHandler("POST /api/lander/create", handler.WrapForNetHTTP(handler.LanderCreate))
	srv.RegisterCustomHandler("PATCH /api/lander/{landerid}", handler.WrapForNetHTTP(handler.LanderModify))
	srv.RegisterCustomHandler("DELETE /api/lander/{landerid}", handler.WrapForNetHTTP(handler.LanderDelete))

	// Site deployment.
	srv.RegisterCustomHandler("POST /api/site/trigger-build", handler.WrapForNetHTTP(handler.TriggerBuild))
	srv.RegisterCustomHandler("GET /api/site/build-status/{buildid}", handler.WrapForNetHTTP(handler.BuildStatus))
	srv.RegisterCustomHandler("GET /api/site/builds/{serverid}", handler.WrapForNetHTTP(handler.ListBuilds))

	// Staging build (WebDAV registered separately below).
	srv.RegisterCustomHandler("POST /api/staging/build", handler.WrapForNetHTTP(handler.StagingBuild))

	// Bad actor admin — only registered when the feature is enabled.
	if badactor.IsEnabled() {
		srv.RegisterCustomHandler("GET /api/badactor/list", handler.WrapForNetHTTP(badactor.ListBadActors))
		srv.RegisterCustomHandler("DELETE /api/badactor/block/{ip}", handler.WrapForNetHTTP(badactor.EvictBadActor))
		srv.RegisterCustomHandler("POST /api/badactor/block", handler.WrapForNetHTTP(badactor.ManualBlock))
		srv.RegisterCustomHandler("GET /api/badactor/allowlist", handler.WrapForNetHTTP(badactor.ListAllowlistHandler))
		srv.RegisterCustomHandler("POST /api/badactor/allowlist", handler.WrapForNetHTTP(badactor.AddToAllowlistHandler))
		srv.RegisterCustomHandler("DELETE /api/badactor/allowlist/{ip}", handler.WrapForNetHTTP(badactor.RemoveFromAllowlistHandler))
		srv.RegisterCustomHandler("GET /api/badactor/stats", handler.WrapForNetHTTP(badactor.BadActorStats))
		srv.RegisterCustomHandler("GET /api/badactor/signatures", handler.WrapForNetHTTP(badactor.ListSignaturesHandler))
	}
}

// verifyJWTAdmin is the token verifier passed to the WebDAV handler.
// Returns (valid, isAdmin, error). Matches the signature
// handler.StagingWebDAVNetHTTP expects.
func verifyJWTAdmin(token string) (valid bool, isAdmin bool, err error) {
	ok, perms, verr := model.VerifyJWTClaims(model.GetDB(), token)
	if verr != nil || !ok {
		return false, false, verr
	}
	return true, perms.HasCap("admin"), nil
}

// adminOnly rejects the request with 401 unless the Authorization header
// carries a valid admin bearer token. Used to gate transitional legacy
// /api/* routes that mirror legacy Fiber's admin group.
func adminOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authz := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if len(authz) <= len(prefix) || authz[:len(prefix)] != prefix {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		valid, admin, verr := verifyJWTAdmin(authz[len(prefix):])
		if verr != nil || !valid || !admin {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}
