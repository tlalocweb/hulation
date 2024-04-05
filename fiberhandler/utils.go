package fiberhandler

// utility functions for the handler package

import (
	"fmt"
	"net/url"

	"github.com/gofiber/fiber/v2"
	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
	"github.com/tlalocweb/hulation/utils"
)

// a convernience function to get the host configuration for the current request
// by looking at the Host header. If a host conifg is not found, then an error
// is returned (but not set on the fiber context)
// The only error returned currently is 404
func GetHostConfig(c *fiber.Ctx) (hostconf *config.Server, host string, httperror int, err error) {
	exist := c.Locals("hostconf")
	fiberhandler_debugf("GetHostConfig: hostconf exist: %v", exist)
	if exist != nil {
		hostconf = exist.(*config.Server)
		exist2 := c.Locals("host")
		if exist2 != nil {
			host = exist2.(string)
			return
		}
	}
	host = c.Get("Host")
	fiberhandler_debugf("GetHostConfig: host: %s", host)
	hostonly := utils.GetHostOnly(host)
	hostconf = app.GetConfig().GetServerByAnyAlias(hostonly)
	if hostconf != nil {
		if !hostconf.RespectPortInLookup() {
			host = hostonly
		}
	} else {
		log.Errorf("Unknown host: %s", host)
		httperror = 404
		err = fmt.Errorf("unknown host: %s", host)
		return
	}
	// cache for later use
	c.Locals("hostconf", hostconf)
	c.Locals("host", host)
	return
}
func AreWeServingThePage(c *fiber.Ctx, hostconf *config.Server) bool {
	referrer := c.Get("Referer")
	if referrer == "" {
		return false
	}
	rUrl, err := url.Parse(referrer)
	if err != nil {
		log.Errorf("error parsing referrer: %s", err.Error())
		return false
	}
	rHost := c.Get("Host")
	if err != nil {
		log.Errorf("error parsing host: %s", err.Error())
		return false
	}
	if !hostconf.RespectPortInLookup() {
		if rUrl.Hostname() != utils.GetHostOnly(rHost) {
			return false
		}
	} else {
		if rUrl.Host != rHost {
			return false
		}
	}
	return true
}

func GetVisitorFromContext(c *fiber.Ctx, hostconf *config.Server) (visitor *model.Visitor, sscookiem *model.VisitorCookie, cookiem *model.VisitorCookie, errresp *ResponseError) {
	//	var visitor *model.Visitor
	// var sscookiem *model.VisitorCookie
	// var cookiem *model.VisitorCookie
	var err error
	var cookieprefix string
	if hostconf != nil {
		cookieprefix = hostconf.CookieOpts.CookiePrefix
	}
	if len(cookieprefix) < 1 {
		cookieprefix = "hula"
	}

	cookie := c.Cookies(cookieprefix + "_hello")
	sscookie := c.Cookies(cookieprefix + "_helloss")

	if len(sscookie) > 0 {
		log.Debugf("saw sscookie (hellonoscript): %s", sscookie)
		// cookie exists - find visitor
		visitor, err = model.GetVisitorBySSCookie(model.GetDB(), sscookie)
		if err != nil {
			// ignore not found error
			errresp = &ResponseError{StatusCode: 500, RootCause: fmt.Errorf("error getting visitor by sscookie: %w", err)}
			//.Status(500).SendString("error getting visitor by sscookie: " + err.Error())
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
	if visitor == nil && len(cookie) > 0 {
		log.Debugf("saw cookie (hellonoscript): %s", cookie)
		// cookie exists - find visitor
		visitor, err = model.GetVisitorByCookie(model.GetDB(), cookie)
		if err != nil {
			// ignore not found error
			errresp = &ResponseError{StatusCode: 500, RootCause: fmt.Errorf("error getting visitor by cookie: %w", err)}
		} else {
			if visitor != nil {
				log.Debugf("visitor seen by cookie (helloiframe): %s", visitor.ID)
			} else {
				log.Debugf("no known visitor by cookie")
			}
		}
	}

	if len(cookie) < 1 {
		cookiem, err = visitor.NewVisitorCookie()
		if err != nil {
			log.Errorf("error creating cookie: %s", err.Error())
			//			return c.Status(500).SendString("error creating cookie: " + err.Error())
		}
		cookie = cookiem.Cookie
		log.Debugf("new cookie (hellonoscript): %s", cookie)
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
			log.Debugf("new cookie (hellonoscript): %s", cookie)
		}
		// reset cookie string to new value in case a new cookie was made
		cookie = cookiem.Cookie
		log.Debugf("CookieFromCookieVal (new?): %s", cookie)
		//			cookiem.Commit(model.GetDB())
	}
	return
}
