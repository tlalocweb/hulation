package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gofiber/contrib/fiberzerolog"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/backend"
	"github.com/tlalocweb/hulation/badactor"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/handler"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
	"github.com/tlalocweb/hulation/router"
	"github.com/tlalocweb/hulation/sitedeploy"
	"golang.org/x/net/http2"
)

// func RunServer(server *config.Listener) (err error) {
// 	appfiber := fiber.New(fiber.Config{
// 		// Views: engine,
// 	})

// 	var corsconfig cors.Config
// 	if server.CORS != nil {
// 		if server.CORS.UnsafeAnyOrigin {
// 			log.Warnf("CORS UnsafeAnyOrigin is enabled")
// 			corsconfig.AllowOriginsFunc = func(origin string) bool {
// 				log.Warnf("Saw origin: %s", origin)
// 				return true
// 			}
// 		} else if len(server.CORS.AllowOrigins) > 0 {
// 			log.Debugf("CORS AllowOrigins: %s", server.CORS.AllowOrigins)
// 			corsconfig.AllowOrigins = server.CORS.AllowOrigins
// 		}
// 		if len(server.CORS.AllowMethods) > 0 {
// 			corsconfig.AllowMethods = server.CORS.AllowMethods
// 		}
// 		if len(server.CORS.AllowHeaders) > 0 {
// 			corsconfig.AllowHeaders = server.CORS.AllowHeaders
// 		}
// 		if server.CORS.AllowCredentials {
// 			log.Debugf("CORS AllowCredentials: %t", server.CORS.AllowCredentials)
// 			corsconfig.AllowCredentials = true
// 		}
// 		// if conf.CORS.AllowCredentials != nil {
// 		// 	corsconfig.AllowCredentials = *conf.CORS.AllowCredentials
// 		// }

// 		appfiber.Use("/", cors.New(corsconfig))
// 	}

// 	if !app.GetAppRuntimeOpts().NoLogVisits {
// 		appfiber.Use(fiberzerolog.New(fiberzerolog.Config{
// 			Logger: log.GetLogger(),
// 		}))
// 		// appfiber.Use(logger.New(
// 		// 	logger.Config{
// 		// 		Format: "${time} ${locals:requestid} ${status} - ${method} from ${ip} ${ua} ${url}​\n",
// 		// 	},
// 		// ))
// 	}
// }

type listenerErr struct {
	listener *config.Listener
	done     bool
	err      error
}

// singleConnListener is a net.Listener that serves exactly one connection
// then blocks forever on subsequent Accept calls. Used to feed a single
// TLS conn to an http.Server.
type singleConnListener struct {
	conn net.Conn
	once sync.Once
	ch   chan struct{}
}

func newSingleConnListener(c net.Conn) *singleConnListener {
	return &singleConnListener{conn: c, ch: make(chan struct{})}
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	var c net.Conn
	l.once.Do(func() { c = l.conn })
	if c != nil {
		return c, nil
	}
	// Block until Close is called
	<-l.ch
	return nil, net.ErrClosed
}

func (l *singleConnListener) Close() error {
	select {
	case <-l.ch:
	default:
		close(l.ch)
	}
	return nil
}

func (l *singleConnListener) Addr() net.Addr {
	return l.conn.LocalAddr()
}

