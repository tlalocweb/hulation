package server

// Unified server construction. Phase 0 stage 0.6 lands the infrastructure;
// actual switch-over from the Fiber listener happens as handlers are ported
// in stage 0.7. Each ported endpoint registers itself on the returned
// unified.Server; non-gRPC endpoints (WebDAV, visitor tracking, static
// site serving, /hulastatus) register custom handlers on the fallback
// ServeMux.

import (
	"context"
	"crypto/tls"
	"fmt"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"golang.org/x/crypto/acme/autocert"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"

	hulaapp "github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/handler"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
	alertsimpl "github.com/tlalocweb/hulation/pkg/api/v1/alerts"
	analyticsimpl "github.com/tlalocweb/hulation/pkg/api/v1/analytics"
	authimpl "github.com/tlalocweb/hulation/pkg/api/v1/auth"
	badactorimpl "github.com/tlalocweb/hulation/pkg/api/v1/badactor"
	chatimpl "github.com/tlalocweb/hulation/pkg/api/v1/chat"
	formsimpl "github.com/tlalocweb/hulation/pkg/api/v1/forms"
	goalsimpl "github.com/tlalocweb/hulation/pkg/api/v1/goals"
	landersimpl "github.com/tlalocweb/hulation/pkg/api/v1/landers"
	mobileimpl "github.com/tlalocweb/hulation/pkg/api/v1/mobile"
	notifyimpl "github.com/tlalocweb/hulation/pkg/api/v1/notify"
	reportsimpl "github.com/tlalocweb/hulation/pkg/api/v1/reports"
	siteimpl "github.com/tlalocweb/hulation/pkg/api/v1/site"
	stagingimpl "github.com/tlalocweb/hulation/pkg/api/v1/staging"
	statusimpl "github.com/tlalocweb/hulation/pkg/api/v1/status"
	alertsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/alerts"
	analyticsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/analytics"
	authspec "github.com/tlalocweb/hulation/pkg/apispec/v1/auth"
	badactorspec "github.com/tlalocweb/hulation/pkg/apispec/v1/badactor"
	chatspec "github.com/tlalocweb/hulation/pkg/apispec/v1/chat"
	formsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/forms"
	goalsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/goals"
	landersspec "github.com/tlalocweb/hulation/pkg/apispec/v1/landers"
	mobilespec "github.com/tlalocweb/hulation/pkg/apispec/v1/mobile"
	notifyspec "github.com/tlalocweb/hulation/pkg/apispec/v1/notify"
	reportsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/reports"
	sitespec "github.com/tlalocweb/hulation/pkg/apispec/v1/site"
	stagingspec "github.com/tlalocweb/hulation/pkg/apispec/v1/staging"
	statusspec "github.com/tlalocweb/hulation/pkg/apispec/v1/status"
	chatpkg "github.com/tlalocweb/hulation/pkg/chat"
	"github.com/tlalocweb/hulation/pkg/server/authware"
	authprovider "github.com/tlalocweb/hulation/pkg/server/authware/provider"
	baseprovider "github.com/tlalocweb/hulation/pkg/server/authware/provider/base"
	"github.com/tlalocweb/hulation/pkg/server/unified"
	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"
	"github.com/tlalocweb/hulation/pkg/store/storage"
	"github.com/tlalocweb/hulation/utils"

	"gopkg.in/yaml.v3"
)

var unifiedLog = log.GetTaggedLogger("unified-boot", "Unified server bootstrap")

