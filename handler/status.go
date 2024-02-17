package handler

import (
	"github.com/gofiber/fiber/v3"
)

func StatusOKAuth(c fiber.Ctx) error {

	hostconf, _, httperr, err := GetHostConfig(c)
	if err != nil {
		return c.Status(httperr).SendString(err.Error())
	}
	id := c.Query("h")
	if id != hostconf.ID {
		return c.Status(400).SendString("host id mismmatch")
	}

	// respond ok
	return c.Status(200).SendString("ok")
}
