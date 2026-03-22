package server

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gofiber/contrib/fiberzerolog"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/config"
	handler "github.com/tlalocweb/hulation/fiberhandler"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
	"github.com/tlalocweb/hulation/router"
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

// a blocking call which is a custom subsistute for fiber.ListenTLSWithCertificate()
func FiberListenWithListener(l *config.Listener, fiberapp *fiber.App) error {
	tlsHandler := &fiber.TLSHandler{}
	tlsCfg := &tls.Config{
		MinVersion:     tls.VersionTLS12,
		Certificates:   []tls.Certificate{},
		GetCertificate: tlsHandler.GetClientInfo,
	}

	// add static certs (non-ACME)
	for _, cert := range l.SSL {
		if cert != nil && !cert.IsACME() && !cert.NoConfig() {
			tlsCfg.Certificates = append(tlsCfg.Certificates, *cert.GetTLSCert())
		}
	}

	// if ACME is configured, use autocert manager for certificate retrieval
	if l.ACMEManager != nil {
		tlsCfg.GetCertificate = l.ACMEManager.GetCertificate
		tlsCfg.NextProtos = append(tlsCfg.NextProtos, "h2", "http/1.1", "acme-tls/1")
		log.Infof("ACME: using autocert manager for TLS on %s", l.GetListenOn())
	}

	log.Debugf("Starting TLS server on port %s - has %d static certificates, ACME=%v", l.GetListenOn(), len(tlsCfg.Certificates), l.ACMEManager != nil)

	ln, err := net.Listen("tcp", l.GetListenOn())
	if err != nil {
		return fmt.Errorf("failed to listen (net.Listen): %w", err)
	}
	ln = tls.NewListener(ln, tlsCfg)
	err = fiberapp.Listener(ln)
	return err
}

// Starts up a fiber server on a specific port / address and then adds all the routes in for that
// port / address that apply.
func RunListenerFiber(l *config.Listener, wg *sync.WaitGroup, errchan chan *listenerErr) (err error) {
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

	l.FiberApp.Use(cors.New(corsconfig))
	log.Debugf("CORS middleware enabled for listener %s", l.GetListenOn())
	// }

	l.FiberApp.Use(func(c *fiber.Ctx) error {
		hostconf, _, _, _ := handler.GetHostConfig(c)
		if hostconf != nil {
			handler.SetCSP(c, hostconf)
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
					hostconf, _, _, _ := handler.GetHostConfig(c)
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

	handler.InitVistorHandlers()
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

		err = FiberListenWithListener(l, l.FiberApp)
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

	waitForServers := &sync.WaitGroup{}
	errchan := make(chan *listenerErr, 10)
	runcnt := 0
	for _, l := range conf.GetListeners() {
		log.Debugf("starting listener goroutine: %s", l.GetListenOn())
		waitForServers.Add(1)
		runcnt++
		go RunListenerFiber(l, waitForServers, errchan)
	}

mainLoop:
	for {
		select {
		// TODO - handle signals here
		case err := <-errchan:
			if err.done {
				log.Debugf("Listener %s is done", err.listener.GetListenOn())
				runcnt--
				if runcnt < 1 {
					log.Debugf("All listeners are done")
					break mainLoop
				}
			} else {
				log.Errorf("Error on listener %s: %s", err.listener.GetListenOn(), err.err.Error())
				exitcode = 1
				return
				//break mainLoop
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
