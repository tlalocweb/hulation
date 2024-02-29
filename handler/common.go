package handler

import (
	"fmt"

	"github.com/gofiber/fiber/v2"
	"github.com/tlalocweb/hulation/utils"
)

const (
	HTTPErrorDBFailure = 520
)

type ResponseError struct {
	StatusCode int `json:"code"`
	//	Body       string
	RootCause error `json:"error"`
}

func (e *ResponseError) Error() (ret string) {
	ret = "ClientError: " + e.RootCause.Error()
	return
}

func (e *ResponseError) Send(c *fiber.Ctx) (err error) {
	return c.Status(e.StatusCode).SendString(e.RootCause.Error())
}

func (e *ResponseError) JsonBody() string {
	if e.RootCause != nil {
		return fmt.Sprintf(`{"code": %d, "error": %s }`, e.StatusCode, utils.JsonifyStr(e.RootCause.Error()))
	} else {
		return fmt.Sprintf(`{"code": %d, "error": "unknown"}`, e.StatusCode)
	}
}
