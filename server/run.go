package server

import (
	"fmt"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/handler"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/router"
)

func Run(conf *config.Config) (exitcode int) { // Initialize standard Go html template engine

	// engine := html.New("./scripts", ".js")
	// if err := engine.Load(); err != nil {
	// 	log.Fatalf("Error loading templates: %s", err)
	// 	return 2
	// }
	// if app.GetAppDebugLevel() > 1 {
	// 	engine.Reload(true)
	// 	engine.Debug(true)
	// }
	appfiber := fiber.New(fiber.Config{
		// Views: engine,
	})
	var corsconfig cors.Config
	if conf.CORS != nil {
		if conf.CORS.UnsafeAnyOrigin {
			log.Warnf("CORS UnsafeAnyOrigin is enabled")
			corsconfig.AllowOriginsFunc = func(origin string) bool {
				log.Warnf("Saw origin: %s", origin)
				return true
			}
		} else if len(conf.CORS.AllowOrigins) > 0 {
			log.Debugf("CORS AllowOrigins: %s", conf.CORS.AllowOrigins)
			corsconfig.AllowOrigins = conf.CORS.AllowOrigins
		}
		if len(conf.CORS.AllowMethods) > 0 {
			corsconfig.AllowMethods = conf.CORS.AllowMethods
		}
		if len(conf.CORS.AllowHeaders) > 0 {
			corsconfig.AllowHeaders = conf.CORS.AllowHeaders
		}
		if conf.CORS.AllowCredentials {
			log.Debugf("CORS AllowCredentials: %t", conf.CORS.AllowCredentials)
			corsconfig.AllowCredentials = true
		}
		// if conf.CORS.AllowCredentials != nil {
		// 	corsconfig.AllowCredentials = *conf.CORS.AllowCredentials
		// }

		appfiber.Use("/", cors.New(corsconfig))
	}

	if app.GetAppRuntimeOpts().LogAllVisits {
		appfiber.Use(logger.New(
			logger.Config{
				Format: "${time} ${locals:requestid} ${status} - ${method} from ${ip} ${ua} ${url}​\n",
			},
		))
	}

	//	store.ConnectDB()

	router.SetupRoutes(appfiber)

	// check for TLS

	if conf.SSL != nil && conf.SSL.GetTLSCert() != nil {
		log.Infof("Starting TLS server on port %d", conf.Port)
		err := appfiber.ListenTLSWithCertificate(fmt.Sprintf(":%d", conf.Port), *conf.SSL.GetTLSCert())
		if err != nil {
			log.Fatalf("Error listening on port %d: %s", conf.Port, err.Error())
			return 1
		}
		return 0
	}
	log.Infof("Starting server on port %d", conf.Port)
	handler.InitVistorHandlers()
	err := appfiber.Listen(fmt.Sprintf(":%d", conf.Port))
	if err != nil {
		log.Fatalf("Error listening on port %d: %s", conf.Port, err.Error())
		return 1
	}
	return 0
}
