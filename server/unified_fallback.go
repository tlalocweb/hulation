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

	// Mobile realtime WebSocket — Phase 5a.6. Registers on the
	// ServeMux fallback because gRPC server-streaming isn't worth
	// the toolchain load for a single endpoint.
	srv.RegisterCustomHandler("/api/mobile/v1/events", http.HandlerFunc(HandleMobileWS))
	// Debug-publish endpoint for the realtime hub — lets the e2e
	// harness exercise WS propagation before the ingest-path hook
	// lands. Admin-only via the authware Claims.
	srv.RegisterCustomHandler("/api/mobile/v1/debug/publish", http.HandlerFunc(HandleMobileDebugPublish))

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
	// verifier matches handler.JWTVerifier — returns parsed permissions
	// as interface{} so the handler package stays model-free.
	verifier := func(token string) (bool, interface{}, error) {
		ok, perms, err := model.VerifyJWTClaims(model.GetDB(), token)
		if err != nil || !ok {
			return false, nil, err
		}
		if !perms.HasCap("admin") {
			return false, nil, nil
		}
		return true, perms, nil
	}
	admin := func(h handler.Handler) http.HandlerFunc {
		return handler.WrapAdminForNetHTTP(h, verifier)
	}

	// Auth. Login/logout are noauth (bootstrap); the rest require a valid
	// admin bearer token so handlers can read jwt/perms from ctx.Locals.
	srv.RegisterCustomHandler("POST /api/auth/login", handler.WrapForNetHTTP(handler.Login))
	srv.RegisterCustomHandler("POST /api/auth/logout", handler.WrapForNetHTTP(handler.Logout))
	srv.RegisterCustomHandler("GET /api/auth/ok", admin(handler.StatusAuthOK))
	srv.RegisterCustomHandler("POST /api/auth/user", admin(handler.NewUser))
	srv.RegisterCustomHandler("GET /api/auth/user/{userlookup}", admin(handler.GetUser))
	srv.RegisterCustomHandler("PATCH /api/auth/user/{userid}", admin(handler.ModifyUser))

	// Status probe paths. The /api/ namespace is admin-scoped, so unlike
	// /hulastatus (liveness, unauth) this one requires a valid admin
	// bearer token — matches what hulactl and monitoring probes expect.
	srv.RegisterCustomHandler("GET /api/status", admin(handler.Status))

	// TOTP — validate is noauth; the rest assume a valid token.
	srv.RegisterCustomHandler("POST /api/auth/totp/validate", handler.WrapForNetHTTP(handler.TotpValidate))
	srv.RegisterCustomHandler("POST /api/auth/totp/setup", admin(handler.TotpSetup))
	srv.RegisterCustomHandler("POST /api/auth/totp/verify-setup", admin(handler.TotpVerifySetup))
	srv.RegisterCustomHandler("POST /api/auth/totp/disable", admin(handler.TotpDisable))
	srv.RegisterCustomHandler("GET /api/auth/totp/status", admin(handler.TotpStatus))

	// Forms.
	srv.RegisterCustomHandler("POST /api/form/create", admin(handler.FormCreate))
	srv.RegisterCustomHandler("PATCH /api/form/{formid}", admin(handler.FormModify))
	srv.RegisterCustomHandler("DELETE /api/form/{formid}", admin(handler.FormDelete))

	// Landers.
	srv.RegisterCustomHandler("POST /api/lander/create", admin(handler.LanderCreate))
	srv.RegisterCustomHandler("PATCH /api/lander/{landerid}", admin(handler.LanderModify))
	srv.RegisterCustomHandler("DELETE /api/lander/{landerid}", admin(handler.LanderDelete))

	// Site deployment.
	srv.RegisterCustomHandler("POST /api/site/trigger-build", admin(handler.TriggerBuild))
	srv.RegisterCustomHandler("GET /api/site/build-status/{buildid}", admin(handler.BuildStatus))
	srv.RegisterCustomHandler("GET /api/site/builds/{serverid}", admin(handler.ListBuilds))

	// Staging build (WebDAV registered separately below).
	srv.RegisterCustomHandler("POST /api/staging/build", admin(handler.StagingBuild))

	// Bad actor admin — only registered when the feature is enabled.
	if badactor.IsEnabled() {
		srv.RegisterCustomHandler("GET /api/badactor/list", admin(badactor.ListBadActors))
		srv.RegisterCustomHandler("DELETE /api/badactor/block/{ip}", admin(badactor.EvictBadActor))
		srv.RegisterCustomHandler("POST /api/badactor/block", admin(badactor.ManualBlock))
		srv.RegisterCustomHandler("GET /api/badactor/allowlist", admin(badactor.ListAllowlistHandler))
		srv.RegisterCustomHandler("POST /api/badactor/allowlist", admin(badactor.AddToAllowlistHandler))
		srv.RegisterCustomHandler("DELETE /api/badactor/allowlist/{ip}", admin(badactor.RemoveFromAllowlistHandler))
		srv.RegisterCustomHandler("GET /api/badactor/stats", admin(badactor.BadActorStats))
		srv.RegisterCustomHandler("GET /api/badactor/signatures", admin(badactor.ListSignaturesHandler))
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

