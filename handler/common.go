package handler

import (
	"fmt"

	"github.com/tlalocweb/hulation/model"
	"github.com/tlalocweb/hulation/utils"
)

const (
	HTTPErrorDBFailure = 520
)

// ResponseError is a structured error with an HTTP status code.
type ResponseError struct {
	StatusCode int   `json:"code"`
	RootCause  error `json:"error"`
}

func (e *ResponseError) Error() string {
	return "ClientError: " + e.RootCause.Error()
}

func (e *ResponseError) Send(ctx RequestCtx) error {
	return ctx.Status(e.StatusCode).SendString(e.RootCause.Error())
}

func (e *ResponseError) JsonBody() string {
	if e.RootCause != nil {
		return fmt.Sprintf(`{"code": %d, "error": %s }`, e.StatusCode, utils.JsonifyStr(e.RootCause.Error()))
	}
	return fmt.Sprintf(`{"code": %d, "error": "unknown"}`, e.StatusCode)
}

// VisitorCookiesBaton carries cookie model references between functions.
type VisitorCookiesBaton struct {
	Sscookiem *model.VisitorCookie
	Cookiem   *model.VisitorCookie
}
