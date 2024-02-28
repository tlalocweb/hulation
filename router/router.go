package router

import (
	"bytes"
	"fmt"
	"net/http"

	"github.com/tlalocweb/hulation/handler"
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

// SetupRoutes setup router api
func SetupRoutes(app *fiber.App) {

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

	// NOTE: login is not protected by OPA
	app.Post("/api/auth/login", handler.Login)                 // logger.New(),
	api := app.Group("/api", middleware.NewOpaMiddleware(cfg)) // logger.New(),
	// Middleware
	//	api.Use("/api",

	// visitor API do not need auth
	visitor := app.Group(hulation.GetConfig().VisitorPrefix)
	visitor.Post("/hello", handler.Hello)
	visitor.Get("/"+hulation.GetConfig().PublishedIFrameHelloFileName, handler.HelloIframe)
	visitor.Get("/"+hulation.GetConfig().PublishedIFrameNoScriptFilename, handler.HelloNoScript)
	// submit form as visitor
	visitor.Post("/sub/:formid", handler.FormSubmit)
	// handle landing as visitor
	//	log.Debugf("LanderPath: %s", hulation.GetConfig().LanderPath)
	visitor.Get(fmt.Sprintf("%s/:landerid", hulation.GetConfig().LanderPath), handler.DoLanding)
	// nor do script downloads
	app.Get("/scripts/"+hulation.GetConfig().PublishedHelloScriptFilename, handler.HelloScriptFile)
	app.Get("/scripts/"+hulation.GetConfig().PublishedFormsScriptFilename, handler.FormsScriptFile)

	api.Get("/status", handler.Status)
	// Auth
	auth := api.Group("/auth")
	auth.Post("/logout", handler.Logout)
	auth.Post("/user", handler.NewUser)
	auth.Get("/user/:userlookup", handler.GetUser)
	auth.Patch("/user/:userid", handler.ModifyUser)
	// TODO
	//auth.Delete("/user/:userid", handler.DeleteUser)
	auth.Get("/ok", handler.StatusAuthOK)

	form := api.Group("/form")
	// order is important - the most generic path :/formid - must be at the end
	form.Post("/create", handler.FormCreate)
	// TODO apparently DELETE is sometimes blocked by proxies
	// so we should provide an alternate later
	form.Delete("/:formid", handler.FormDelete)
	form.Patch("/:formid", handler.FormModify)

	lander := api.Group("/lander")
	lander.Post("/create", handler.LanderCreate)
	lander.Delete("/:landerid", handler.LanderDelete)
	lander.Patch("/:landerid", handler.LanderModify)

}
