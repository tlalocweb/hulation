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
		}
	}

	return nil
}
