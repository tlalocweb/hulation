package handler

import (
	"fmt"

	"github.com/gofiber/fiber/v2"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
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

type VisitorCookiesBaton struct {
	Sscookiem *model.VisitorCookie
	Cookiem   *model.VisitorCookie
}

func SetCSP(c *fiber.Ctx, hostconf *config.Server) {
	cspmap := hostconf.GetCSPMap()
	policy := ""
	for k, v := range cspmap {
		policy = fmt.Sprintf("%s; %s %s", policy, k, v)
	}
	c.Set("Content-Security-Policy", policy)
}

func GetOrSetVisitor(c *fiber.Ctx, hostconf *config.Server, baton *VisitorCookiesBaton) (visitor *model.Visitor, newvisitor bool, err error) {
	if hostconf == nil {
		err = &ResponseError{StatusCode: 500, RootCause: fmt.Errorf("hostconf is nil")}
		return
	}
	// check for httponly cookie
	sscookie := c.Cookies(hostconf.CookieOpts.CookiePrefix + "_helloss")
	if len(sscookie) > 0 {
		log.Debugf("saw sscookie (helloiframe): %s", sscookie)
		// cookie exists - find visitor
		visitor, err = model.GetVisitorBySSCookie(model.GetDB(), sscookie)
		if err != nil {
			// ignore not found error
			//			return c.Status(500).SendString()
			err = &ResponseError{StatusCode: 500, RootCause: fmt.Errorf("error getting visitor by sscookie: %s", err.Error())}
			return
		} else {
			if visitor != nil {
				log.Debugf("visitor seen by sscookie: %s", visitor.ID)
			} else {
				log.Debugf("no known visitor by sscookie")
			}
		}
	}
	// check for normal cookie
	// if we find both, the normal cookie takes priority over sscookie
	// when we look up the Visitor
	cookie := c.Cookies(hostconf.CookieOpts.CookiePrefix + "_hello")
	if visitor == nil && len(cookie) > 0 {
		log.Debugf("saw cookie (helloiframe): %s", cookie)
		// cookie exists - find visitor
		visitor, err = model.GetVisitorByCookie(model.GetDB(), cookie)
		if err != nil {
			// ignore not found error
			err = &ResponseError{StatusCode: 500, RootCause: fmt.Errorf("error getting visitor by sscookie: %s", err.Error())}
			return
		} else {
			if visitor != nil {
				log.Debugf("visitor seen by cookie (helloiframe): %s", visitor.ID)
			} else {
				log.Debugf("no known visitor by cookie")
			}
		}
	}

	if visitor == nil {
		visitor = model.NewVisitor()
		newvisitor = true
	}

	var sscookiem *model.VisitorCookie
	var cookiem *model.VisitorCookie

	if len(cookie) < 1 {
		cookiem, err = visitor.NewVisitorCookie()
		if err != nil {
			log.Errorf("error creating cookie: %s", err.Error())
			//			return c.Status(500).SendString("error creating cookie: " + err.Error())
		}
		cookie = cookiem.Cookie
		log.Debugf("new cookie (helloiframe): %s", cookie)
		// err = model.AddCookieToVisitor(model.GetDB(), visitor, cookiem)
		// if err != nil {
		// 	return c.Status(500).SendString("error adding cookie to visitor: " + err.Error())
		// }
	} else {
		log.Debugf("CookieFromCookieVal: %s", cookie)
		// this should be moved to the bounce map:
		cookiem, err = model.CookieFromCookieVal(model.GetDB(), cookie, visitor)
		if err != nil {
			log.Errorf("error getting cookie from cookie val: %s", err.Error())
			// create new cookie then
			cookiem, err = visitor.NewVisitorCookie()
			if err != nil {
				log.Errorf("error creating cookie: %s", err.Error())
				//			return c.Status(500).SendString("error creating cookie: " + err.Error())
			}
			log.Debugf("new cookie (helloiframe): %s", cookie)
		}
		// reset cookie string to new value in case a new cookie was made
		cookie = cookiem.Cookie
		log.Debugf("CookieFromCookieVal (new?): %s", cookie)
		//		cookiem.Commit(model.GetDB())
	}

	if baton != nil {
		baton.Cookiem = cookiem
	}

	if len(sscookie) < 1 {
		sscookiem, err = visitor.NewVisitorSSCookie()
		if err != nil {
			err = &ResponseError{StatusCode: 500, RootCause: fmt.Errorf("error creating cookies: %s", err.Error())}
			return
			//			return c.Status(500).SendString("error creating sscookie: " + err.Error())
		}
		sscookie = sscookiem.Cookie
		log.Debugf("new sscookie (helloiframe): %s", sscookie)
	} else {
		log.Debugf("SSCookieFromSSCookieVal: %s", sscookie)
		sscookiem, err = model.SSCookieFromSSCookieVal(model.GetDB(), sscookie, visitor)
		if err != nil {
			log.Errorf("error getting sscookie from sscookie val: %s", err.Error())
			// create new cookie then
			sscookiem, err = visitor.NewVisitorSSCookie()
			if err != nil {
				log.Errorf("error creating sscookie: %s", err.Error())
				//			return c.Status(500).SendString("error creating cookie: " + err.Error())
			}
			log.Debugf("new cookie (helloiframe): %s", cookie)
		}
		// reset cookie string to new value in case a new cookie was made
		sscookie = sscookiem.Cookie
		log.Debugf("SSCookieFromSSCookieVal (new?): %s", sscookie)
		//		sscookiem.Commit(model.GetDB())
	}
	if baton != nil {
		baton.Sscookiem = sscookiem
	}
	samesite := hostconf.CookieOpts.SameSite
	if len(samesite) < 1 {
		if AreWeServingThePage(c, hostconf) {
			// if we are actually serving the content, then we can set the cookie to strict
			// which should allow the cookie to remain longer
			samesite = "Strict"
		} else {
			samesite = "Lax"
		}
	}
	domain := hostconf.Domain
	if len(hostconf.Domain) > 0 {
		if hostconf.CookieOpts.NoUseDomain {
			domain = ""
		}
	}
	c.Cookie(&fiber.Cookie{
		Name:     hostconf.CookieOpts.CookiePrefix + "_hello",
		Value:    cookie,
		Secure:   !hostconf.CookieOpts.NoSecure,
		HTTPOnly: false,
		SameSite: samesite,
		Domain:   domain,
		MaxAge:   60 * 60 * 24 * hostconf.HelloCookieMaxAge, // 30 days
	})
	c.Cookie(&fiber.Cookie{
		Name:     hostconf.CookieOpts.CookiePrefix + "_helloss",
		Value:    sscookie,
		Secure:   !hostconf.CookieOpts.NoSecure,
		HTTPOnly: true,
		Domain:   domain,
		SameSite: samesite,
	})

	return
}