// FiberListenWithListener starts a TLS server that dispatches based on ALPN:
//   - h2 connections go to a net/http handler (HTTP/2)
//   - http/1.1 connections go to the Fiber app (fasthttp)
func FiberListenWithListener(l *config.Listener, fiberapp *fiber.App, hulaCert *tls.Certificate, hulaNames map[string]bool) error {
	tlsHandler := &fiber.TLSHandler{}

	// Compute effective TLS version from all SSL configs on this listener.
	// The strictest (highest) min_version wins.
	var effectiveMin uint16 = tls.VersionTLS12
	var effectiveMax uint16
	for _, ssl := range l.SSL {
		if ssl != nil && ssl.TLS != nil {
			if v := ssl.TLS.GetMinVersion(); v > effectiveMin {
				effectiveMin = v
			}
			if v := ssl.TLS.GetMaxVersion(); v > 0 && (effectiveMax == 0 || v < effectiveMax) {
				effectiveMax = v
			}
		}
	}

	tlsCfg := &tls.Config{
		MinVersion:     effectiveMin,
		MaxVersion:     effectiveMax,
		Certificates:   []tls.Certificate{},
		GetCertificate: tlsHandler.GetClientInfo,
		NextProtos:     []string{"h2", "http/1.1"},
	}

	if effectiveMin > tls.VersionTLS12 || effectiveMax > 0 {
		log.Infof("TLS version constraints on %s: min=0x%04x max=0x%04x", l.GetListenOn(), effectiveMin, effectiveMax)
	}

	// add static certs (non-ACME)
	for _, cert := range l.SSL {
		if cert != nil && !cert.IsACME() && !cert.NoConfig() {
			tlsCfg.Certificates = append(tlsCfg.Certificates, *cert.GetTLSCert())
		}
	}

	// Set up SNI-based certificate selection with tiered routing
	var acmeMgr = l.ACMEManager
	staticCerts := tlsCfg.Certificates

	if acmeMgr != nil {
		tlsCfg.NextProtos = append(tlsCfg.NextProtos, "acme-tls/1")
		log.Infof("ACME: using autocert manager for TLS on %s", l.GetListenOn())
	}

	tlsCfg.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		sni := hello.ServerName

		// Tier 1: Hula's own identity (admin/localhost connections)
		if hulaNames[sni] || sni == "" {
			if hulaCert != nil {
				return hulaCert, nil
			}
		}

		// Tier 2: Virtual host ACME certs
		if acmeMgr != nil {
			cert, err := acmeMgr.GetCertificate(hello)
			if err == nil {
				return cert, nil
			}
			log.Debugf("ACME: no cert for SNI %q: %s", sni, err)
		}

		// Tier 3: Static virtual host certs
		if len(staticCerts) > 0 {
			return &staticCerts[0], nil
		}

		// Tier 4: Fallback to hula self-signed cert
		if hulaCert != nil {
			return hulaCert, nil
		}

		return nil, fmt.Errorf("no certificate available for %q", sni)
	}

	log.Debugf("Starting TLS server on port %s - has %d static certificates, ACME=%v", l.GetListenOn(), len(tlsCfg.Certificates), l.ACMEManager != nil)

	// Set up the HTTP/2 server with the h2 handler
	h2Handler := NewH2Handler(l)
	h2Server := &http.Server{
		Handler: h2Handler,
	}
	if err := http2.ConfigureServer(h2Server, &http2.Server{}); err != nil {
		return fmt.Errorf("failed to configure HTTP/2: %w", err)
	}

	ln, err := net.Listen("tcp", l.GetListenOn())
	if err != nil {
		return fmt.Errorf("failed to listen (net.Listen): %w", err)
	}
	tlsLn := tls.NewListener(ln, tlsCfg)
	log.Infof("TLS server listening on %s (h2 + http/1.1)", l.GetListenOn())

	// Accept loop: dispatch based on negotiated ALPN protocol
	for {
		conn, err := tlsLn.Accept()
		if err != nil {
			return fmt.Errorf("accept error: %w", err)
		}
		go func(c net.Conn) {
			// Pre-TLS bad actor check — block known bad IPs before wasting CPU on handshake
			if badactor.IsEnabled() {
				host, _, _ := net.SplitHostPort(c.RemoteAddr().String())
				if block, _ := badactor.GetStore().CheckKnownOnly(host); block {
					c.Close()
					return
				}
			}
			tlsConn, ok := c.(*tls.Conn)
			if !ok {
				c.Close()
				return
			}
			// Complete the TLS handshake
			if err := tlsConn.Handshake(); err != nil {
				log.Debugf("TLS handshake error: %s", err)
				tlsConn.Close()
				return
			}
			proto := tlsConn.ConnectionState().NegotiatedProtocol
			switch proto {
			case "h2":
				// HTTP/2: feed the conn to the net/http server
				scl := newSingleConnListener(tlsConn)
				h2Server.Serve(scl)
				scl.Close()
			default:
				// HTTP/1.1: serve via Fiber's fasthttp server
				fiberapp.Server().ServeConn(tlsConn)
			}
		}(conn)
	}
}

