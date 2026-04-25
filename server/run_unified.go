package server

// RunUnified is the Phase-0 unified-server entry point. It boots the
// single HTTPS listener (gRPC + REST gateway + ServeMux fallback) and
// blocks until ctx is cancelled or the process receives SIGINT/SIGTERM.
//
// This is an alternative to Run() which still uses the legacy Fiber
// listener. The cutover happens when main.go switches from Run() to
// RunUnified(). Per-subsystem setup (badactor, site deploy, staging,
// backends, etc.) that currently lives inside Run() is replicated here
// so the two entry points can coexist during the transition.

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tlalocweb/hulation/backend"
	"github.com/tlalocweb/hulation/badactor"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
	alertsevaluator "github.com/tlalocweb/hulation/pkg/alerts/evaluator"
	"github.com/tlalocweb/hulation/pkg/auth/opaque"
	"github.com/tlalocweb/hulation/pkg/mailer"
	"github.com/tlalocweb/hulation/pkg/notifier"
	"github.com/tlalocweb/hulation/pkg/notifier/apns"
	"github.com/tlalocweb/hulation/pkg/notifier/email"
	"github.com/tlalocweb/hulation/pkg/notifier/fcm"
	"github.com/tlalocweb/hulation/pkg/realtime"
	"github.com/tlalocweb/hulation/pkg/reports/dispatch"
	"github.com/tlalocweb/hulation/utils"
	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"
	"github.com/tlalocweb/hulation/pkg/store/clickhouse"
	"github.com/tlalocweb/hulation/sitedeploy"
)

// RunUnified initializes shared subsystems and boots the unified server.
// Returns when ctx is cancelled or a termination signal is received.
// Non-zero exit code indicates a startup or shutdown error worth
// propagating to main's os.Exit.
func RunUnified(parentCtx context.Context, conf *config.Config) (exitcode int) {
	if conf == nil {
		log.Errorf("RunUnified: config is nil")
		return 1
	}
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	if err := preloadSharedSubsystems(ctx, conf); err != nil {
		log.Errorf("RunUnified: preload failed: %s", err.Error())
		return 1
	}

	srv, err := BootUnifiedServer(ctx, conf)
	if err != nil {
		log.Errorf("RunUnified: boot failed: %s", err.Error())
		return 1
	}
	if err := srv.Start(ctx); err != nil {
		log.Errorf("RunUnified: start failed: %s", err.Error())
		return 1
	}
	log.Infof("Unified server listening on %s", srv.GetAddress())

	// Wait for SIGINT/SIGTERM.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-ctx.Done():
		log.Infof("RunUnified: parent context cancelled")
	case s := <-sigCh:
		log.Infof("RunUnified: received %s", s)
	}

	// Graceful shutdown.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := srv.Stop(shutdownCtx); err != nil {
		log.Errorf("RunUnified: shutdown failed: %s", err.Error())
		return 1
	}
	log.Infof("Unified server stopped")
	return 0
}

