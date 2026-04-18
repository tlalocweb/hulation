package handler

import (
	"context"
	"net/http"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/filesystem"
	"github.com/tlalocweb/hulation/app"
)

// FiberCtx adapts *fiber.Ctx to the RequestCtx interface.
type FiberCtx struct {
	c    *fiber.Ctx
	code int
}

// NewFiberCtx wraps a Fiber context.
func NewFiberCtx(c *fiber.Ctx) *FiberCtx {
	return &FiberCtx{c: c, code: 200}
}

func (f *FiberCtx) Body() []byte {
	return f.c.Body()
}

func (f *FiberCtx) BodyParser(out interface{}) error {
	return f.c.BodyParser(out)
}

func (f *FiberCtx) Cookie(name string) string {
	return f.c.Cookies(name)
}

func (f *FiberCtx) Header(name string) string {
	return f.c.Get(name)
}

func (f *FiberCtx) Query(name string) string {
	return f.c.Query(name)
}

func (f *FiberCtx) QueryString() string {
	return string(f.c.Request().URI().QueryString())
}

func (f *FiberCtx) Param(name string) string {
	return f.c.Params(name)
}

func (f *FiberCtx) IP() string {
	// Trust CF-Connecting-IP only if RemoteAddr is a verified Cloudflare IP
	cfRanges := app.GetConfig().GetCloudflareIPs()
	if cfRanges != nil {
		if cfip := f.c.Get("CF-Connecting-IP"); cfip != "" {
			if cfRanges.ContainsString(f.c.Context().RemoteAddr().String()) {
				return cfip
			}
		}
	}
	return f.c.IP()
}

func (f *FiberCtx) Hostname() string {
	return f.c.Hostname()
}

func (f *FiberCtx) Method() string {
	return f.c.Method()
}

func (f *FiberCtx) Path() string {
	return f.c.Path()
}

func (f *FiberCtx) OriginalURL() string {
	return f.c.OriginalURL()
}

func (f *FiberCtx) Protocol() string {
	return f.c.Protocol()
}

func (f *FiberCtx) Referer() string {
	return f.c.Get("Referer")
}

func (f *FiberCtx) VisitHeaders(fn func(key, value string)) {
	f.c.Request().Header.VisitAll(func(key, value []byte) {
		fn(string(key), string(value))
	})
}

func (f *FiberCtx) Status(code int) RequestCtx {
	f.code = code
	return f
}

func (f *FiberCtx) SetHeader(name, value string) {
	f.c.Set(name, value)
}

func (f *FiberCtx) SetCookie(cookie *Cookie) {
	fc := fiber.Cookie{
		Name:     cookie.Name,
		Value:    cookie.Value,
		Domain:   cookie.Domain,
		Path:     cookie.Path,
		MaxAge:   cookie.MaxAge,
		Secure:   cookie.Secure,
		HTTPOnly: cookie.HTTPOnly,
	}
	switch cookie.SameSite {
	case "Strict":
		fc.SameSite = "Strict"
	case "Lax":
		fc.SameSite = "Lax"
	case "None":
		fc.SameSite = "None"
	}
	f.c.Cookie(&fc)
}

func (f *FiberCtx) SetContentType(v string) {
	f.c.Set("Content-Type", v)
}

func (f *FiberCtx) SendBytes(data []byte) error {
	return f.c.Status(f.code).Send(data)
}

func (f *FiberCtx) SendString(s string) error {
	return f.c.Status(f.code).SendString(s)
}

func (f *FiberCtx) SendJSON(v interface{}) error {
	f.c.Status(f.code)
	return f.c.JSON(v)
}

func (f *FiberCtx) Redirect(url string, code ...int) error {
	if len(code) > 0 {
		return f.c.Redirect(url, code[0])
	}
	return f.c.Redirect(url)
}

func (f *FiberCtx) SendFile(root http.FileSystem, path string) error {
	return filesystem.SendFile(f.c, root, path)
}

func (f *FiberCtx) Locals(key string) interface{} {
	return f.c.Locals(key)
}

func (f *FiberCtx) SetLocals(key string, value interface{}) {
	f.c.Locals(key, value)
}

func (f *FiberCtx) Next() error {
	return f.c.Next()
}

func (f *FiberCtx) Context() context.Context {
	return f.c.UserContext()
}

// WrapForFiber converts a unified Handler into a fiber.Handler.
func WrapForFiber(h Handler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		return h(NewFiberCtx(c))
	}
}
