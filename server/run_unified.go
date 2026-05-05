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
	"sync"
	"syscall"
	"time"

	"github.com/tlalocweb/hulation/backend"
	"github.com/tlalocweb/hulation/badactor"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
	alertsevaluator "github.com/tlalocweb/hulation/pkg/alerts/evaluator"
	"github.com/tlalocweb/hulation/pkg/auth/opaque"
	"github.com/tlalocweb/hulation/pkg/forwarder"
	"github.com/tlalocweb/hulation/pkg/mailer"
	"github.com/tlalocweb/hulation/pkg/notifier"
	"github.com/tlalocweb/hulation/pkg/notifier/apns"
	"github.com/tlalocweb/hulation/pkg/notifier/email"
	"github.com/tlalocweb/hulation/pkg/notifier/fcm"
	"github.com/tlalocweb/hulation/pkg/realtime"
	"github.com/tlalocweb/hulation/pkg/reports/dispatch"
	"github.com/tlalocweb/hulation/pkg/server/unified"
	"github.com/tlalocweb/hulation/utils"
	"github.com/tlalocweb/hulation/pkg/store/clickhouse"
	"github.com/tlalocweb/hulation/pkg/store/storage"
	raftbackend "github.com/tlalocweb/hulation/pkg/store/storage/raft"
	"github.com/tlalocweb/hulation/sitedeploy"
)

// RunUnified initializes shared subsystems and boots the unified server.
// Returns when ctx is cancelled or a termination signal is received.
// Non-zero exit code indicates a startup or shutdown error worth
// propagating to main's os.Exit.
//
// Boot ordering is structured to get the listener (and therefore the
// per-host static file servers) accepting requests as fast as
// possible. Two phases:
//
//  1. preloadFastSubsystems — synchronous, only the work the listener
//     hard-depends on: storage, OPAQUE keys, mailer + notifier
//     handles, realtime hub, scheduled-report dispatcher start,
//     forwarders. Anything that talks to ClickHouse beyond the
//     handle that main.go already opened, anything that pulls
//     Docker images, anything that clones git, all gets deferred.
//
//  2. preloadSlowSubsystems — runs in the background AFTER the
//     listener is up. ClickHouse migrations, badactor (init +
//     loadFromDB), per-server backend container startup, git
//     auto-deploy resolve + clone, staging-container startup. None
//     of these block static file serving; the listener has already
//     accepted requests by the time they start.
func RunUnified(parentCtx context.Context, conf *config.Config) (exitcode int) {
	if conf == nil {
		log.Errorf("RunUnified: config is nil")
		return 1
	}
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	if err := preloadFastSubsystems(ctx, conf); err != nil {
		log.Errorf("RunUnified: fast preload failed: %s", err.Error())
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

	// Slow subsystems run in the background so the listener is
	// already serving by the time they start. The goroutine attaches
	// the badactor incident recorder once badactor.Init completes.
	go preloadSlowSubsystems(ctx, conf, srv.SetIncidentRecorder)

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
	//
	// Order matters: stop the listener FIRST so no new requests arrive
	// at the about-to-die backends. Then tear down docker-spawned
	// children (backends and staging containers) so a hula restart
	// brings them up with fresh env, and the host doesn't leak
	// containers across upgrades. Each step gets its own bounded
	// context so a slow docker daemon can't hang shutdown forever.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := srv.Stop(shutdownCtx); err != nil {
		log.Errorf("RunUnified: shutdown failed: %s", err.Error())
		// don't return — still attempt backend teardown
	}
	log.Infof("Unified server stopped")
	stopDockerChildren()
	return 0
}

// stopDockerChildren tears down every docker container hula spawned:
// backend containers (backend.Manager) and long-lived staging
// containers (sitedeploy.StagingManager). Bounded by its own timeout
// so a wedged docker daemon can't keep hula's process alive past the
// shutdown grace period.
func stopDockerChildren() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if mgr := backend.GetGlobalManager(); mgr != nil {
		log.Infof("Shutdown: stopping backend containers...")
		if err := mgr.StopAll(ctx); err != nil {
			log.Errorf("Shutdown: backend StopAll: %s", err.Error())
		}
	}
	if sm := sitedeploy.GetStagingManager(); sm != nil {
		log.Infof("Shutdown: stopping staging containers...")
		sm.Close()
	}
}

