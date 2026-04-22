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

	"golang.org/x/crypto/acme/autocert"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	statusimpl "github.com/tlalocweb/hulation/pkg/api/v1/status"
	statusspec "github.com/tlalocweb/hulation/pkg/apispec/v1/status"
	authimpl "github.com/tlalocweb/hulation/pkg/api/v1/auth"
	badactorimpl "github.com/tlalocweb/hulation/pkg/api/v1/badactor"
	formsimpl "github.com/tlalocweb/hulation/pkg/api/v1/forms"
	landersimpl "github.com/tlalocweb/hulation/pkg/api/v1/landers"
	siteimpl "github.com/tlalocweb/hulation/pkg/api/v1/site"
	stagingimpl "github.com/tlalocweb/hulation/pkg/api/v1/staging"
	authspec "github.com/tlalocweb/hulation/pkg/apispec/v1/auth"
	badactorspec "github.com/tlalocweb/hulation/pkg/apispec/v1/badactor"
	formsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/forms"
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
	var tlsCert, tlsKey string
	var getCert func(*tls.ClientHelloInfo) (*tls.Certificate, error)

	if cfg.HulaSSL != nil {
		tlsCert = cfg.HulaSSL.Cert
		tlsKey = cfg.HulaSSL.Key
		if cfg.HulaSSL.ACME != nil {
			acfg := cfg.HulaSSL.ACME
			mgr := &autocert.Manager{
				Prompt: autocert.AcceptTOS,
				Cache:  autocert.DirCache(acfg.CacheDir),
				Email:  acfg.Email,
			}
			if len(acfg.Domains) > 0 {
				mgr.HostPolicy = autocert.HostWhitelist(acfg.Domains...)
			}
			getCert = mgr.GetCertificate
			unifiedLog.Infof("ACME enabled: cache=%q email=%q domains=%v", acfg.CacheDir, acfg.Email, acfg.Domains)
		}
	}
	if (tlsCert == "" || tlsKey == "") && getCert == nil {
		return nil, fmt.Errorf("hula_ssl: set cert/key files, acme.domains, or both — none is configured")
	}

	addr := fmt.Sprintf(":%d", cfg.Port)
	if cfg.ListenOn != "" {
		addr = cfg.ListenOn
	}

	srv, err = unified.NewServer(&unified.Config{
		Address:        addr,
		TLSCertFile:    tlsCert,
		TLSKeyFile:     tlsKey,
		GetCertificate: getCert,
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

	// Initialize the provider manager from config.Auth.Providers.
	if err := initProviderManager(cfg); err != nil {
		return nil, fmt.Errorf("init provider manager: %w", err)
	}

	// Register every non-gRPC HTTP endpoint (visitor tracking, scripts,
	// /hulastatus) on the unified server's ServeMux fallback. WebDAV
	// and per-host site routing are registered by their owning
	// subsystems at startup time — not here, because they need
	// host-level dispatch.
	RegisterFallbackRoutes(srv)

	// Per-host backend proxies (Docker containers configured under a
	// server's `backends:` block). Dispatched by HTTP middleware that
	// matches on (Host, path-prefix) and hands off to
	// httputil.ReverseProxy before the rest of the pipeline runs.
	registerBackendProxies(srv, cfg)

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