// Starts up a fiber server on a specific port / address and then adds all the routes in for that
// port / address that apply.
func RunListenerFiber(l *config.Listener, wg *sync.WaitGroup, errchan chan *listenerErr, hulaCert *tls.Certificate, hulaNames map[string]bool) (err error) {
	defer func() {
		errchan <- &listenerErr{
			listener: l,
			done:     true,
			err:      nil,
		}
		wg.Done()
	}()
	l.FiberApp = fiber.New(fiber.Config{
		// Views: engine,
	})

	var corsconfig cors.Config
	// if there is no CORS settings, then we need the default to allow
	// access to the hula server from each webserver - so that visitors tracking, etc. will work
	if l.CORS == nil {
		l.CORS = &config.CORSConfig{}
		l.CORS.AllowCredentials = true
	}
	// if l.CORS != nil {
	if l.CORS.UnsafeAnyOrigin {
		log.Warnf("CORS UnsafeAnyOrigin is enabled")
		corsconfig.AllowOriginsFunc = func(origin string) bool {
			log.Warnf("Saw origin: %s", origin)
			return true
		}
	} else if len(l.CORS.AllowOrigins) > 0 {
		corsconfig.AllowOrigins = l.CORS.AllowOrigins
		if !l.CORS.NoAddInHula {
			corsconfig.AllowOrigins = fmt.Sprintf("%s,%s", corsconfig.AllowOrigins, app.GetHulaOriginBaseUrl())
		}

	} else {
		if !l.CORS.NoAddInHula {
			alloworigin := app.GetHulaOriginBaseUrl()
			log.Debugf("CORS AllowOrigins using default (hula only): %s", alloworigin)
			corsconfig.AllowOrigins = alloworigin
		}
	}
	if len(l.CORS.AllowMethods) > 0 {
		corsconfig.AllowMethods = l.CORS.AllowMethods
	} else {
		corsconfig.AllowMethods = "GET, POST, HEAD, PUT, DELETE, PATCH, OPTIONS"
	}
	if len(l.CORS.AllowHeaders) > 0 {
		corsconfig.AllowHeaders = l.CORS.AllowHeaders
	}
	if !l.CORS.NoAddInHula || l.CORS.AllowCredentials {
		corsconfig.AllowCredentials = true

	}
	// if conf.CORS.AllowCredentials != nil {
	// 	corsconfig.AllowCredentials = *conf.CORS.AllowCredentials
	// }
	log.Debugf("CORS AllowOrigins: %s", corsconfig.AllowOrigins)
	log.Debugf("CORS AllowCredentials: %t", corsconfig.AllowCredentials)
	log.Debugf("CORS AllowMethods: %s", corsconfig.AllowMethods)

	// Bad actor check — must be first middleware
	if badactor.IsEnabled() {
		l.FiberApp.Use(func(c *fiber.Ctx) error {
			ba := badactor.GetStore()
			if block, _ := ba.CheckAndBlock(c.IP(), c.Get("User-Agent"), c.Method(), c.Path(), string(c.Request().URI().QueryString()), c.Hostname()); block {
				return nil // drop connection — no response
			}
			return c.Next()
		})
	}

	l.FiberApp.Use(cors.New(corsconfig))
	log.Debugf("CORS middleware enabled for listener %s", l.GetListenOn())
	// }

	l.FiberApp.Use(func(c *fiber.Ctx) error {
		ctx := handler.NewFiberCtx(c)
		hostconf, _, _, _ := handler.GetHostConfig(ctx)
		if hostconf != nil {
			handler.SetCSP(ctx, hostconf)
		}
		return c.Next()
	})

	if !app.GetAppRuntimeOpts().NoLogVisits {
		l.FiberApp.Use(fiberzerolog.New(fiberzerolog.Config{
			Logger: log.GetLogger(),
		}))
	}

	router.SetupRoutesFiber(l)
	// setup static serving
	for _, server := range l.GetServers() {
		if server.Root != "" {
			var duration time.Duration
			if len(server.RootCacheDuration) > 0 {
				duration, err = time.ParseDuration(server.RootCacheDuration)
				if err != nil {
					log.Warnf("Error parsing cache duration for root static folder for server entry %s: %s", server.Host, err.Error())
				}
			}
			l.FiberApp.Static("/", server.Root, fiber.Static{
				Compress:      server.RootCompress,
				ByteRange:     server.RootByteRange,
				Index:         server.RootIndex,
				MaxAge:        int(server.RootMaxAge),
				CacheDuration: duration,
				Next: func(c *fiber.Ctx) bool {
					ctx := handler.NewFiberCtx(c)
					hostconf, _, _, _ := handler.GetHostConfig(ctx)
					if hostconf != nil {
						if hostconf.Host == server.Host {
							return false
						}
					}
					return true
				},
			})
			log.Infof("Server %s: Serving static files from %s", server.Host, server.Root)
		}

		if server.NonRootStaticFolders != nil && len(server.NonRootStaticFolders) > 0 {
			for _, folder := range server.NonRootStaticFolders {
				var duration time.Duration
				if len(folder.CacheDuration) > 0 {
					duration, err = time.ParseDuration(folder.CacheDuration)
					if err != nil {
						log.Warnf("Error parsing cache duration for static folder %s for server entry %s: %s", folder.Root, server.Host, err.Error())
						errchan <- &listenerErr{
							listener: l,
							err:      err,
						}
					}
				}
				l.FiberApp.Static(folder.URLPrefix, folder.Root, fiber.Static{
					Compress:      folder.Compress,
					ByteRange:     folder.ByteRange,
					Index:         folder.Index,
					MaxAge:        int(folder.MaxAge),
					CacheDuration: duration,
				})
				log.Infof("Server %s: Serving static files from %s at URL prefix %s", server.Host, folder.Root, folder.URLPrefix)
			}
		}
	}

	handler.InitVisitorHandlers()
	// check for TLS

	if len(l.SSL) > 0 {
		log.Infof("Starting TLS server on port %s", l.GetListenOn())

		// If ACME is active, start HTTP-01 challenge handler on :80
		// This also redirects non-challenge HTTP traffic to HTTPS
		if l.ACMEManager != nil {
			acmeHTTPAddr := fmt.Sprintf(":%d", l.ACMEHTTPPort)
			go func() {
				httpHandler := l.ACMEManager.HTTPHandler(nil)
				log.Infof("ACME: starting HTTP-01 challenge handler on %s", acmeHTTPAddr)
				if httpErr := http.ListenAndServe(acmeHTTPAddr, httpHandler); httpErr != nil {
					log.Errorf("ACME: HTTP-01 challenge handler error: %s", httpErr.Error())
				}
			}()
		}

		err = FiberListenWithListener(l, l.FiberApp, hulaCert, hulaNames)
		if err != nil {
			log.Fatalf("Error listening (tls) on port %s: %s", l.GetListenOn(), err.Error())
			err = fmt.Errorf("error listening (tls) on port %s: %s", l.GetListenOn(), err.Error())
			errchan <- &listenerErr{
				listener: l,
				err:      err,
			}
		}
		return
	}
	// non ssl start:
	log.Infof("Starting server on port %s", l.GetListenOn())

	// blocks .. forever - some signals should unblock
	err = l.FiberApp.Listen(l.GetListenOn())
	if err != nil {
		log.Fatalf("Error listening on port %s: %s", l.GetListenOn(), err.Error())
		err = fmt.Errorf("error listening on port %s: %s", l.GetListenOn(), err.Error())
		errchan <- &listenerErr{
			listener: l,
			err:      err,
		}
	}
	return
}

