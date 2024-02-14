package router

import (
	"github.com/tlalocweb/hulation/handler"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
	hulation "github.com/tlalocweb/hulation/app"
)

// SetupRoutes setup router api
func SetupRoutes(app *fiber.App) {
	// Middleware
	api := app.Group("/api", logger.New())
	api.Get("/status", handler.Status)
	api.Get("/ok", handler.StatusOK)

	// Auth
	auth := app.Group("/auth")
	auth.Post("/login", handler.Login)
	auth.Get("/ok", handler.StatusOKAuth)

	app.Get("/scripts/"+hulation.GetConfig().PublishedHelloScriptFilename, handler.HelloScriptFile)
	app.Get("/scripts/"+hulation.GetConfig().PublishedFormsScriptFilename, handler.FormsScriptFile)
	visitor := app.Group("/v")
	visitor.Post("/hello", handler.Hello)
	visitor.Get("/"+hulation.GetConfig().PublishedIFrameHelloFileName, handler.HelloIframe)
	visitor.Get("/"+hulation.GetConfig().PublishedIFrameNoScriptFilename, handler.HelloNoScript)

	form := api.Group("/form")
	form.Post("/:formid", handler.FormSubmit)
	form.Post("/create", handler.FormCreate)
	form.Put("/modify/:formid", handler.FormModify)

	// Products
	// product := api.Group("/product")
	// product.Get("/", handler.GetAllProducts)
	// product.Get("/:id", handler.GetProduct)
	// product.Post("/", middleware.Protected(), handler.CreateProduct)
	// product.Delete("/:id", middleware.Protected(), handler.DeleteProduct)
}
