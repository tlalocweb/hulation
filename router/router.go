package router

import (
	"bytes"
	"fmt"
	"net/http"

	"github.com/tlalocweb/hulation/backend"
	"github.com/tlalocweb/hulation/config"
	fhandler "github.com/tlalocweb/hulation/fiberhandler"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/middleware"
	"github.com/tlalocweb/hulation/model"

	"github.com/gofiber/fiber/v2"
	hulation "github.com/tlalocweb/hulation/app"
)

const opaModule = `
package hulation.authz

import rego.v1

default allow := false

#	allow {
#		input.method == "GET"
#	}
# always allow the root user
#	allow if {
#		input.userid == input.rootname
#	}
allow if {
	some cap in input.attrs
	cap == "admin"
}
`

// SetupRoutesFiber setup router api
func SetupRoutesFiber(l *config.Listener) {

	cfg := middleware.OpaConfig{
		RegoQuery:             "data.hulation.authz.allow",
		RegoPolicy:            bytes.NewBufferString(opaModule),
		IncludeQueryString:    true,
		DeniedStatusCode:      fiber.StatusForbidden,
		DeniedResponseMessage: "status forbidden",
		IncludeHeaders:        []string{"Authorization"},
		InputCreationMethod: func(ctx *fiber.Ctx) (map[string]interface{}, int, string, error) {
			log.Debugf("In input creation method")
			ahdr := ctx.Get("Authorization")
			var token string
			n, err := fmt.Sscanf(ahdr, "Bearer %s", &token)
			if err != nil {
				log.Debugf("error parsing token: %s", err.Error())
				log.Tracef("token was: %s", token)
				return nil, http.StatusUnauthorized, "error parsing token", fmt.Errorf("error parsing token: %w", err)
			}
			if n < 1 {
				log.Debugf("no token")
				return nil, http.StatusUnauthorized, "no token", fmt.Errorf("no token")
			}

			// lookup token here
			ok, perms, err := model.VerifyJWTClaims(model.GetDB(), token)
			if err != nil {
				log.Debugf("error verifying token: %s", err.Error())
				return nil, http.StatusUnauthorized, "error verifying token", fmt.Errorf("error verifying token: %w", err)
			}
			if !ok {
				log.Debugf("token not valid")
				return nil, http.StatusUnauthorized, "token not valid", fmt.Errorf("token not valid")
			}
			ctx.Locals("jwt", token)
			ctx.Locals("perms", perms)
			// then add capabilities to the map we pass to OPA

			// lookup token here
			return map[string]interface{}{
				"method":   ctx.Method(),
				"path":     ctx.Path(),
				"jwt":      token,
				"jwtkey":   hulation.GetConfig().JWTKey,
				"rootname": hulation.GetConfig().Admin.Username,
				"userid":   perms.UserID,
				"attrs":    perms.ListCaps(), // this is the input.attributes
				"ip":       ctx.IP(),
			}, 0, "", nil
		},
	}

	// l.FiberApp.Use(func(c *fiber.Ctx) error {
	// 	hostconf, _, _, _ := fhandler.GetHostConfig(c)
	// 	if hostconf != nil {

	// 	}
	// 	//		c.Locals("hostconf", l)
	// 	return c.Next()
	// })

	// TODO - use middleware to look at host header
	// and then determine if routes apply

	var api fiber.Router
	// NOTE: login is not protected by OPA
	if l.IsHulaCore() {
		l.FiberApp.Post("/api/auth/login", fhandler.Login)               // logger.New(),
		l.FiberApp.Get("/hulastatus", fhandler.Status)                   // logger.New(),
		api = l.FiberApp.Group("/api", middleware.NewOpaMiddleware(cfg)) // logger.New(),
		api.Get("/status", fhandler.Status)
		// Auth
		auth := api.Group("/auth")
		auth.Post("/logout", fhandler.Logout)
		auth.Post("/user", fhandler.NewUser)
		auth.Get("/user/:userlookup", fhandler.GetUser)
		auth.Patch("/user/:userid", fhandler.ModifyUser)
		// TODO
		//auth.Delete("/user/:userid", fhandler.DeleteUser)
		auth.Get("/ok", fhandler.StatusAuthOK)

		form := api.Group("/form")
		// order is important - the most generic path :/formid - must be at the end
		form.Post("/create", fhandler.FormCreate)
		// TODO apparently DELETE is sometimes blocked by proxies
		// so we should provide an alternate later
		form.Delete("/:formid", fhandler.FormDelete)
		form.Patch("/:formid", fhandler.FormModify)

		lander := api.Group("/lander")
		lander.Post("/create", fhandler.LanderCreate)
		lander.Delete("/:landerid", fhandler.LanderDelete)
		lander.Patch("/:landerid", fhandler.LanderModify)
	}
	// Middleware
	//	api.Use("/api",

	// visitor API do not need auth
	visitor := l.FiberApp.Group(hulation.GetConfig().VisitorPrefix)
	visitor.Post("/hello", fhandler.Hello)
	visitor.Get("/"+hulation.GetConfig().PublishedIFrameHelloFileName, fhandler.HelloIframe)
	visitor.Get("/"+hulation.GetConfig().PublishedIFrameNoScriptFilename, fhandler.HelloNoScript)
	// submit form as visitor
	visitor.Post("/sub/:formid", fhandler.FormSubmit)
	// handle landing as visitor
	//	log.Debugf("LanderPath: %s", hulation.GetConfig().LanderPath)
	visitor.Get(fmt.Sprintf("%s/:landerid", hulation.GetConfig().LanderPath), fhandler.DoLanding)
	// nor do script downloads
	l.FiberApp.Get("/scripts/"+hulation.GetConfig().PublishedHelloScriptFilename, fhandler.HelloScriptFile)
	l.FiberApp.Get("/scripts/"+hulation.GetConfig().PublishedFormsScriptFilename, fhandler.FormsScriptFile)

	// Register backend proxy routes for each server's backends
	for _, server := range l.GetServers() {
		if len(server.Backends) == 0 {
			continue
		}
		for _, b := range server.Backends {
			if !b.IsReady() {
				log.Warnf("Backend %s for server %s is not ready, skipping proxy route", b.ContainerName, server.Host)
				continue
			}
			proxyHandler := backend.NewProxyHandler(b)
			// Capture loop variables for the closure
			serverHost := server.Host
			backendName := b.ContainerName
			virtualPath := b.VirtualPath

			// Register a catch-all route for this backend's virtual path.
			// The host-check inside the handler ensures isolation between virtual servers.
			l.FiberApp.All(virtualPath+"/*", func(c *fiber.Ctx) error {
				hostconf, _, _, _ := fhandler.GetHostConfig(c)
				if hostconf == nil || hostconf.Host != serverHost {
					return c.Next() // not this server, skip to next handler
				}
				return proxyHandler.Handle(c)
			})
			// Also handle exact path match (e.g. /api without trailing slash)
			l.FiberApp.All(virtualPath, func(c *fiber.Ctx) error {
				hostconf, _, _, _ := fhandler.GetHostConfig(c)
				if hostconf == nil || hostconf.Host != serverHost {
					return c.Next()
				}
				return proxyHandler.Handle(c)
			})

			log.Infof("Server %s: proxying %s -> %s (container: %s)",
				serverHost, virtualPath, b.GetProxyTarget(), backendName)
		}
	}
}
