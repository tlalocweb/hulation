package handler

import (
	"encoding/json"
	"net/http"

	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
)

// Login is the legacy plaintext-password endpoint. Removed —
// hula now requires OPAQUE PAKE for every password-based login
// path. Returns 410 Gone with a hint so old clients fail loud
// instead of silently retrying.
//
// The route at POST /api/auth/login is kept registered for now so
// stale clients receive an explicit error rather than 404, but
// the underlying plaintext-comparison code is gone.
func Login(ctx RequestCtx) error {
	log.Debugf("legacy /api/auth/login hit; rejecting with 410")
	body := map[string]string{
		"error": "legacy login removed; use OPAQUE at /api/v1/auth/opaque/login/* (run hulactl set-password to bootstrap)",
	}
	ctx.SetContentType("application/json")
	js, _ := json.Marshal(body)
	return ctx.Status(http.StatusGone).SendString(string(js))
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
