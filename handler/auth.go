package handler

import (
	"github.com/gofiber/fiber/v2"
	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
	"github.com/tlalocweb/hulation/utils"
)

type LoginInput struct {
	Userid       string `json:"userid"`
	PasswordHash string `json:"hash"`
}

// Login get user and password
func Login(c *fiber.Ctx) error {
	var input LoginInput

	log.Debugf("Body: %s", string(c.Body()))
	if err := c.BodyParser(&input); err != nil {
		c.SendString("bad parse: " + err.Error())
		return c.SendStatus(fiber.StatusUnauthorized)
	}
	// input.Userid
	// input.PasswordHash
	var authok bool
	var isadmin bool
	// do Login
	// if this is the special root user then:
	if input.Userid == app.GetConfig().Admin.Username {
		// check hash
		match, err := utils.Argon2CompareHashAndSecret(input.PasswordHash, app.GetConfig().Admin.Hash)
		if err != nil {
			c.SendString("has function error: " + err.Error())
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		if match {
			authok = true
			isadmin = true
		}
	} else {

		// check db
		// TODO
	}

	if !authok {
		return c.SendStatus(fiber.StatusUnauthorized)
	}

	jwt, err2 := model.NewJWTClaimsCommit(model.GetDB(), input.Userid, &model.LoginOpts{
		IsAdmin: isadmin,
	})
	if err2 != nil {
		c.SendString("jwt error: " + err2.Error())
		return c.SendStatus(fiber.StatusInternalServerError)
	}
	// TODO record JWT in table

	return c.JSON(fiber.Map{"jwt": jwt})
}
