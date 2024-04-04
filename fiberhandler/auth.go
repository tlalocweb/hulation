package fiberhandler

import (
	"encoding/json"
	"net/http"

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

type StatusAuthOKResp struct {
	Userid string `json:"id"`
	Email  string `json:"email"`
	Jwt    string `json:"jwt"`
}

func StatusAuthOK(c *fiber.Ctx) error {

	jwt := c.Locals("jwt")
	permsi := c.Locals("perms")

	if jwt == nil || permsi == nil {
		return c.Status(fiber.StatusUnauthorized).SendString("No jwt or perms")
	}

	perms, err := model.GetUserById(model.GetDB(), permsi.(*model.UserPermissions).UserID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("error getting user by id: " + err.Error())
	}
	if perms == nil || perms.ID == "" {
		return c.Status(fiber.StatusUnauthorized).SendString("No id")
	}

	user, err := model.GetUserById(model.GetDB(), perms.ID)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).SendString("No user by id")
	}

	resp, err := json.Marshal(&StatusAuthOKResp{
		Userid: user.ID,
		Email:  user.Email,
		Jwt:    jwt.(string),
	})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("error marshalling response: " + err.Error())
	}
	// set json header
	c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	// respond ok
	return c.Status(200).Send(resp)
}

func Logout(c *fiber.Ctx) error {
	// hostconf, _, httperr, err := GetHostConfig(c)
	// if err != nil {
	// 	return c.Status(httperr).SendString(err.Error())
	// }
	// id := c.Query("h")
	// if id != hostconf.ID {
	// 	return c.Status(400).SendString("host id mismmatch")
	// }

	// respond ok
	return c.Status(http.StatusNoContent).SendString("UNIMPLEMENTED")
}

func NewUser(c *fiber.Ctx) error {
	// hostconf, _, httperr, err := GetHostConfig(c)
	// if err != nil {
	// 	return c.Status(httperr).SendString(err.Error())
	// }
	// id := c.Query("h")
	// if id != hostconf.ID {
	// 	return c.Status(400).SendString("host id mismmatch")
	// }

	// respond ok
	return c.Status(http.StatusNoContent).SendString("UNIMPLEMENTED")
}

func GetUser(c *fiber.Ctx) error {
	// hostconf, _, httperr, err := GetHostConfig(c)
	// if err != nil {
	// 	return c.Status(httperr).SendString(err.Error())
	// }
	// id := c.Query("h")
	// if id != hostconf.ID {
	// 	return c.Status(400).SendString("host id mismmatch")
	// }

	// respond ok
	return c.Status(http.StatusNoContent).SendString("UNIMPLEMENTED")
}

func ModifyUser(c *fiber.Ctx) error {
	// hostconf, _, httperr, err := GetHostConfig(c)
	// if err != nil {
	// 	return c.Status(httperr).SendString(err.Error())
	// }
	// id := c.Query("h")
	// if id != hostconf.ID {
	// 	return c.Status(400).SendString("host id mismmatch")
	// }

	// respond ok
	return c.Status(http.StatusNoContent).SendString("UNIMPLEMENTED")
}
