package handler

import (
	"encoding/json"
	"net/http"

	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
	"github.com/tlalocweb/hulation/utils"
)

type LoginInput struct {
	Userid       string `json:"userid"`
	PasswordHash string `json:"hash"`
}

func Login(ctx RequestCtx) error {
	var input LoginInput

	log.Debugf("Body: %s", string(ctx.Body()))
	if err := ctx.BodyParser(&input); err != nil {
		ctx.SendString("bad parse: " + err.Error())
		return ctx.Status(http.StatusUnauthorized).SendString("")
	}

	var authok bool
	var isadmin bool
	if input.Userid == app.GetConfig().Admin.Username {
		match, err := utils.Argon2CompareHashAndSecret(input.PasswordHash, app.GetConfig().Admin.Hash)
		if err != nil {
			ctx.SendString("has function error: " + err.Error())
			return ctx.Status(http.StatusInternalServerError).SendString("")
		}
		if match {
			authok = true
			isadmin = true
		}
	}

	if !authok {
		return ctx.Status(http.StatusUnauthorized).SendString("")
	}

	// Check if TOTP is required for this user
	if isadmin && CheckTotpRequired(input.Userid) {
		return LoginResponseForTotp(ctx, input.Userid)
	}

	jwt, err2 := model.NewJWTClaimsCommit(model.GetDB(), input.Userid, &model.LoginOpts{
		IsAdmin: isadmin,
	})
	if err2 != nil {
		ctx.SendString("jwt error: " + err2.Error())
		return ctx.Status(http.StatusInternalServerError).SendString("")
	}

	return ctx.SendJSON(map[string]string{"jwt": jwt})
}

type StatusAuthOKResp struct {
	Userid string `json:"id"`
	Email  string `json:"email"`
	Jwt    string `json:"jwt"`
}

func StatusAuthOK(ctx RequestCtx) error {
	jwt := ctx.Locals("jwt")
	permsi := ctx.Locals("perms")

	if jwt == nil || permsi == nil {
		return ctx.Status(http.StatusUnauthorized).SendString("No jwt or perms")
	}

	userID := permsi.(*model.UserPermissions).UserID
	if userID == "" {
		return ctx.Status(http.StatusUnauthorized).SendString("No user id in perms")
	}

	// The admin user is configured in the YAML, not stored in the users DB table.
	// If this is the admin, return the known info without a DB lookup.
	if userID == app.GetConfig().Admin.Username {
		resp, err := json.Marshal(&StatusAuthOKResp{
			Userid: userID,
			Email:  "",
			Jwt:    jwt.(string),
		})
		if err != nil {
			return ctx.Status(http.StatusInternalServerError).SendString("error marshalling response: " + err.Error())
		}
		ctx.SetContentType("application/json")
		return ctx.Status(200).SendBytes(resp)
	}

	user, err := model.GetUserById(model.GetDB(), userID)
	if err != nil {
		return ctx.Status(http.StatusInternalServerError).SendString("error getting user by id: " + err.Error())
	}
	if user == nil || user.ID == "" {
		return ctx.Status(http.StatusUnauthorized).SendString("No user by id")
	}

	resp, err := json.Marshal(&StatusAuthOKResp{
		Userid: user.ID,
		Email:  user.Email,
		Jwt:    jwt.(string),
	})
	if err != nil {
		return ctx.Status(http.StatusInternalServerError).SendString("error marshalling response: " + err.Error())
	}
	ctx.SetContentType("application/json")
	return ctx.Status(200).SendBytes(resp)
}

func Logout(ctx RequestCtx) error {
	return ctx.Status(http.StatusNoContent).SendString("UNIMPLEMENTED")
}

func NewUser(ctx RequestCtx) error {
	return ctx.Status(http.StatusNoContent).SendString("UNIMPLEMENTED")
}

func GetUser(ctx RequestCtx) error {
	return ctx.Status(http.StatusNoContent).SendString("UNIMPLEMENTED")
}

func ModifyUser(ctx RequestCtx) error {
	return ctx.Status(http.StatusNoContent).SendString("UNIMPLEMENTED")
}
