package server

// Build the Phase-4b chat Service from config + register the
// public HTTP routes (POST /api/v1/chat/start). The visitor
// WebSocket at /api/v1/chat/ws lands in stage 4b.4 alongside the
// hub.
//
// The chat admin REST surface (gRPC service registered in
// unified_boot.go) doesn't need anything in this file — its ACL
// + claims wiring lives there.

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
	chatpkg "github.com/tlalocweb/hulation/pkg/chat"
	"github.com/tlalocweb/hulation/pkg/chat/captcha"
	"github.com/tlalocweb/hulation/pkg/chat/emailverify"
	"github.com/tlalocweb/hulation/pkg/chat/moderate"
	"github.com/tlalocweb/hulation/pkg/server/unified"
)

func durationFromMS(ms int) time.Duration {
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

// chatHubSingleton is the per-process Hub the visitor + agent WS
// handlers share. Set at boot; nil before registerChatPublic
// runs. Exposed so the chat admin RPCs (LiveSessionsView) can be
// upgraded from the noop view to the real hub at the same moment.
var chatHubSingleton *chatpkg.Hub

// chatRouterSingleton is the per-process Router for next-available
// agent assignment. Same lifecycle as the Hub.
var chatRouterSingleton *chatpkg.Router

// ChatHub returns the process-shared chat hub, or nil before boot.
func ChatHub() *chatpkg.Hub { return chatHubSingleton }

// ChatRouter returns the process-shared chat router, or nil before boot.
func ChatRouter() *chatpkg.Router { return chatRouterSingleton }

// chatLiveLazy resolves the hub at every call. unified_boot wires
// the chat admin service before registerChatPublic creates the
// hub, so a captured-pointer view would always be nil; this
// thin shim re-reads the package singleton each time so the
// /chat/admin/live-sessions RPC returns real data the moment the
// hub is constructed.
type chatLiveLazy struct{}

func (chatLiveLazy) AgentsFor(id uuid.UUID) []string {
	if chatHubSingleton == nil {
		return nil
	}
	return chatHubSingleton.AgentsFor(id)
}

func (chatLiveLazy) VisitorOnline(id uuid.UUID) bool {
	if chatHubSingleton == nil {
		return false
	}
	return chatHubSingleton.VisitorOnline(id)
}

// registerChatPublic builds a chat.Service from cfg, installs it on
// the package-singleton, and registers the public HTTP handlers.
// No-op when ClickHouse isn't available — chat needs persistence
// to function.
func registerChatPublic(srv *unified.Server, cfg *config.Config) {
	db := model.GetSQLDB()
	if db == nil {
		log.Warnf("chat: ClickHouse unavailable; /api/v1/chat/start disabled")
		return
	}
	store := chatpkg.NewStore(db)
	chatHubSingleton = chatpkg.NewHub()
	chatRouterSingleton = chatpkg.NewRouter()

	cc := captchaFromConfig(cfg)
	ev := emailVerifierFromConfig(cfg)
	mod := moderatorFromConfig(cfg)

	svc := &chatpkg.Service{
		Store:              store,
		Captcha:            cc,
		Email:              ev,
		Moderate:           mod,
		RateLimit:          chatpkg.NewRateLimiter(0, 0),
		JWTKey:             cfg.JWTKey,
		TokenTTL:           chatpkg.DefaultTokenTTL,
		Router:             chatRouterSingleton,
		EmailTimeout:       emailverify.SingletonTimeout,
		ModerateTimeout:    0, // moderate.New already enforces its own timeout
		CaptchaTimeout:     0, // verifier defaults are 30s
		DisableNewSessions: cfg.Chat != nil && cfg.Chat.DisableNewSessions,
	}

	// IsServerKnown closure: hoist the configured server-id set
	// once at boot (cheap; cfg.Servers doesn't change at runtime).
	known := make(map[string]struct{}, len(cfg.Servers))
	for _, s := range cfg.Servers {
		if s != nil && s.ID != "" {
			known[s.ID] = struct{}{}
		}
	}
	isKnown := func(id string) bool { _, ok := known[id]; return ok }

	srv.RegisterCustomHandler("POST /api/v1/chat/start", chatStartHandler(svc, isKnown))

	// Visitor WebSocket — bound to the same Store + JWT key that
	// signed the chat token at /chat/start.
	visitorWS := &chatpkg.VisitorWS{
		Store:  store,
		Hub:    chatHubSingleton,
		JWTKey: cfg.JWTKey,
	}
	srv.RegisterCustomHandler("GET /api/v1/chat/ws", chatVisitorWSHandler(visitorWS))

	// Agent WebSocket — admin JWT + chat ACL. Same hub as the
	// visitor side; multiple agents per session are allowed.
	aclLookup := chatACLLookup(cfg)
	agentDeps := &agentWS{
		Store:  store,
		Hub:    chatHubSingleton,
		ACL:    aclLookup,
		Router: chatRouterSingleton,
	}
	srv.RegisterCustomHandler("GET /api/v1/chat/admin/agent-ws", chatAgentWSHandler(agentDeps))

	// Control WebSocket — agents who hold one open are in the
	// "ready" pool the router picks from for next-available
	// routing.
	ctlDeps := &agentControlWS{
		Router: chatRouterSingleton,
		ACL:    aclLookup,
		Store:  store,
	}
	srv.RegisterCustomHandler("GET /api/v1/chat/admin/agent-control-ws", chatAgentControlWSHandler(ctlDeps))

	log.Infof("chat: /api/v1/chat/{start,ws,admin/agent-ws,admin/agent-control-ws} registered (captcha=%s, openai=%v, kill_switch=%v)",
		captchaName(cc), mod != nil, svc.DisableNewSessions)

	// Quiet the "imported and not used" check on http when this
	// file shrinks in future stages (the WS handler in 4b.4 also
	// imports it).
	_ = http.StatusOK
}

func captchaName(v captcha.Verifier) string {
	if v == nil {
		return "<nil>"
	}
	return v.Name()
}

func captchaFromConfig(cfg *config.Config) captcha.Verifier {
	// Test bypass takes precedence — the env var + the config
	// flag both opt in. Production code logs a WARN if either is
	// active so operators don't accidentally ship with it on.
	if captcha.IsTestBypassEnv() ||
		(cfg.Chat != nil && cfg.Chat.Captcha != nil && cfg.Chat.Captcha.TestBypass) {
		log.Warnf("chat: captcha test bypass is ENABLED (env=%v) — do not use in production",
			captcha.IsTestBypassEnv())
		return captcha.TestBypass{}
	}
	if cfg.Chat == nil || cfg.Chat.Captcha == nil {
		// No config at all → default to Turnstile with empty key,
		// which will fail every Verify() until the operator
		// configures it. Loud failure beats silent acceptance.
		return captcha.Turnstile{}
	}
	c := cfg.Chat.Captcha
	switch c.Provider {
	case "recaptcha":
		return captcha.Recaptcha{SecretKey: c.SecretKey}
	case "turnstile", "":
		return captcha.Turnstile{SecretKey: c.SecretKey}
	default:
		log.Warnf("chat: unknown captcha provider %q — falling back to turnstile", c.Provider)
		return captcha.Turnstile{SecretKey: c.SecretKey}
	}
}

func emailVerifierFromConfig(cfg *config.Config) *emailverify.Verifier {
	opts := emailverify.DefaultOptions()
	if cfg.Chat != nil && cfg.Chat.EmailVerifier != nil {
		c := cfg.Chat.EmailVerifier
		opts.SMTPCheck = c.SMTPCheck
		if c.DisposableCheck != nil {
			opts.DisposableCheck = *c.DisposableCheck
		}
		if c.RoleCheck != nil {
			opts.RoleCheck = *c.RoleCheck
		}
		if c.MisspellCheck != nil {
			opts.MisspellCheck = *c.MisspellCheck
		}
	}
	v := emailverify.New(opts)
	emailverify.SetSingleton(v)
	return v
}

func moderatorFromConfig(cfg *config.Config) *moderate.Moderator {
	if cfg.Chat == nil || cfg.Chat.OpenAI == nil {
		return nil
	}
	c := cfg.Chat.OpenAI
	return moderate.New(moderate.Config{
		Enabled: c.Enabled,
		APIKey:  c.APIKey,
		Model:   c.Model,
		// TimeoutMS → time.Duration; moderate.New defaults to 3s
		// when zero.
		Timeout: durationFromMS(c.TimeoutMS),
		OnError: c.OnError,
	})
}