// preloadSharedSubsystems runs the startup work that's independent of
// which listener (Fiber or unified) handles requests. Mirrors the
// corresponding block in Run() so the two entry points stay in sync.
func preloadSharedSubsystems(ctx context.Context, conf *config.Config) error {
	if err := model.PreloadDefinedLanders(model.GetDB()); err != nil {
		return fmt.Errorf("preload landers: %w", err)
	}
	if err := model.PreloadDefinedForms(model.GetDB()); err != nil {
		return fmt.Errorf("preload forms: %w", err)
	}

	// BoltDB store — identity/ACL/goals/reports data that doesn't
	// belong in ClickHouse. Opened once; lives as a process global
	// until Close(). Non-fatal when the path can't be created (e.g.,
	// running `hulactl` style utilities that don't need persistence)
	// so degrade gracefully.
	if _, err := hulabolt.Open(""); err != nil {
		log.Warnf("Bolt store unavailable (%s); ACL + goals + reports RPCs will 503", err.Error())
	}

	// Scheduled-report dispatcher. Runs on a 1-minute ticker + an
	// on-demand SendNow queue. No-op until scheduled reports are
	// created via the ReportsService. Non-fatal when the mailer isn't
	// configured — dispatcher logs renders without sending so the
	// admin UI still works for preview flows.
	m := mailer.New(conf.Mailer)
	if conf.Mailer != nil && conf.Mailer.Configured() {
		log.Infof("mailer: SMTP %s:%d from=%q starttls=%v",
			conf.Mailer.Host, conf.Mailer.Port, conf.Mailer.From, conf.Mailer.StartTLS)
	} else {
		log.Infof("mailer: not configured — scheduled reports render but will not send")
	}
	dispatch.Start(ctx, m)

	// Notifier composite — Phase 5a.3. Email backend always; APNs +
	// FCM plug in when creds are present. The APNs + FCM configs
	// degrade to "not configured" on missing creds, so we register
	// them unconditionally — per-recipient ErrNotConfigured is how
	// the downstream evaluator knows to mark delivery status
	// "mailer_unconfigured" for that channel.
	composite := notifier.NewComposite(email.New(m))
	if conf.APNS != nil && conf.APNS.KeyPEMPath != "" {
		apnsBackend, err := apns.New(apns.Config{
			TeamID:     conf.APNS.TeamID,
			KeyID:      conf.APNS.KeyID,
			KeyPEMPath: conf.APNS.KeyPEMPath,
			BundleID:   conf.APNS.BundleID,
			Endpoint:   conf.APNS.Endpoint,
		})
		if err != nil {
			log.Warnf("apns: backend init failed: %s", err.Error())
		} else {
			composite.Add(apnsBackend)
			log.Infof("apns: backend registered (team=%s bundle=%s)", conf.APNS.TeamID, conf.APNS.BundleID)
		}
	}
	if conf.FCM != nil && conf.FCM.ServiceAccountJSONPath != "" {
		fcmBackend, err := fcm.New(fcm.Config{
			ProjectID:              conf.FCM.ProjectID,
			ServiceAccountJSONPath: conf.FCM.ServiceAccountJSONPath,
		})
		if err != nil {
			log.Warnf("fcm: backend init failed: %s", err.Error())
		} else {
			composite.Add(fcmBackend)
			log.Infof("fcm: backend registered (project=%s)", conf.FCM.ProjectID)
		}
	}
	notifier.SetGlobal(composite)

	// OPAQUE PAKE — replaces plaintext password exchange for admin
	// + internal-provider logins. See OPAQUE_PLAN.md.
	{
		var opaqueSeedCfg, opaqueAKECfg string
		if conf.OPAQUE != nil {
			opaqueSeedCfg = conf.OPAQUE.OPRFSeed
			opaqueAKECfg = conf.OPAQUE.AKESecret
		}
		seed, akePriv, akePub, err := opaque.LoadOrGenerate(opaqueSeedCfg, opaqueAKECfg)
		if err != nil {
			log.Warnf("OPAQUE: key material init failed: %s — OPAQUE login + register will be unavailable", err.Error())
		} else if osrv, err := opaque.New(seed, akePriv, akePub); err != nil {
			log.Warnf("OPAQUE: server init failed: %s", err.Error())
		} else {
			opaque.SetGlobal(osrv)
			log.Infof("OPAQUE: %s", osrv.Suite())
		}
	}

	// Realtime pub/sub hub — Phase 5a.6. Broadcasters:
	//   * /api/mobile/v1/debug/publish (used by e2e suite 30)
	//   * TODO: visitor-tracking ingest hook (follow-up)
	// Subscribers: every connection on /api/mobile/v1/events.
	realtime.SetGlobal(realtime.New())

	// Install the master key the evaluator uses to open sealed push
	// tokens before handing them to the notifier. Same key the TOTP
	// subsystem uses; see pkg/mobile/tokenbox.
	if key, err := utils.GetTOTPEncryptionKey(conf.TotpEncryptionKey); err == nil {
		alertsevaluator.SetTokenKey(key)
	} else {
		log.Warnf("alerts evaluator: no TOTP encryption key; push delivery will be disabled")
	}

	// Alert rule evaluator — Phase 4.7 (now Notifier-backed after
	// stage 5a.4). Runs on a 1-minute ticker, evaluates every
	// enabled alert against the kind-specific predicate and fires
	// via the composite notifier (email + push fan-out).
	// No-op until alerts are created via AlertsService.
	if db := model.GetSQLDB(); db != nil {
		alertsevaluator.Start(ctx, m, db)
	} else {
		log.Warnf("alerts evaluator: ClickHouse not available; alert rules will not fire until DB is reachable")
	}

	// Apply ClickHouse migrations — brings up the MV state tables and
	// materialized views that the analytics query builder reads from.
	// Phase 0 defined the SQL files; Phase 1 wires the runner into boot.
	// Non-fatal on failure — analytics endpoints fall back to raw
	// events when the MVs aren't present, and an ops team may not want
	// DDL running unattended on an unhealthy cluster.
	if db := model.GetSQLDB(); db != nil {
		ttl := 0 // zero → runner picks DefaultEventsTTLDays
		if conf.Analytics != nil {
			ttl = conf.Analytics.EventsTTLDays
		}
		if err := clickhouse.Apply(ctx, db, ttl); err != nil {
			log.Warnf("ClickHouse migrations failed to apply: %s", err.Error())
		} else {
			log.Infof("ClickHouse schema + migrations applied")
		}
	}

	if conf.BadActors != nil && !conf.BadActors.Disable {
		var cfCIDRs []*net.IPNet
		if conf.IsCloudflareMode() {
			cfCIDRs = conf.GetCloudflareIPs().Ranges()
		}
		if err := badactor.Init(conf.BadActors, model.GetDB(), conf.Servers, cfCIDRs); err != nil {
			log.Errorf("Failed to initialize bad actor detection: %s", err.Error())
			// Non-fatal — matches Run() semantics.
		}
	}

	// Docker backends per-server.
	hasBackends := false
	for _, s := range conf.Servers {
		if len(s.Backends) > 0 {
			hasBackends = true
			break
		}
	}
	if hasBackends {
		mgr, berr := backend.NewManager(conf.Registries)
		if berr != nil {
			return fmt.Errorf("backend manager: %w", berr)
		}
		startCtx, startCancel := context.WithTimeout(ctx, 5*time.Minute)
		defer startCancel()
		for _, s := range conf.Servers {
			if len(s.Backends) > 0 {
				if berr := mgr.StartBackendsForServer(startCtx, s.Host, s.Backends); berr != nil {
					return fmt.Errorf("start backends for %s: %w", s.Host, berr)
				}
			}
		}
		// mgr is held by the global state it registers; no Close here
		// because the process will exit at shutdown anyway. If a test
		// harness needs finer control, it can call backend.Close()
		// directly.
	}

	// Site deploy build manager — only if any server has git autodeploy.
	hasAutoDeploy := false
	for _, s := range conf.Servers {
		if s.GitAutoDeploy != nil {
			hasAutoDeploy = true
			break
		}
	}
	if hasAutoDeploy {
		buildMgr, berr := sitedeploy.NewBuildManager()
		if berr != nil {
			log.Errorf("Failed to initialize site deploy manager: %s", berr.Error())
			// Non-fatal — site deploy disabled, server continues.
		} else {
			sitedeploy.SetGlobalBuildManager(buildMgr)
			buildMgr.ResolveSiteRoots(conf.Servers)
			stagingMgr := sitedeploy.NewStagingManager(buildMgr.DockerClient())
			sitedeploy.SetGlobalStagingManager(stagingMgr)
			log.Infof("Site deploy build manager initialized")
			// Launch long-lived staging containers for every server
			// configured with `hula_build: staging`. The legacy Fiber
			// boot path did this; the unified rewrite dropped the call
			// and staging-* commands nil-return from GetStagingContainer
			// without it.
			go stagingMgr.StartupStaging(conf.Servers)
		}
	}

	return nil
}