// preloadFastSubsystems runs synchronously before the listener opens.
// Only includes work the listener hard-depends on or that is cheap
// enough to keep here. Anything that touches Docker, ClickHouse DDL,
// or git is deferred to preloadSlowSubsystems.
func preloadFastSubsystems(ctx context.Context, conf *config.Config) error {
	// Lander + form preloads run gorm queries against ClickHouse. They
	// keep the routing tables warm so the first request finds them
	// without a cold-cache lookup. main.SetupAppDB has already opened
	// the connection synchronously, so this is fast.
	if err := model.PreloadDefinedLanders(model.GetDB()); err != nil {
		return fmt.Errorf("preload landers: %w", err)
	}
	if err := model.PreloadDefinedForms(model.GetDB()); err != nil {
		return fmt.Errorf("preload forms: %w", err)
	}

	// Persistent store — identity/ACL/goals/reports data that
	// doesn't belong in ClickHouse. Stage 2 of HA Plan: a
	// Raft-backed RaftStorage in single-node mode. Non-fatal when
	// init fails (degrades to no-storage so hulactl-style utilities
	// that don't touch persistence still work).
	if rcfg, err := raftbackend.AutoConfig(conf.Team); err != nil {
		log.Warnf("storage: team config error (%s); ACL + goals + reports RPCs will 503", err.Error())
	} else if rs, err := raftbackend.New(rcfg); err != nil {
		log.Warnf("storage unavailable (%s); ACL + goals + reports RPCs will 503", err.Error())
	} else {
		waitCtx, waitCancel := context.WithTimeout(ctx, 30*time.Second)
		if err := rs.WaitLeader(waitCtx); err != nil {
			log.Warnf("storage: solo bootstrap stalled (%s); ACL + goals + reports RPCs may 503", err.Error())
		}
		waitCancel()
		storage.SetGlobal(rs)
		log.Infof("Raft storage online (node=%s, data_dir=%s, mode=solo)", rcfg.NodeID, rcfg.DataDir)
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

	// Notifier composite — email always; APNs + FCM plug in when
	// creds are present. Per-recipient ErrNotConfigured is how the
	// downstream evaluator knows to mark a missing channel.
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
	// + internal-provider logins.
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

	// Realtime pub/sub hub.
	realtime.SetGlobal(realtime.New())

	// Token key for the alerts evaluator — needed at every push send,
	// so resolve it before evaluator goroutines start scoring.
	if key, err := utils.GetTOTPEncryptionKey(conf.TotpEncryptionKey); err == nil {
		alertsevaluator.SetTokenKey(key)
	} else {
		log.Warnf("alerts evaluator: no TOTP encryption key; push delivery will be disabled")
	}

	// Alert rule evaluator. The 1-minute ticker + queue are cheap to
	// spin up; only firings touch ClickHouse, and those are tolerant
	// of transient unavailability.
	if db := model.GetSQLDB(); db != nil {
		alertsevaluator.Start(ctx, m, db)
	} else {
		log.Warnf("alerts evaluator: ClickHouse not available; alert rules will not fire until DB is reachable")
	}

	// Per-server forwarders are pure config wiring — no I/O at
	// registration. Cheap, keep here.
	if n := forwarder.BuildAndRegisterAll(conf.Servers); n > 0 {
		log.Infof("forwarders registered for %d server(s)", n)
	}

	return nil
}

// preloadSlowSubsystems runs in a goroutine AFTER the listener opens.
// Everything in here is non-fatal: failures degrade specific
// surfaces (analytics, autodeploy, badactor scoring, backend proxies)
// without taking the static file servers offline.
//
// setIncidentRecorder is the callback for wiring the listener's
// incident hook once badactor.Init completes. Passed in (rather than
// the *unified.Server) to keep the import graph one-way:
// run_unified.go is the only file in pkg/server that knows about
// pkg/server/unified.Server, and we want preloadSlowSubsystems to
// stay easy to test without standing up a real listener.
func preloadSlowSubsystems(ctx context.Context, conf *config.Config, setIncidentRecorder func(unified.IncidentRecorder)) {
	// Apply ClickHouse migrations — brings up the MV state tables
	// and materialized views that the analytics query builder reads
	// from. Non-fatal on failure — analytics endpoints fall back to
	// raw events when the MVs aren't present.
	if db := model.GetSQLDB(); db != nil {
		ttl := 0
		if conf.Analytics != nil {
			ttl = conf.Analytics.EventsTTLDays
		}
		chatRet := 0
		if conf.Chat != nil {
			chatRet = conf.Chat.RetentionDays
		}
		if err := clickhouse.Apply(ctx, db, ttl, chatRet); err != nil {
			log.Warnf("ClickHouse migrations failed to apply: %s", err.Error())
		} else {
			log.Infof("ClickHouse schema + migrations applied")
		}
	}

	// Bad-actor detection. Init does its own DDL + loadFromDB; once
	// it's online we wire the listener's incident recorder so TLS
	// handshake errors and protocol-peek EOFs contribute to scoring.
	if conf.BadActors != nil && !conf.BadActors.Disable {
		var cfCIDRs []*net.IPNet
		if conf.IsCloudflareMode() {
			cfCIDRs = conf.GetCloudflareIPs().Ranges()
		}
		if err := badactor.Init(conf.BadActors, model.GetDB(), conf.Servers, cfCIDRs); err != nil {
			log.Errorf("Failed to initialize bad actor detection: %s", err.Error())
		} else if store := badactor.GetStore(); store != nil && setIncidentRecorder != nil {
			setIncidentRecorder(store)
			log.Infof("badactor: incident recorder wired to unified listener")
		}
	}

	// Docker backends per-server. Each StartBackendsForServer call
	// pulls the image + starts the container + waits for healthcheck.
	// Run them concurrently so a slow image doesn't gate every
	// other server, and downgrade failures from fatal to logged —
	// the backend reverse proxy already returns 502/503 when its
	// upstream isn't ready, so the static and api surfaces of an
	// unrelated server stay up.
	hasBackends := false
	for _, s := range conf.Servers {
		if len(s.Backends) > 0 {
			hasBackends = true
			break
		}
	}
	if hasBackends {
		mgr, berr := backend.NewManager(conf.Registries, conf.BackendLogs)
		if berr != nil {
			log.Errorf("backend manager: %s", berr.Error())
		} else {
			// Publish before any backend starts so a SIGTERM
			// arriving mid-startup can still find the manager and
			// tear down whatever did come up.
			backend.SetGlobalManager(mgr)
			startCtx, startCancel := context.WithTimeout(ctx, 5*time.Minute)
			defer startCancel()
			var wg sync.WaitGroup
			for _, s := range conf.Servers {
				if len(s.Backends) == 0 {
					continue
				}
				wg.Add(1)
				go func(host string, bks []*backend.BackendConfig) {
					defer wg.Done()
					if err := mgr.StartBackendsForServer(startCtx, host, bks); err != nil {
						log.Errorf("backends for %s failed to start: %s", host, err.Error())
					}
				}(s.Host, s.Backends)
			}
			wg.Wait()
		}
	}

	// Site deploy build manager — only if any server has git
	// autodeploy. Static roots are already pointed at the right
	// directory by config validation, so the site serves whatever
	// content is on disk from the previous boot. StartupBuildAll
	// then pulls and rebuilds in this goroutine; the rebuild lands
	// via an atomic rename swap (deploySite), so the cached site
	// keeps serving until the new build is ready.
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
		} else {
			sitedeploy.SetGlobalBuildManager(buildMgr)
			stagingMgr := sitedeploy.NewStagingManager(buildMgr.DockerClient())
			sitedeploy.SetGlobalStagingManager(stagingMgr)
			log.Infof("Site deploy build manager initialized")
			// Start staging containers (long-lived) and run
			// production startup pulls + builds concurrently. Either
			// can be slow; running them in parallel keeps total
			// "fully ready" time as short as possible.
			go stagingMgr.StartupStaging(conf.Servers)
			go buildMgr.StartupBuildAll(conf.Servers)
		}
	}
}