// BootUnifiedServer constructs the unified HTTPS server with every Phase 0
// gRPC service implementation registered. The caller is responsible for
// Start()/Stop() and for registering the Fiber-fallback handlers that the
// non-migrating endpoints (WebDAV, visitor, scripts, /hulastatus, per-host
// site serving) still use.
//
// Returns the unified Server, the grpc-gateway ServeMux (for REST-route
// registration during stage 0.7), and an error.
func BootUnifiedServer(ctx context.Context, cfg *config.Config) (srv *unified.Server, err error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}

	// Resolve TLS material. Hula supports three modes on the unified
	// listener:
	//   1. Static cert+key files via cfg.HulaSSL.Cert / .Key.
	//   2. ACME auto-issuance via cfg.HulaSSL.ACME.
	//   3. Both (static as fallback, ACME for covered hostnames).
	// At least one must be configured or the boot fails.
	var getCert func(*tls.ClientHelloInfo) (*tls.Certificate, error)
	// acmeTLSALPN is set true only on the ACME path so the unified server
	// advertises "acme-tls/1" and routes TLS-ALPN-01 challenges to the
	// autocert manager. Static-cert and self-signed paths leave it false.
	var acmeTLSALPN bool

	// After config.Load*, HulaSSL.Cert/Key hold PEM CONTENT (not paths),
	// so we can't pass them to unified.NewServer as TLSCertFile/Key —
	// those fields feed tls.LoadX509KeyPair which opens the string as a
	// filename. Go via GetTLSCert (already parsed) and expose it through
	// the caller-supplied GetCertificate path instead.
	var staticCert *tls.Certificate
	if cfg.HulaSSL != nil {
		staticCert = cfg.HulaSSL.GetTLSCert()
		// conftagz eagerly materializes HulaSSL.ACME with defaults, so
		// the struct being non-nil doesn't mean ACME was actually
		// requested. Only wire the autocert manager when the user
		// supplied at least one domain — nothing else makes sense.
		if cfg.HulaSSL.ACME != nil && len(cfg.HulaSSL.ACME.Domains) > 0 {
			acfg := cfg.HulaSSL.ACME
			mgr := &autocert.Manager{
				Prompt:     autocert.AcceptTOS,
				Cache:      autocert.DirCache(acfg.CacheDir),
				Email:      acfg.Email,
				HostPolicy: autocert.HostWhitelist(acfg.Domains...),
			}
			getCert = mgr.GetCertificate
			acmeTLSALPN = true
			unifiedLog.Infof("ACME enabled: cache=%q email=%q domains=%v", acfg.CacheDir, acfg.Email, acfg.Domains)
		}
	}
	if staticCert != nil && getCert == nil {
		// Promote the pre-loaded static cert into the dynamic-selector
		// slot. Per-host certs attached via AddHostCertificate below
		// still win SNI before this fallback runs.
		getCert = func(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
			return staticCert, nil
		}
	}
	if staticCert == nil && getCert == nil && cfg.HulaSSL != nil && cfg.HulaSSL.DevCA != nil && cfg.HulaSSL.DevCA.Enabled {
		// Opt-in local dev CA (Caddy "tls internal" style). Instead of a
		// bare self-signed cert no client trusts, cache a local root CA
		// and sign a per-host leaf on demand — every leaf chains to ONE
		// root the developer can trust. This getter occupies the SAME
		// catch-all slot the self-signed cert used, so per-host static /
		// Cloudflare Origin CA certs attached via AddHostCertificate below
		// still win SNI before this fallback runs. No ACME here, so
		// acmeTLSALPN stays false.
		devCA, dcerr := unified.NewDevCA(cfg.HulaSSL.DevCADir())
		if dcerr != nil {
			return nil, fmt.Errorf("init dev CA: %w", dcerr)
		}
		getCert = devCA.GetCertificate
		// Emit the root path + trust instructions by DEFAULT so the
		// operator can remove browser warnings. Auto-install is a separate
		// opt-in below.
		unifiedLog.Infof("dev CA enabled: local root at %s — trust this root to remove browser warnings", devCA.RootPath())
		unifiedLog.Infof("dev CA trust command: %s", devCA.TrustInstructions())
		if cfg.HulaSSL.DevCAInstallTrust() {
			// OPT-IN: install the root into the OS trust store. InstallTrust
			// logs a manual-install hint + returns the error on failure; we
			// warn and continue (a failed trust install must not abort boot —
			// handshakes still succeed, browsers just warn).
			if ierr := devCA.InstallTrust(); ierr != nil {
				unifiedLog.Warnf("dev CA install_trust requested but failed: %v", ierr)
			} else {
				unifiedLog.Infof("dev CA root installed into the OS trust store")
			}
		} else {
			unifiedLog.Infof("dev CA install_trust is off (opt-in) — the root was NOT added to the OS trust store; run the command above to trust it")
		}
	}
	if staticCert == nil && getCert == nil {
		// Fallback: no static cert AND no ACME manager AND no dev CA.
		// Generate an in-memory self-signed cert for the admin listener.
		// Matches the legacy Fiber behaviour and keeps local / dev / test
		// harnesses working without explicit cert config. Production
		// deployments should configure hula_ssl.cert/key or ACME.
		unifiedLog.Warnf("hula_ssl not configured; generating self-signed certificate for admin listener")
		hosts := []string{"localhost", "127.0.0.1", "::1"}
		if cfg.HulaHost != "" {
			hosts = append(hosts, cfg.HulaHost)
		}
		hosts = append(hosts, cfg.HulaAliases...)
		selfCert, scerr := GenerateSelfSignedCert(hosts)
		if scerr != nil {
			return nil, fmt.Errorf("generate self-signed cert: %w", scerr)
		}
		getCert = func(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
			return selfCert, nil
		}
	}

	addr := fmt.Sprintf(":%d", cfg.Port)
	if cfg.ListenOn != "" {
		addr = cfg.ListenOn
	}

	// hulaagent Phase 3: trust the Agent CA on the public listener so
	// agents that present an Agent-CA-signed leaf complete the TLS
	// handshake. The Phase-3 mTLS middleware (attached below) walks
	// the request-time half — fingerprint lookup against the registry,
	// revoke/expire checks. nil ClientCAs is fine (boot will skip the
	// middleware too) when the Agent CA hasn't loaded yet.
	clientCAs := agentClientCAPool()

	// Device-key store powers Hula-Device-* signature auth (QR-paired mobile devices).
	// The QR-pairing endpoints (/api/v1/pair/{issue,redeem}, see pair_handlers.go)
	// write into this store when a marketer redeems a code; the auth interceptors
	// read from it to validate signed requests. Tests insert keys directly.
	//
	// We prefer the bolt-backed stores when storage.Global() is online so paired
	// devices + unredeemed codes survive a hulation restart. Falls back to the
	// in-memory variants when bolt isn't available (no-storage dev runs, tests).
	var deviceKeyStore authware.DeviceKeyStore
	var pairCodeStore authware.PairCodeStore
	if s := storage.Global(); s != nil {
		log.Infof("pair: using bolt-backed device-key + pair-code stores")
		deviceKeyStore = hulabolt.NewDeviceKeyStore(s)
		pairCodeStore = hulabolt.NewPairCodeStore(s)
	} else {
		log.Warnf("pair: storage.Global() unavailable — using in-memory device-key + pair-code stores (state lost on restart)")
		deviceKeyStore = authware.NewInMemoryDeviceKeyStore()
		pairCodeStore = authware.NewInMemoryPairCodeStore()
	}
	wirePairHandlers(pairCodeStore, deviceKeyStore)

	srv, err = unified.NewServer(&unified.Config{
		Address:        addr,
		GetCertificate: getCert,
		ACMETLSALPN:    acmeTLSALPN,
		ClientCAs:      clientCAs,
		GRPCServerOptions: []grpc.ServerOption{
			grpc.UnaryInterceptor(AdminBearerInterceptor(deviceKeyStore)),
			grpc.StreamInterceptor(AdminBearerStreamInterceptor(deviceKeyStore)),
		},
		MuxOptions: []runtime.ServeMuxOption{
			// Emit proto field names as-is (snake_case) and keep default
			// values in the response. hulactl + the e2e harness expect
			// snake_case keys; the grpc-gateway default of lowerCamelCase
			// ("isAdmin") would break both.
			runtime.WithMarshalerOption(runtime.MIMEWildcard, &runtime.JSONPb{
				MarshalOptions: protojson.MarshalOptions{
					UseProtoNames:   true,
					EmitUnpopulated: true,
				},
				UnmarshalOptions: protojson.UnmarshalOptions{DiscardUnknown: true},
			}),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create unified server: %w", err)
	}

	// hulaagent Phase 3: attach the request-time agent-auth middleware
	// when the Agent CA is loaded. The middleware is a no-op for any
	// request that didn't present an Agent-CA-signed leaf — non-agent
	// traffic flows untouched.
	if clientCAs != nil {
		attachAgentMTLSMiddleware(srv)
	}

	// HA Stage 3: optionally turn on the team-only internal mTLS
	// channel. No-op when team.pki is unset (solo deployments).
	if err := bootEnableInternalChannel(srv, cfg); err != nil {
		return nil, fmt.Errorf("enable internal channel: %w", err)
	}

	// Register services. Additional services (auth, site, staging,
	// badactor, analytics) are registered by stage 0.7 as their
	// implementations land.
	grpcSrv := srv.GetGRPCServer()
	gwMux := srv.GetGatewayMux()

	// Status
	statusSvc := statusimpl.New()
	statusspec.RegisterStatusServiceServer(grpcSrv, statusSvc)
	if err := statusspec.RegisterStatusServiceHandlerServer(ctx, gwMux, statusSvc); err != nil {
		return nil, fmt.Errorf("register status handler: %w", err)
	}

	// Forms
	formsSvc := formsimpl.New()
	formsspec.RegisterFormsServiceServer(grpcSrv, formsSvc)
	if err := formsspec.RegisterFormsServiceHandlerServer(ctx, gwMux, formsSvc); err != nil {
		return nil, fmt.Errorf("register forms handler: %w", err)
	}

	// Landers
	landersSvc := landersimpl.New()
	landersspec.RegisterLandersServiceServer(grpcSrv, landersSvc)
	if err := landersspec.RegisterLandersServiceHandlerServer(ctx, gwMux, landersSvc); err != nil {
		return nil, fmt.Errorf("register landers handler: %w", err)
	}

	// BadActor
	badactorSvc := badactorimpl.New()
	badactorspec.RegisterBadActorServiceServer(grpcSrv, badactorSvc)
	if err := badactorspec.RegisterBadActorServiceHandlerServer(ctx, gwMux, badactorSvc); err != nil {
		return nil, fmt.Errorf("register badactor handler: %w", err)
	}

	// Site (production build triggers)
	siteSvc := siteimpl.New()
	sitespec.RegisterSiteServiceServer(grpcSrv, siteSvc)
	if err := sitespec.RegisterSiteServiceHandlerServer(ctx, gwMux, siteSvc); err != nil {
		return nil, fmt.Errorf("register site handler: %w", err)
	}

	// Staging build (WebDAV remains on the ServeMux fallback).
	stagingSvc := stagingimpl.New()
	stagingspec.RegisterStagingServiceServer(grpcSrv, stagingSvc)
	if err := stagingspec.RegisterStagingServiceHandlerServer(ctx, gwMux, stagingSvc); err != nil {
		return nil, fmt.Errorf("register staging handler: %w", err)
	}

	// Auth — skeleton impl. WhoAmI, GetMyPermissions, and
	// ListAuthProviders are live; the rest (LoginAdmin, LoginOIDC, user
	// CRUD, TOTP, invite, RefreshToken, GrantServerAccess family)
	// return Unimplemented pending the Bolt user-store wiring.
	authSvc := authimpl.New()
	authspec.RegisterAuthServiceServer(grpcSrv, authSvc)
	if err := authspec.RegisterAuthServiceHandlerServer(ctx, gwMux, authSvc); err != nil {
		return nil, fmt.Errorf("register auth handler: %w", err)
	}

	// Analytics — Phase-1 read endpoints backed by ClickHouse. The ACL
	// lookup grants admin callers every configured server id; non-admin
	// callers fall back to the authware/access helpers (stub until the
	// Bolt user store ships — admin-only flows continue to work).
	analyticsSvc := analyticsimpl.New(analyticsACLLookup(cfg), analyticsimpl.DefaultDB)
	analyticsspec.RegisterAnalyticsServiceServer(grpcSrv, analyticsSvc)
	if err := analyticsspec.RegisterAnalyticsServiceHandlerServer(ctx, gwMux, analyticsSvc); err != nil {
		return nil, fmt.Errorf("register analytics handler: %w", err)
	}

	// Goals — Phase 3.3. CRUD only for now; ListConversions + TestGoal
	// inherit Unimplemented until the query-builder goal-rule evaluator
	// lands in 3.3b.
	goalsSvc := goalsimpl.New()
	goalsspec.RegisterGoalsServiceServer(grpcSrv, goalsSvc)
	if err := goalsspec.RegisterGoalsServiceHandlerServer(ctx, gwMux, goalsSvc); err != nil {
		return nil, fmt.Errorf("register goals handler: %w", err)
	}

	// Reports — Phase 3.4. CRUD + Preview live; SendNow + ListRuns
	// land with the dispatcher in stage 3.5.
	reportsSvc := reportsimpl.New()
	reportsspec.RegisterReportsServiceServer(grpcSrv, reportsSvc)
	if err := reportsspec.RegisterReportsServiceHandlerServer(ctx, gwMux, reportsSvc); err != nil {
		return nil, fmt.Errorf("register reports handler: %w", err)
	}

	// Alerts — Phase 4.6. CRUD + ListAlertEvents. The evaluator
	// goroutine that actually fires rules lands in stage 4.7.
	alertsSvc := alertsimpl.New()
	alertsspec.RegisterAlertsServiceServer(grpcSrv, alertsSvc)
	if err := alertsspec.RegisterAlertsServiceHandlerServer(ctx, gwMux, alertsSvc); err != nil {
		return nil, fmt.Errorf("register alerts handler: %w", err)
	}

	// Chat — Phase 4b. Admin REST surface for chat history,
	// take/release, queue, search. The visitor-facing /chat/start
	// HTTP handler + the visitor / agent WebSockets land in
	// stages 4b.3 / 4b.4 / 4b.5 alongside hub + router; the live
	// view passed in here is nil until then (the impl substitutes
	// a noop view that reports no agents / visitor offline).
	chatStore := chatpkg.NewStore(model.GetSQLDB())
	// LiveSessionsView is wired to the per-process hub once
	// registerChatPublic creates it (later in this same boot).
	// Until then, chatHubSingleton is nil and chatimpl falls back
	// to its noop view; we hand a closure here that re-resolves
	// each call so /chat/admin/live-sessions returns real data
	// the moment the WS endpoint is up.
	chatSvc := chatimpl.New(chatStore, chatLiveLazy{}, chatACLLookup(cfg))
	// Wire the lazy hub accessor so the REST CloseSession RPC can
	// broadcast session_closed to the connected visitor once the
	// per-process hub is up (registerChatPublic, later in boot).
	chatSvc.SetHub(ChatHub)
	chatspec.RegisterChatServiceServer(grpcSrv, chatSvc)
	if err := chatspec.RegisterChatServiceHandlerServer(ctx, gwMux, chatSvc); err != nil {
		return nil, fmt.Errorf("register chat handler: %w", err)
	}

	// ChatStreamService — bidirectional agent stream + per-server control stream
	// that replace the two WebSocket endpoints (chat_ws_agent.go,
	// chat_ws_control.go). Hub + router are constructed lazily by
	// registerChatPublic() later in boot; the closures here re-resolve each call so
	// the gRPC path comes online the moment the singletons land.
	//
	// The Noise static secret is optional — when set, gRPC clients can opt into a
	// Noise_IK session-wrap around the per-session stream. Missing / malformed keys
	// degrade the chat stream to plaintext-only mode (mobile clients that demand
	// Noise will see noise_unavailable).
	//
	// Resolved through a getter (re-reading config each handshake) rather than
	// captured once, so a config reload / key rotation takes effect for new
	// streams without a restart — and stays consistent with what
	// /api/v1/installation/identity serves, which also re-reads per request.
	noiseStaticFn := func() []byte {
		c := hulaapp.GetConfig()
		if c == nil || c.NoiseStaticKey == "" {
			return nil
		}
		k, err := utils.DecodeNoiseStaticKey(c.NoiseStaticKey)
		if err != nil {
			log.Warnf("chat stream: noise_static_key decode: %s (Noise mode disabled)", err)
			return nil
		}
		return k
	}
	chatStreamSvc := chatimpl.NewStreamServer(
		chatStore,
		ChatHub,
		ChatRouter,
		chatACLLookup(cfg),
		noiseStaticFn,
	)
	chatspec.RegisterChatStreamServiceServer(grpcSrv, chatStreamSvc)

	// Mobile — Phase 5a.5. Compact Summary/Timeseries projections +
	// device registration. Delegates the analytics math to the
	// already-registered analyticsSvc; device storage rides on Bolt
	// via pkg/mobile/tokenbox for token sealing.
	mobileSvc := mobileimpl.New(
		analyticsSvc.Summary,
		analyticsSvc.Timeseries,
		analyticsSvc.Pages,
		chatStore,
		cfg,
		func() ([]byte, error) {
			return utils.GetTOTPEncryptionKey(cfg.TotpEncryptionKey)
		},
	)
	mobilespec.RegisterMobileServiceServer(grpcSrv, mobileSvc)
	if err := mobilespec.RegisterMobileServiceHandlerServer(ctx, gwMux, mobileSvc); err != nil {
		return nil, fmt.Errorf("register mobile handler: %w", err)
	}

	// Notify — Phase 5a.7. NotificationPrefs CRUD + TestNotification.
	notifySvc := notifyimpl.New()
	notifyspec.RegisterNotifyServiceServer(grpcSrv, notifySvc)
	if err := notifyspec.RegisterNotifyServiceHandlerServer(ctx, gwMux, notifySvc); err != nil {
		return nil, fmt.Errorf("register notify handler: %w", err)
	}

	// Initialize the provider manager from config.Auth.Providers.
	if err := initProviderManager(cfg); err != nil {
		return nil, fmt.Errorf("init provider manager: %w", err)
	}

	// Visitor tracking handlers keep a BounceMap (short-lived per-visitor
	// state keyed by bounce ID). Fiber used to call InitVisitorHandlers
	// inside router setup; the unified boot path needs to call it too or
	// HelloIframe / Hello / FormSubmit nil-panic on first request.
	handler.InitVisitorHandlers()

	// Register every non-gRPC HTTP endpoint (visitor tracking, scripts,
	// /hulastatus) on the unified server's ServeMux fallback. WebDAV
	// and per-host site routing are registered by their owning
	// subsystems at startup time — not here, because they need
	// host-level dispatch.
	RegisterFallbackRoutes(srv)

	// Populate authware.Claims on the request context from any Bearer
	// token. The gRPC UnaryInterceptor only fires for native gRPC
	// calls; grpc-gateway calls Handler implementations directly so
	// the gateway path needs this HTTP-level equivalent for WhoAmI and
	// GetMyPermissions to see the caller's identity.
	srv.AttachHTTPMiddleware(AdminBearerHTTPMiddleware)

	// Analytics: CSV export + per-user rate limiting. Only affects
	// /api/v1/analytics/* requests; everything else passes through.
	// Must attach AFTER AdminBearerHTTPMiddleware so the rate limiter
	// can key off authware.Claims populated upstream.
	srv.AttachHTTPMiddleware(analyticsHTTPMiddleware)

	// Per-host backend proxies (Docker containers configured under a
	// server's `backends:` block). Dispatched by HTTP middleware that
	// matches on (Host, path-prefix) and hands off to
	// httputil.ReverseProxy before the rest of the pipeline runs.
	registerBackendProxies(srv, cfg)

	// Per-host static file serving (server.Root directory). Attached
	// AFTER backend proxies so backend paths like /api take priority
	// over static files when both are configured on the same host.
	registerStaticSites(srv, cfg)

	// Top-level `proxies:` — path-preserving reverse proxy to an arbitrary
	// target URL (e.g. a hula-push-relay sidecar on localhost). Distinct from
	// `backends:`, which manage containers + rewrite the path. Attached AFTER
	// backend/static registration (most-recently-attached runs first) so a
	// by_domain proxy intercepts before static serving / backends and owns its
	// host. CORS + HSTS are attached later still, so they remain outermost and
	// proxied responses keep those headers.
	registerProxies(srv, cfg)

	// Phase-2 analytics dashboard — serves the SvelteKit build tree
	// at /analytics/* and a tiny config shim at /analytics/config.json
	// that the UI reads on boot. No-op when the bundle isn't present.
	registerAnalyticsUI(srv, cfg)

	// Phase 4b — visitor chat public endpoints. Registers
	// POST /api/v1/chat/start (token issuer); the WS endpoint
	// arrives in stage 4b.4.
	registerChatPublic(srv, cfg)

	// Public installation-identity endpoint — returns the Noise static public
	// key so mobile clients can pin it at pair time without first
	// authenticating. No-op when no Noise key is configured; the handler
	// returns 404 in that case.
	srv.RegisterCustomHandler("GET /api/v1/installation/identity", installationIdentityHandler())

	// QR pair flow — admin issues a single-use code via /pair/issue (bearer-
	// authed); the mobile app redeems it via /pair/redeem (unauthenticated;
	// the code is the proof). Redemption writes a DeviceKey into the
	// deviceKeyStore above so subsequent signed requests from the device
	// satisfy the AdminBearerInterceptor's `claimsFromDeviceSignature` path.
	srv.RegisterCustomHandler("POST /api/v1/pair/issue", pairIssueHandler())
	srv.RegisterCustomHandler("POST /api/v1/pair/redeem", pairRedeemHandler())
	// Self-service + admin list/revoke. List is GET so curl-ability for
	// operators is preserved; revoke is POST since it mutates and we want
	// the CSRF protections the existing middleware applies to POST routes.
	srv.RegisterCustomHandler("GET /api/v1/pair/devices", pairListDevicesHandler())
	srv.RegisterCustomHandler("POST /api/v1/pair/devices/revoke", pairRevokeDeviceHandler())

	// CORS — must be among the OUTERMOST middleware. CORS needs to
	// see OPTIONS preflights before auth/proxy middleware drops them,
	// and add Access-Control-* headers to every response regardless
	// of which handler produced it.
	srv.AttachHTTPMiddleware(CORSMiddleware(cfg))

	// HSTS — Strict-Transport-Security header on every HTTPS response.
	// Reads global defaults from pkg/tune and per-virtualhost
	// overrides from cfg.Servers[].HSTS. Attached after CORS so it's
	// OUTERMOST: header-setting middlewares need to fire even when an
	// inner middleware (e.g. unified_static.go's static-host shortcut)
	// bypasses `next` and serves directly. See
	// server/hsts_middleware.go.
	srv.AttachHTTPMiddleware(hstsMiddleware(cfg))

	// Per-server static TLS certs. Each configured server can ship its
	// own cert+key; the unified server's SNI selector maps Host →
	// certificate at handshake time. Servers without static cert files
	// (e.g. ACME-only or Cloudflare Origin CA) are skipped here — those
	// flows plug in via the GetCertificate path or the static root
	// above.
	for _, s := range cfg.Servers {
		if s == nil || s.SSL == nil || s.Host == "" {
			continue
		}
		// After config load, SSL.Cert/Key hold PEM content (not file
		// paths) because config.LoadSSLConfig has already read them.
		// Call LoadSSLConfig opportunistically in case SSL wasn't
		// processed during server-setup pass, then use the parsed
		// *tls.Certificate directly.
		if s.SSL.GetTLSCert() == nil {
			if lerr := s.SSL.LoadSSLConfig(); lerr != nil {
				unifiedLog.Warnf("per-host SSL load %s: %v", s.Host, lerr)
				continue
			}
		}
		cert := s.SSL.GetTLSCert()
		if cert == nil {
			continue
		}
		srv.AddHostCertificate(s.Host, cert)
		for _, alias := range s.Aliases {
			if alias == "" {
				continue
			}
			srv.AddHostCertificate(alias, cert)
		}
	}

	unifiedLog.Infof("Unified server constructed on %s (gRPC + REST gateway + ServeMux fallback)", addr)
	return srv, nil
}

// initProviderManager takes the YAML auth.providers[] and feeds it to the
// authware ProviderManager. Called once on startup; subsequent reloads are
// not supported in Phase 0.
func initProviderManager(cfg *config.Config) error {
	pm := authprovider.GetProviderManager()
	if cfg.Auth == nil || len(cfg.Auth.Providers) == 0 {
		// No providers configured — the local admin (break-glass) user
		// still works via hula's legacy /api/auth/login handler. When
		// stage 0.7 migrates auth to gRPC, the internal provider is
		// registered by default.
		unifiedLog.Debugf("no auth.providers configured; provider manager empty")
		return nil
	}

	providerCfgs := make([]*baseprovider.AuthProviderConfig, 0, len(cfg.Auth.Providers))
	for _, p := range cfg.Auth.Providers {
		// Marshal the raw map back into yaml.Node so the base
		// AuthProviderConfig (which uses yaml.Node for its polymorphic
		// Config field) can decode it per-provider.
		var node yaml.Node
		if p.Config != nil {
			data, err := yaml.Marshal(p.Config)
			if err != nil {
				return fmt.Errorf("marshal provider %q config: %w", p.Name, err)
			}
			if err := yaml.Unmarshal(data, &node); err != nil {
				return fmt.Errorf("unmarshal provider %q config: %w", p.Name, err)
			}
			// yaml.Unmarshal wraps in a DocumentNode; unwrap one level.
			if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
				node = *node.Content[0]
			}
		}
		providerCfgs = append(providerCfgs, &baseprovider.AuthProviderConfig{
			Name:         p.Name,
			ProviderType: p.Provider,
			Config:       node,
		})
	}

	errs := pm.CreateAndRegisterProvdiders(providerCfgs)
	if len(errs) > 0 {
		for name, err := range errs {
			unifiedLog.Errorf("auth provider %q failed: %v", name, err)
		}
		return fmt.Errorf("%d provider(s) failed to register", len(errs))
	}
	return nil
}