// reloadConfig handles SIGHUP: re-reads the config file, hot-swaps cheap fields,
// re-inits components that support it, and warns about expensive changes that
// require a full restart.
func reloadConfig(oldConf *config.Config) {
	log.Infof("SIGHUP received — reloading config")

	_, err := app.ReloadConfig()
	if err != nil {
		log.Errorf("config reload failed: %s (keeping old config)", err)
		return
	}
	newConf := app.GetConfig()

	// --- Warn about expensive fields that require a full restart ---
	if oldConf.Port != newConf.Port {
		log.Warnf("reload: port changed (%d -> %d) — full restart required", oldConf.Port, newConf.Port)
	}
	if oldConf.ListenOn != newConf.ListenOn {
		log.Warnf("reload: listen_on changed — full restart required")
	}
	if oldConf.DBConfig != nil && newConf.DBConfig != nil {
		if oldConf.DBConfig.Host != newConf.DBConfig.Host ||
			oldConf.DBConfig.Port != newConf.DBConfig.Port ||
			oldConf.DBConfig.Username != newConf.DBConfig.Username ||
			oldConf.DBConfig.Password != newConf.DBConfig.Password ||
			oldConf.DBConfig.DBName != newConf.DBConfig.DBName {
			log.Warnf("reload: dbconfig changed — full restart required")
		}
	}
	if sslChanged(oldConf, newConf) {
		log.Warnf("reload: ssl/acme config changed — full restart required")
	}
	if serversChanged(oldConf, newConf) {
		log.Warnf("reload: servers config changed (hosts/aliases) — full restart required")
	}

	// --- Hot-swap cheap fields (already effective via app.GetConfig()) ---
	// admin.hash, admin.username, jwt_key, jwt_expiration — all read live

	// --- Re-apply log tag filters ---
	app.ApplyLogTagConfig()
	log.Infof("reload: log tag filters re-applied")

	// --- Re-init bad actor system ---
	if newConf.BadActors != nil && !newConf.BadActors.Disable {
		if err := badactor.Reinit(newConf.BadActors, model.GetDB(), newConf.Servers); err != nil {
			log.Errorf("reload: failed to reinit bad actor system: %s", err)
		} else {
			log.Infof("reload: bad actor system reloaded")
		}
	} else if badactor.IsEnabled() {
		// Was enabled, now disabled
		badactor.Shutdown()
		log.Infof("reload: bad actor system disabled")
	}

	log.Infof("config reload complete")
}

