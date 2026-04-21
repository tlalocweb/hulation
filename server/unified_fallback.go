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

	hulaapp "github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/handler"
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

	unifiedLog.Infof("Registered non-gRPC HTTP fallback routes on unified server")
}
