package middleware

import (
	"github.com/gofiber/contrib/fiberzerolog"
	"github.com/gofiber/fiber/v3"
	"github.com/tlalocweb/hulation/log"
)

// SetupLogging setup all middleware
func SetupLogging(app *fiber.App) {
	app.Use(fiberzerolog.New(fiberzerolog.Config{
		Logger: log.GetLogger(),
	}))
}