func sslChanged(old, new *config.Config) bool {
	if old.SSL == nil && new.SSL == nil {
		return false
	}
	if (old.SSL == nil) != (new.SSL == nil) {
		return true
	}
	if old.SSL.Cert != new.SSL.Cert || old.SSL.Key != new.SSL.Key {
		return true
	}
	if (old.SSL.ACME == nil) != (new.SSL.ACME == nil) {
		return true
	}
	if old.SSL.ACME != nil && new.SSL.ACME != nil {
		if old.SSL.ACME.Email != new.SSL.ACME.Email || old.SSL.ACME.CacheDir != new.SSL.ACME.CacheDir {
			return true
		}
	}
	return false
}

func serversChanged(old, new *config.Config) bool {
	if len(old.Servers) != len(new.Servers) {
		return true
	}
	for i := range old.Servers {
		if old.Servers[i].Host != new.Servers[i].Host {
			return true
		}
	}
	return false
}

// Runs the main server
func Run(conf *config.Config) (exitcode int) { // Initialize standard Go html template engine

	err := model.PreloadDefinedLanders(model.GetDB())
	if err != nil {
		fmt.Printf("Error preloading landers: %s", err.Error())
		return 1
	}

	err = model.PreloadDefinedForms(model.GetDB())
	if err != nil {
		fmt.Printf("Error preloading forms: %s", err.Error())
		return 1
	}

	// Initialize bad actor detection
	if conf.BadActors != nil && !conf.BadActors.Disable {
		if err := badactor.Init(conf.BadActors, model.GetDB(), conf.Servers); err != nil {
			log.Errorf("Failed to initialize bad actor detection: %s", err.Error())
			// Non-fatal — continue without bad actor protection
		}
	}

	// Start Docker backend containers if any server has them configured
	var backendMgr *backend.Manager
	hasBackends := false
	for _, s := range conf.Servers {
		if len(s.Backends) > 0 {
			hasBackends = true
			break
		}
	}
	if hasBackends {
		var berr error
		backendMgr, berr = backend.NewManager(conf.Registries)
		if berr != nil {
			log.Errorf("Failed to initialize Docker backend manager: %s", berr.Error())
			return 1
		}
		defer backendMgr.Close()

		startCtx, startCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		for _, s := range conf.Servers {
			if len(s.Backends) > 0 {
				if berr = backendMgr.StartBackendsForServer(startCtx, s.Host, s.Backends); berr != nil {
					log.Errorf("Failed to start backends for server %s: %s", s.Host, berr.Error())
					startCancel()
					return 1
				}
			}
		}
		startCancel()
	}

	// Initialize site deploy build manager if any server has git autodeploy configured
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
			// Non-fatal: site deployment won't work but server continues
		} else {
			sitedeploy.SetGlobalBuildManager(buildMgr)
			defer buildMgr.Close()
			log.Infof("Site deploy build manager initialized")

			// Pull and build any sites that need it on startup (sequentially)
			buildMgr.StartupBuildAll(conf.Servers)
		}
	}

	// Build hula identity names and resolve TLS cert for admin/internal connections
	hulaNames := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}
	if conf.HulaHost != "" && conf.HulaHost != "localhost" {
		hulaNames[conf.HulaHost] = true
	}

	var hulaCert *tls.Certificate
	if conf.HulaSSL != nil && !conf.HulaSSL.NoConfig() {
		if conf.HulaSSL.IsACME() {
			// ACME for hula handled in GetCertificate callback (hulaCert stays nil)
			log.Infof("hula TLS: using ACME for hula identity")
		} else {
			hulaCert = conf.HulaSSL.GetTLSCert()
			log.Infof("hula TLS: using configured cert for hula identity")
		}
	}
	if hulaCert == nil && (conf.HulaSSL == nil || !conf.HulaSSL.IsACME()) {
		hosts := make([]string, 0, len(hulaNames))
		for h := range hulaNames {
			hosts = append(hosts, h)
		}
		var cerr error
		hulaCert, cerr = GenerateSelfSignedCert(hosts)
		if cerr != nil {
			log.Errorf("Failed to generate hula self-signed cert: %s", cerr)
			return 1
		}
		log.Infof("hula TLS: generated self-signed cert for %v", hosts)
	}

	waitForServers := &sync.WaitGroup{}
	errchan := make(chan *listenerErr, 10)
	runcnt := 0
	for _, l := range conf.GetListeners() {
		log.Debugf("starting listener goroutine: %s", l.GetListenOn())
		waitForServers.Add(1)
		runcnt++
		go RunListenerFiber(l, waitForServers, errchan, hulaCert, hulaNames)
	}

	// Signal handling for graceful shutdown and config reload
	sigchan := make(chan os.Signal, 1)
	signal.Notify(sigchan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

mainLoop:
	for {
		select {
		case sig := <-sigchan:
			if sig == syscall.SIGHUP {
				reloadConfig(conf)
				conf = app.GetConfig()
				continue
			}
			log.Infof("Received signal %s, shutting down", sig)
			badactor.Shutdown()
			if bm := sitedeploy.GetBuildManager(); bm != nil {
				bm.Close()
			}
			if backendMgr != nil {
				shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
				backendMgr.StopAll(shutdownCtx)
				shutdownCancel()
			}
			// Shutdown all fiber apps
			for _, l := range conf.GetListeners() {
				if l.FiberApp != nil {
					l.FiberApp.Shutdown()
				}
			}
			break mainLoop
		case lerr := <-errchan:
			if lerr.done {
				log.Debugf("Listener %s is done", lerr.listener.GetListenOn())
				runcnt--
				if runcnt < 1 {
					log.Debugf("All listeners are done")
					break mainLoop
				}
			} else {
				log.Errorf("Error on listener %s: %s", lerr.listener.GetListenOn(), lerr.err.Error())
				exitcode = 1
				// Stop backends before exiting
				if backendMgr != nil {
					shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
					backendMgr.StopAll(shutdownCtx)
					shutdownCancel()
				}
				return
			}
		}
	}

	log.Debugf("waiting for servers to finish")
	waitForServers.Wait()
	// // TODO: start a gothread for each fiber server

	// // engine := html.New("./scripts", ".js")
	// // if err := engine.Load(); err != nil {
	// // 	log.Fatalf("Error loading templates: %s", err)
	// // 	return 2
	// // }
	// // if app.GetAppDebugLevel() > 1 {
	// // 	engine.Reload(true)
	// // 	engine.Debug(true)
	// // }
	// appfiber := fiber.New(fiber.Config{
	// 	// Views: engine,
	// })

	// // Interesting - but we aren't using it:
	// // claims := payload if {
	// // 	# Verify the signature on the Bearer token. In this example the secret is
	// // 	# hardcoded into the policy however it could also be loaded via data or
	// // 	# an environment variable. Environment variables can be accessed using
	// // 	# the opa.runtime() built-in function.
	// // 	io.jwt.verify_hs256(bearer_token, "%s")

	// // 	# This statement invokes the built-in function io.jwt.decode passing the
	// // 	# parsed bearer_token as a parameter. The io.jwt.decode function returns an
	// // 	# array:
	// // 	#
	// // 	#	[header, payload, signature]
	// // 	#
	// // 	# In Rego, you can pattern match values using the = and := operators. This
	// // 	# example pattern matches on the result to obtain the JWT payload.
	// // 	[_, payload, _] := io.jwt.decode(bearer_token)
	// // }

	// // bearer_token := t if {
	// // 	# Bearer tokens are contained inside of the HTTP Authorization header. This rule
	// // 	# parses the header and extracts the Bearer token value. If no Bearer token is
	// // 	# provided, the bearer_token value is undefined.
	// // 	v := input.attributes.request.http.headers.authorization
	// // 	startswith(v, "Bearer ")
	// // 	t := substring(v, count("Bearer "), -1)
	// // }

	// var corsconfig cors.Config
	// if conf.CORS != nil {
	// 	if conf.CORS.UnsafeAnyOrigin {
	// 		log.Warnf("CORS UnsafeAnyOrigin is enabled")
	// 		corsconfig.AllowOriginsFunc = func(origin string) bool {
	// 			log.Warnf("Saw origin: %s", origin)
	// 			return true
	// 		}
	// 	} else if len(conf.CORS.AllowOrigins) > 0 {
	// 		log.Debugf("CORS AllowOrigins: %s", conf.CORS.AllowOrigins)
	// 		corsconfig.AllowOrigins = conf.CORS.AllowOrigins
	// 	}
	// 	if len(conf.CORS.AllowMethods) > 0 {
	// 		corsconfig.AllowMethods = conf.CORS.AllowMethods
	// 	}
	// 	if len(conf.CORS.AllowHeaders) > 0 {
	// 		corsconfig.AllowHeaders = conf.CORS.AllowHeaders
	// 	}
	// 	if conf.CORS.AllowCredentials {
	// 		log.Debugf("CORS AllowCredentials: %t", conf.CORS.AllowCredentials)
	// 		corsconfig.AllowCredentials = true
	// 	}
	// 	// if conf.CORS.AllowCredentials != nil {
	// 	// 	corsconfig.AllowCredentials = *conf.CORS.AllowCredentials
	// 	// }

	// 	appfiber.Use("/", cors.New(corsconfig))
	// }

	// if !app.GetAppRuntimeOpts().NoLogVisits {
	// 	appfiber.Use(fiberzerolog.New(fiberzerolog.Config{
	// 		Logger: log.GetLogger(),
	// 	}))
	// 	// appfiber.Use(logger.New(
	// 	// 	logger.Config{
	// 	// 		Format: "${time} ${locals:requestid} ${status} - ${method} from ${ip} ${ua} ${url}​\n",
	// 	// 	},
	// 	// ))
	// }

	// //	store.ConnectDB()
	// router.SetupRoutesFiber(appfiber)
	// // setup static serving
	// var err error
	// for _, server := range conf.Servers {
	// 	if server.Root != "" {
	// 		var duration time.Duration
	// 		if len(server.RootCacheDuration) > 0 {
	// 			duration, err = time.ParseDuration(server.RootCacheDuration)
	// 			if err != nil {
	// 				log.Warnf("Error parsing cache duration for root static folder for server entry %s: %s", server.Host, err.Error())
	// 			}
	// 		}
	// 		appfiber.Static("/", server.Root, fiber.Static{
	// 			Compress:      server.RootCompress,
	// 			ByteRange:     server.RootByteRange,
	// 			Index:         server.RootIndex,
	// 			MaxAge:        int(server.RootMaxAge),
	// 			CacheDuration: duration,
	// 		})
	// 		log.Infof("Server %s: Serving static files from %s", server.Host, server.Root)
	// 	}

	// 	if server.NonRootStaticFolders != nil && len(server.NonRootStaticFolders) > 0 {
	// 		for _, folder := range server.NonRootStaticFolders {
	// 			var duration time.Duration
	// 			if len(folder.CacheDuration) > 0 {
	// 				duration, err = time.ParseDuration(folder.CacheDuration)
	// 				if err != nil {
	// 					log.Warnf("Error parsing cache duration for static folder %s for server entry %s: %s", folder.Root, server.Host, err.Error())
	// 				}
	// 			}
	// 			appfiber.Static(folder.URLPrefix, folder.Root, fiber.Static{
	// 				Compress:      folder.Compress,
	// 				ByteRange:     folder.ByteRange,
	// 				Index:         folder.Index,
	// 				MaxAge:        int(folder.MaxAge),
	// 				CacheDuration: duration,
	// 			})
	// 			log.Infof("Server %s: Serving static files from %s at URL prefix %s", server.Host, folder.Root, folder.URLPrefix)
	// 		}
	// 	}
	// }

	// // check for TLS

	// if conf.SSL != nil && conf.SSL.GetTLSCert() != nil {
	// 	log.Infof("Starting TLS server on port %d", conf.Port)
	// 	err := appfiber.ListenTLSWithCertificate(fmt.Sprintf(":%d", conf.Port), *conf.SSL.GetTLSCert())
	// 	if err != nil {
	// 		log.Fatalf("Error listening on port %d: %s", conf.Port, err.Error())
	// 		return 1
	// 	}
	// 	return 0
	// }
	// log.Infof("Starting server on port %d", conf.Port)
	// handler.InitVistorHandlers()
	// err = appfiber.Listen(fmt.Sprintf(":%d", conf.Port))
	// if err != nil {
	// 	log.Fatalf("Error listening on port %d: %s", conf.Port, err.Error())
	// 	return 1
	// }
	// return 0
	return
}
