package router

import (
	"bytes"
	"fmt"
	"net/http"

	"github.com/tlalocweb/hulation/backend"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/handler"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"

	"github.com/gofiber/fiber/v2"
	hulation "github.com/tlalocweb/hulation/app"
)

const opaModule = `
package hulation.authz

import rego.v1

default allow := false

allow if {
	some cap in input.attrs
	cap == "admin"
}
`

// opaConfig returns the unified OPA middleware for admin API protection.
func opaMiddleware() handler.Middleware {
	return handler.NewOpaMiddleware(handler.OpaConfig{
		RegoQuery:             "data.hulation.authz.allow",
		RegoPolicy:            bytes.NewBufferString(opaModule),
		IncludeQueryString:    true,
		DeniedStatusCode:      http.StatusForbidden,
		DeniedResponseMessage: "status forbidden",
		IncludeHeaders:        []string{"Authorization"},
		InputCreationMethod: func(ctx handler.RequestCtx) (map[string]interface{}, int, string, error) {
			log.Debugf("In input creation method")
			ahdr := ctx.Header("Authorization")
			var token string
			n, err := fmt.Sscanf(ahdr, "Bearer %s", &token)
			if err != nil {
				log.Debugf("error parsing token: %s", err.Error())
				return nil, http.StatusUnauthorized, "error parsing token", fmt.Errorf("error parsing token: %w", err)
			}
			if n < 1 {
				log.Debugf("no token")
				return nil, http.StatusUnauthorized, "no token", fmt.Errorf("no token")
			}

			ok, perms, err := model.VerifyJWTClaims(model.GetDB(), token)
			if err != nil {
				log.Debugf("error verifying token: %s", err.Error())
				return nil, http.StatusUnauthorized, "error verifying token", fmt.Errorf("error verifying token: %w", err)
			}
			if !ok {
				log.Debugf("token not valid")
				return nil, http.StatusUnauthorized, "token not valid", fmt.Errorf("token not valid")
			}
			ctx.SetLocals("jwt", token)
			ctx.SetLocals("perms", perms)

			return map[string]interface{}{
				"method":   ctx.Method(),
				"path":     ctx.Path(),
				"jwt":      token,
				"jwtkey":   hulation.GetConfig().JWTKey,
				"rootname": hulation.GetConfig().Admin.Username,
				"userid":   perms.UserID,
				"attrs":    perms.ListCaps(),
				"ip":       ctx.IP(),
			}, 0, "", nil
		},
	})
}

// wrapWithOpa wraps a handler with the OPA middleware for Fiber registration.
func wrapWithOpa(opa handler.Middleware, h handler.Handler) fiber.Handler {
	return handler.WrapForFiber(opa(h))
}

// SetupRoutesFiber sets up all routes on the Fiber app using unified handlers.
func SetupRoutesFiber(l *config.Listener) {

	if l.IsHulaCore() {
		opa := opaMiddleware()

		// Login is NOT protected by OPA
		l.FiberApp.Post("/api/auth/login", handler.WrapForFiber(handler.Login))
		l.FiberApp.Get("/hulastatus", handler.WrapForFiber(handler.Status))

		// Protected API routes
		api := l.FiberApp.Group("/api")
		api.Get("/status", wrapWithOpa(opa, handler.Status))

		// Auth
		auth := api.Group("/auth")
		auth.Post("/logout", wrapWithOpa(opa, handler.Logout))
		auth.Post("/user", wrapWithOpa(opa, handler.NewUser))
		auth.Get("/user/:userlookup", wrapWithOpa(opa, handler.GetUser))
		auth.Patch("/user/:userid", wrapWithOpa(opa, handler.ModifyUser))
		auth.Get("/ok", wrapWithOpa(opa, handler.StatusAuthOK))

		// Forms
		form := api.Group("/form")
		form.Post("/create", wrapWithOpa(opa, handler.FormCreate))
		form.Delete("/:formid", wrapWithOpa(opa, handler.FormDelete))
		form.Patch("/:formid", wrapWithOpa(opa, handler.FormModify))

		// Landers
		lander := api.Group("/lander")
		lander.Post("/create", wrapWithOpa(opa, handler.LanderCreate))
		lander.Delete("/:landerid", wrapWithOpa(opa, handler.LanderDelete))
		lander.Patch("/:landerid", wrapWithOpa(opa, handler.LanderModify))
	}

	// Visitor API — no auth required
	visitor := l.FiberApp.Group(hulation.GetConfig().VisitorPrefix)
	visitor.Post("/hello", handler.WrapForFiber(handler.Hello))
	visitor.Get("/"+hulation.GetConfig().PublishedIFrameHelloFileName, handler.WrapForFiber(handler.HelloIframe))
	visitor.Get("/"+hulation.GetConfig().PublishedIFrameNoScriptFilename, handler.WrapForFiber(handler.HelloNoScript))
	visitor.Post("/sub/:formid", handler.WrapForFiber(handler.FormSubmit))
	visitor.Get(fmt.Sprintf("%s/:landerid", hulation.GetConfig().LanderPath), handler.WrapForFiber(handler.DoLanding))

	// Script downloads
	l.FiberApp.Get("/scripts/"+hulation.GetConfig().PublishedHelloScriptFilename, handler.WrapForFiber(handler.HelloScriptFile))
	l.FiberApp.Get("/scripts/"+hulation.GetConfig().PublishedFormsScriptFilename, handler.WrapForFiber(handler.FormsScriptFile))

	// Backend proxy routes
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
			serverHost := server.Host
			backendName := b.ContainerName
			virtualPath := b.VirtualPath

			l.FiberApp.All(virtualPath+"/*", func(c *fiber.Ctx) error {
				ctx := handler.NewFiberCtx(c)
				hostconf, _, _, _ := handler.GetHostConfig(ctx)
				if hostconf == nil || hostconf.Host != serverHost {
					return c.Next()
				}
				return proxyHandler.Handle(c)
			})
			l.FiberApp.All(virtualPath, func(c *fiber.Ctx) error {
				ctx := handler.NewFiberCtx(c)
				hostconf, _, _, _ := handler.GetHostConfig(ctx)
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
