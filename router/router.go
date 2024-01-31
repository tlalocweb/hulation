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

	// Auth
	auth := api.Group("/auth")
	auth.Post("/login", handler.Login)

	app.Get("/scripts/"+hulation.GetConfig().PublishedScriptFilename, handler.ScriptFile)

	visitor := app.Group("/v")
	visitor.Post("/hello", handler.Hello)
	visitor.Get("/"+hulation.GetConfig().PublishedIFrameHelloFileName, handler.HelloIframe)
	visitor.Get("/"+hulation.GetConfig().PublishedIFrameNoScriptFilename, handler.HelloNoScript)

	// Products
	// product := api.Group("/product")
	// product.Get("/", handler.GetAllProducts)
	// product.Get("/:id", handler.GetProduct)
	// product.Post("/", middleware.Protected(), handler.CreateProduct)
	// product.Delete("/:id", middleware.Protected(), handler.DeleteProduct)
}
