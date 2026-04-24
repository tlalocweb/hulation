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

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/handler"
	"github.com/tlalocweb/hulation/log"
	statusimpl "github.com/tlalocweb/hulation/pkg/api/v1/status"
	statusspec "github.com/tlalocweb/hulation/pkg/apispec/v1/status"
	analyticsimpl "github.com/tlalocweb/hulation/pkg/api/v1/analytics"
	authimpl "github.com/tlalocweb/hulation/pkg/api/v1/auth"
	badactorimpl "github.com/tlalocweb/hulation/pkg/api/v1/badactor"
	formsimpl "github.com/tlalocweb/hulation/pkg/api/v1/forms"
	goalsimpl "github.com/tlalocweb/hulation/pkg/api/v1/goals"
	landersimpl "github.com/tlalocweb/hulation/pkg/api/v1/landers"
	siteimpl "github.com/tlalocweb/hulation/pkg/api/v1/site"
	stagingimpl "github.com/tlalocweb/hulation/pkg/api/v1/staging"
	analyticsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/analytics"
	authspec "github.com/tlalocweb/hulation/pkg/apispec/v1/auth"
	badactorspec "github.com/tlalocweb/hulation/pkg/apispec/v1/badactor"
	formsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/forms"
	goalsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/goals"
	landersspec "github.com/tlalocweb/hulation/pkg/apispec/v1/landers"
	sitespec "github.com/tlalocweb/hulation/pkg/apispec/v1/site"
	stagingspec "github.com/tlalocweb/hulation/pkg/apispec/v1/staging"
	authprovider "github.com/tlalocweb/hulation/pkg/server/authware/provider"
	baseprovider "github.com/tlalocweb/hulation/pkg/server/authware/provider/base"
	"github.com/tlalocweb/hulation/pkg/server/unified"

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
	if staticCert == nil && getCert == nil {
		// Fallback: no static cert AND no ACME manager. Generate an
		// in-memory self-signed cert for the admin listener. Matches
		// the legacy Fiber behaviour and keeps local / dev / test
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

	srv, err = unified.NewServer(&unified.Config{
		Address:        addr,
		GetCertificate: getCert,
		GRPCServerOptions: []grpc.ServerOption{
			grpc.UnaryInterceptor(AdminBearerInterceptor()),
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

	// Phase-2 analytics dashboard — serves the SvelteKit build tree
	// at /analytics/* and a tiny config shim at /analytics/config.json
	// that the UI reads on boot. No-op when the bundle isn't present.
	registerAnalyticsUI(srv, cfg)

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

