package fiberhandler

import (
	"net/url"
	"strings"

	"github.com/cbroglie/mustache"
	"github.com/gofiber/fiber/v2"
	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
	"github.com/tlalocweb/hulation/utils"
)

const (
	VisitBounceFlagSawURL = 1 << iota
)

const (
	// Bounce map Data indexes
	BMIndexURL       = iota // string
	BMIndexUA        = iota // string
	BMIndexIP        = iota // string
	BMIndexCookieM   = iota // *model.VisitorCookie
	BMIndexSSCookieM = iota // *model.VisitorCookie
	BMIndexVisitorM  = iota // *model.Visitor
	BMHostConf       = iota // *config.Server
	// user
	BMIndexData1 = iota
	BMIndexData2 = iota
	BMIndexData3 = iota
	BMIndexData4 = iota
	BMIndexData5 = iota
)

const (
	// a HelloMesg struct
	BMIndexHelloMsg = BMIndexData1
)

type HelloMsg struct {
	// bounce ID from query
	Bounce    string `json:"b"`
	EventCode int    `json:"e"`
	// URL being visited or URL where event originates
	URL string `json:"u"`
	// Aux data
	Data string `json:"d"`
}

var visitorBounceMap *model.BounceMap

func InitVistorHandlers() {
	if visitorBounceMap == nil {
		visitorBounceMap = model.NewBounceMap(app.GetConfig().BounceTimeout)
	}
	visitorBounceMap.Start()
}

var precompileNewVisitorHooks = &utils.RunOnceSingleton{Run: func(p interface{}) (err error) {
	// add in any global things which should be available during any time we call this hook
	// If we don't change the names of the globals later, the script should always
	// be precompiled and ready to run.
	hostconf := p.(*config.Server)
	if hostconf.Hooks != nil {
		hostconf.Hooks.PrecompileHooksOnNewVisitor(map[string]any{"visitorid": "", "url": ""})
	}
	return
}}

// these routes handle the actual tracking of visitors to the site.
// There are two ways to do this:
// - a script is locaded from the browser - that JS script makes an API call to /v/hello
//   in order for the cookie to be set easily, the JS is pulled in through an iframe (IFrameHello) load from the server
//   this iframe load is how the cookie is set. The iframe HTML and script can be entirely cached.
//   The actualy reporting of the URL is done in JS using XHR back to the hulation server (Hello). That XHR has a
//   cache busting query param, OR a random url speciifc to the originally requested URL (parent page)
// - the noscript method is the other.
//   Here the server loads an iframe (HelloNoScript) with a  <noscript> tag around it. This iframe load has an inner iframe load inside it
//   with a cache busting query param, OR a random url speciifc to the originally requested URL (parent page)
//
// Both methods will likely be on each page. The noscript method is used for browsers that have JS disabled.
// If both method are used, and report succesfully, the unique visit event will simply be recorded twice, but the event will have the same ID
// so when the report is generated the table will merge this back to one event.
//
// See: https://stackoverflow.com/questions/3420004/access-parent-url-from-iframe/7739035#7739035

// Hello is the basic API call made by the client anytime a visitor to website hits a page
func Hello(c *fiber.Ctx) error {

	// if hello API call has a valid bounce value, we dont need any of the rest of the
	// host determination stuff
	// FIX...
	var err error
	var hello HelloMsg

	err = c.BodyParser(&hello)
	if err != nil {
		c.Status(400).SendString("bad parse: " + err.Error())
	}

	if len(hello.URL) > 0 {
		log.Debugf("saw url (hello): %s", hello.URL)
	}

	// bounce ID from query
	bounce := c.Query("b")
	if len(bounce) > 0 {
		log.Debugf("saw bounce val (hello): %s", bounce)
		if hello.Bounce != bounce {
			log.Errorf("bounce mismatch: %s != %s", hello.Bounce, bounce)
			return c.Status(400).SendString("bounce mismatch")
		}
	} else {
		log.Errorf("no bounce val (hello)")
		return c.Status(404).SendString("404 Not Found - No bounce query param")
	}

	hconf, host, httperr, err := GetHostConfig(c)

	precompileNewVisitorHooks.Verify(host, hconf, "error precompiling new visitor hooks (hello)")

	if err != nil {
		return c.Status(httperr).SendString(err.Error())
	}

	//	contenttype := c.Get("Content-Type", "application/json")
	c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	c.Set(fiber.HeaderCacheControl, "no-cache, no-store, must-revalidate")
	SetCSP(c, hconf)

	// figure out visitor
	var sscookiem *model.VisitorCookie
	var sscookie string
	rawdat, ok := visitorBounceMap.GetDataByBounceID(bounce, BMIndexSSCookieM)
	if ok {
		sscookiem = rawdat.(*model.VisitorCookie)
	} else {
		log.Debugf("in /hello - no sscookie found for bounce: %s", bounce)
	}
	if sscookiem != nil {
		sscookie = sscookiem.Cookie
	}

	visitorBounceMap.ReportBounceBack(bounce, func(b *model.Bounce) (err error) {
		// should only write to DB if changed
		err = b.Visitor.Commit(model.GetDB())
		if err != nil {
			log.Errorf("error committing visitor: %s", err.Error())
		}
		// if we have not previously recorded this URL (from the iframe load that pulled the script sending us this request),
		// then we will create an event for it now
		hello := b.Data[BMIndexHelloMsg].(*HelloMsg)
		if hello.EventCode == model.EventCodePageView {
			if b.Flags&VisitBounceFlagSawURL == 0 {
				url := hello.URL
				host, urlpath := utils.GetURLHostPath(url)
				log.Debugf("saw url (hello): %s (%s)", url, urlpath)
				// since we have a URL we will create an event for it now
				ev := model.NewEvent(model.EventCodePageView)
				ev.SetURL(url)
				ev.SetUrlPath(urlpath)
				ev.SetHost(host)
				ev.SetMethod("hello")
				ev.SetBrowserUA(b.Data[BMIndexUA].(string))
				ev.SetFromIP(b.Data[BMIndexIP].(string))
				err = ev.CommitTo(model.GetDB(), b.Visitor)
				if err != nil {
					log.Errorf("error committing event: %s", err.Error())
				}
				b.Flags |= VisitBounceFlagSawURL
				// execute hooks
				hconf := b.Data[BMHostConf].(*config.Server)
				if hconf != nil {
					hconf.Hooks.SubmitToHooksOnNewVisitor(map[string]any{"visitorid": b.Visitor.ID, "url": url}, nil, nil)
				}
			} else {
				log.Debugf("already saw URL for this bounce (hello)")
			}
		} else {
			log.Debugf("not a pageview event (hello)")

		}

		return err
	}, map[uint32]interface{}{BMIndexHelloMsg: &hello, BMIndexUA: strings.Clone(c.Get("User-Agent")), BMIndexIP: strings.Clone(c.IP()), BMHostConf: hconf})

	return c.SendString(`{"status":"ok","vc":"` + sscookie + `"}`)
}

const iframehello = `<!doctype html>
<html lang=en>
<head><meta charset=utf-8><title>ha</title>
<script type="text/javascript" src="{{hulaurl}}/scripts/{{helloscript}}?b={{b}}&u={{thisurl}}"></script>
</head>
<body></body>
</html>`

var iframeHelloTemplate *mustache.Template

// This is the handler for when a normal (not the noscript) iframe is loaded
// We will set a visitor cookie and a sscookie if one does not exist.
func HelloIframe(c *fiber.Ctx) error {
	var err error
	if iframeHelloTemplate == nil {
		iframeHelloTemplate, err = mustache.ParseString(iframehello)
		if err != nil {
			return c.Status(500).SendString("error parsing hello iframe template: " + err.Error())
		}
	}

	c.Set(fiber.HeaderCacheControl, "no-cache, no-store, must-revalidate")

	// hostconf, host, httperr, err := GetHostConfig(c)
	// if err != nil {
	// 	return c.Status(httperr).SendString(err.Error())
	// }
	var thisurl string
	thisurlescaped := c.Query("u")
	if len(thisurlescaped) > 0 {
		thisurl, err = url.PathUnescape(thisurlescaped)
		if err != nil {
			log.Errorf("error unescaping URL (HelloIframe): %s", err.Error())
		}
		log.Debugf("saw url (helloiframe - handler): %s", thisurl)
	}

	hostconf, host, _, err := GetHostConfigFromUrl(thisurl)
	if err != nil {
		log.Errorf("error getting host config from URL: %s", err.Error())
		//		return c.Status(httperr).SendString(err.Error())
	}

	SetCSP(c, hostconf)
	id := c.Query("h")
	if id != hostconf.ID {
		log.Errorf("host id mismmatch: %s != %s", id, hostconf.ID)
		return c.Status(400).SendString("host id mismmatch")
		// ok, fine - then check the check the 'u' query param
		// urlU := c.Query("u")
		// if len(urlU) < 1 {
		// 	return c.Status(400).SendString("bad param 'u'")
		// }
		// parsed, err := url.Parse(urlU)
		// log.Debugf("parsed host: <%s> vs hostconf: <%s>", parsed.Host, hostconf.Host)

		// if err != nil {
		// 	return c.Status(400).SendString("could not parse u param: " + err.Error())
		// }
		// hostconf = app.GetConfig().GetServerByAnyAlias(parsed.Host)
		// if hostconf == nil {
		// 	return c.Status(400).SendString("unknown host (2)")
		// }
		// if id != hostconf.ID {
		// 	return c.Status(400).SendString("host id mismmatch (2)")
		// }
		// if parsed.Host != hostconf.Host {
		// 	return c.Status(400).SendString("host id mismmatch (2)")
		// }
	}
	precompileNewVisitorHooks.Verify(host, hostconf, "error precompiling new visitor hooks (helloiframe)")

	var cookiebaton VisitorCookiesBaton

	// TODO - do something on newvisitor
	// newvisitor
	visitor, _, err := GetOrSetVisitor(c, hostconf, &cookiebaton)

	if err != nil {
		resperr, ok := err.(*ResponseError)
		if ok {
			return c.Status(resperr.StatusCode).SendString(resperr.Error())
		} else {
			return c.Status(500).SendString("error getting or setting visitor: " + err.Error())
		}
	}

	var body string
	// NOTE: In these callbacks your CAN NOT reference anything from the original fiber context / request
	// stuff in the closure you can reference
	bounceS, _ := visitorBounceMap.NewBounceWithVisitor(visitor, func(b *model.Bounce) (err error) {
		// should only write to DB if changed
		err = b.Visitor.Commit(model.GetDB())
		if err != nil {
			log.Errorf("error committing visitor: %s", err.Error())
		}
		// should only write to DB if changed
		cookiem := b.Data[BMIndexCookieM].(*model.VisitorCookie)
		if cookiem != nil {
			err = cookiem.Commit(model.GetDB())
			if err != nil {
				log.Errorf("error committing cookie: %s", err.Error())
			}
		}
		// todo: lookup the cookie object by ID update last seen

		// should only write to DB if changed
		sscookiem := b.Data[BMIndexSSCookieM].(*model.VisitorCookie)
		if sscookiem != nil {
			err = sscookiem.Commit(model.GetDB())
			if err != nil {
				log.Errorf("error committing sscookie: %s", err.Error())
			}
		}
		url := b.Data[BMIndexURL].(string)
		if len(url) > 0 {
			host, urlpath := utils.GetURLHostPath(url)
			log.Debugf("saw url (helloiframe cb1): %s (%s)", url, urlpath)
			// since we have a URL we will create an event for it now
			ev := model.NewEvent(model.EventCodePageView)
			ev.SetURL(url)
			ev.SetUrlPath(urlpath)
			ev.SetHost(host)
			ev.SetMethod("helloiframe")
			ev.SetBrowserUA(b.Data[BMIndexUA].(string))
			ev.SetFromIP(b.Data[BMIndexIP].(string))

			err = ev.CommitTo(model.GetDB(), b.Visitor)
			if err != nil {
				log.Errorf("error committing event: %s", err.Error())
			}
			b.Flags |= VisitBounceFlagSawURL

			hconf := b.Data[BMHostConf].(*config.Server)
			if hconf != nil {
				if hconf.Hooks == nil {
					log.Debugf("no hooks for host: %s", hconf.Host)
				} else {
					hconf.Hooks.SubmitToHooksOnNewVisitor(map[string]any{"visitorid": visitor.ID, "url": url}, nil, nil)
				}
			} else {
				log.Errorf("hconf was nil in bounce map")
			}
		}

		return err

		// it's necessary to Clone v - becasue string is a struct with a pointer and len
		// it is possible ofr the memory the pointer in the struct points to get garbage collected
		// and then the string is no longer valid
		// It seems the GC can't keep up with our callback pointers
	}, map[uint32]interface{}{BMIndexURL: strings.Clone(thisurl), BMIndexCookieM: cookiebaton.Cookiem, BMIndexSSCookieM: cookiebaton.Sscookiem,
		BMIndexUA: strings.Clone(c.Get("User-Agent")), BMIndexIP: strings.Clone(c.IP()), BMHostConf: hostconf})

	hulaurl, err := utils.GetBaseUrl(c.OriginalURL())
	if err != nil {
		log.Errorf("error parsing original URL: %s - using hostconf url", err.Error())
		hulaurl = hostconf.GetExternalUrl()
	}
	body, err = iframeHelloTemplate.Render(map[string]string{"helloscript": app.GetConfig().PublishedHelloScriptFilename, "apipath": hostconf.APIPath, "h": hostconf.ID,
		"b": bounceS, "cookieprefix": hostconf.CookieOpts.CookiePrefix, "visithost": hostconf.Host, "hulaurl": hulaurl, "thisurl": thisurl})
	if err != nil {
		return c.Status(500).SendString("error rendering iframe template: " + err.Error())
	}

	c.Set(fiber.HeaderContentType, fiber.MIMETextHTML)
	return c.SendString(body)
	// }
	//	return c.SendString("ok")
}

const iframe = `<!doctype html>
<html lang=en>
<head><meta charset=utf-8><title>ha</title></head>
<body><iframe src="{{hulaurl}}/v/{{iframename}}?h={{h}}&b={{b}}" height="0" width="0" style="display:none;visibility:hidden">			
</iframe></body>
</html>`

const iframe2 = `<!doctype html>
<html lang=en>
<head><meta charset=utf-8><title>Hello</title></head>
<body><!--done--></body>
</html>`

var iframeTemplate *mustache.Template

// noscript / iframe way of doing hello
func HelloNoScript(c *fiber.Ctx) error {
	var err error
	if iframeTemplate == nil {
		iframeTemplate, err = mustache.ParseString(iframe)
		if err != nil {
			return c.Status(500).SendString("error parsing hello iframe template: " + err.Error())
		}
	}
	c.Set(fiber.HeaderCacheControl, "no-cache, no-store, must-revalidate")

	hostconf, _, httperr, err := GetHostConfig(c)
	if err != nil {
		return c.Status(httperr).SendString(err.Error())
	}

	id := c.Query("h")
	if id != hostconf.ID {
		//		return c.Status(400).SendString("host id mismmatch")
		// ok, fine - then check the check the 'u' query param
		urlU := c.Query("u")
		if len(urlU) < 1 {
			return c.Status(400).SendString("host id mismmatch")
		}
		parsed, err := url.Parse(urlU)
		//		log.Debugf("parsed host: <%s> vs hostconf: <%s>", parsed.Host, hostconf.Host)
		if err != nil {
			return c.Status(400).SendString("could not parse u param: " + err.Error())
		}
		hostconf = app.GetConfig().GetServerByAnyAlias(parsed.Host)
		if hostconf == nil {
			return c.Status(400).SendString("unknown host (2)")
		}
		if id != hostconf.ID {
			return c.Status(400).SendString("host id mismmatch (2)")
		}
	}

	SetCSP(c, hostconf)
	bouncid := c.Query("b")

	url := c.Get("Referer")

	if len(bouncid) < 1 && len(url) > 0 {
		log.Debugf("saw url (hellonoscript / referer): %s", url)
	}

	// if there is bounceid, then we are reporting a bounceback
	if len(bouncid) > 0 {

		visitorBounceMap.ReportBounceBack(bouncid, func(b *model.Bounce) (err error) {
			// should only write to DB if changed
			err = b.Visitor.Commit(model.GetDB())
			if err != nil {
				log.Errorf("error committing visitor: %s", err.Error())
			}
			// // TODO create event for Visitor which record the URL
			// // as being visited
			// ev := model.NewEvent(model.EventCodePageView)
			// ev.SetURL(url)
			// err = ev.CommitTo(model.GetDB(), b.Visitor)
			// if err != nil {
			// 	log.Errorf("error committing event: %s", err.Error())
			// }

			return err
		})

		c.Set(fiber.HeaderContentType, fiber.MIMETextHTML)
		return c.SendString(iframe2)

	} else {

		var cookiebaton VisitorCookiesBaton
		visitor, _, err := GetOrSetVisitor(c, hostconf, &cookiebaton)

		// var visitor *model.Visitor
		// var sscookiem *model.VisitorCookie
		// var cookiem *model.VisitorCookie
		// cookie := c.Cookies(hostconf.CookieOpts.CookiePrefix + "_hello")
		// sscookie := c.Cookies(hostconf.CookieOpts.CookiePrefix + "_helloss")

		// if len(sscookie) > 0 {
		// 	log.Debugf("saw sscookie (hellonoscript): %s", sscookie)
		// 	// cookie exists - find visitor
		// 	visitor, err = model.GetVisitorBySSCookie(model.GetDB(), sscookie)
		// 	if err != nil {
		// 		// ignore not found error
		// 		return c.Status(500).SendString("error getting visitor by sscookie: " + err.Error())
		// 	} else {
		// 		if visitor != nil {
		// 			log.Debugf("visitor seen by sscookie: %s", visitor.ID)
		// 		} else {
		// 			log.Debugf("no known visitor by sscookie")
		// 		}
		// 	}
		// }
		// // check for normal cookie
		// // if we find both, the normal cookie takes priority over sscookie
		// // when we look up the Visitor
		// if visitor == nil && len(cookie) > 0 {
		// 	log.Debugf("saw cookie (hellonoscript): %s", cookie)
		// 	// cookie exists - find visitor
		// 	visitor, err = model.GetVisitorByCookie(model.GetDB(), cookie)
		// 	if err != nil {
		// 		// ignore not found error
		// 		return c.Status(500).SendString("error getting visitor by cookie: " + err.Error())
		// 	} else {
		// 		if visitor != nil {
		// 			log.Debugf("visitor seen by cookie (helloiframe): %s", visitor.ID)
		// 		} else {
		// 			log.Debugf("no known visitor by cookie")
		// 		}
		// 	}
		// }

		// if len(cookie) < 1 {
		// 	cookiem, err = visitor.NewVisitorCookie()
		// 	if err != nil {
		// 		log.Errorf("error creating cookie: %s", err.Error())
		// 		//			return c.Status(500).SendString("error creating cookie: " + err.Error())
		// 	}
		// 	cookie = cookiem.Cookie
		// 	log.Debugf("new cookie (hellonoscript): %s", cookie)
		// 	// err = model.AddCookieToVisitor(model.GetDB(), visitor, cookiem)
		// 	// if err != nil {
		// 	// 	return c.Status(500).SendString("error adding cookie to visitor: " + err.Error())
		// 	// }
		// } else {
		// 	log.Debugf("CookieFromCookieVal: %s", cookie)
		// 	// this should be moved to the bounce map:
		// 	cookiem, err = model.CookieFromCookieVal(model.GetDB(), cookie, visitor)
		// 	if err != nil {
		// 		log.Errorf("error getting cookie from cookie val: %s", err.Error())
		// 		// create new cookie then
		// 		cookiem, err = visitor.NewVisitorCookie()
		// 		if err != nil {
		// 			log.Errorf("error creating cookie: %s", err.Error())
		// 			//			return c.Status(500).SendString("error creating cookie: " + err.Error())
		// 		}
		// 		log.Debugf("new cookie (hellonoscript): %s", cookie)
		// 	}
		// 	// reset cookie string to new value in case a new cookie was made
		// 	cookie = cookiem.Cookie
		// 	log.Debugf("CookieFromCookieVal (new?): %s", cookie)
		// 	//			cookiem.Commit(model.GetDB())
		// }

		// if len(sscookie) < 1 {
		// 	sscookiem, err = visitor.NewVisitorSSCookie()
		// 	if err != nil {
		// 		return c.Status(500).SendString("error creating sscookie: " + err.Error())
		// 	}
		// 	sscookie = sscookiem.Cookie
		// 	log.Debugf("new sscookie (hellonoscript): %s", sscookie)
		// } else {
		// 	log.Debugf("SSCookieFromSSCookieVal: %s", sscookie)
		// 	sscookiem, err = model.SSCookieFromSSCookieVal(model.GetDB(), sscookie, visitor)
		// 	if err != nil {
		// 		log.Errorf("error getting sscookie from sscookie val: %s", err.Error())
		// 		// create new cookie then
		// 		sscookiem, err = visitor.NewVisitorSSCookie()
		// 		if err != nil {
		// 			log.Errorf("error creating sscookie: %s", err.Error())
		// 			//			return c.Status(500).SendString("error creating cookie: " + err.Error())
		// 		}
		// 		log.Debugf("new cookie (hellonoscript): %s", cookie)
		// 	}
		// 	// reset cookie string to new value in case a new cookie was made
		// 	sscookie = sscookiem.Cookie
		// 	log.Debugf("SSCookieFromSSCookieVal (new?): %s", sscookie)
		// 	//			sscookiem.Commit(model.GetDB())
		// }

		// samesite := hostconf.CookieOpts.SameSite
		// if len(samesite) < 1 {
		// 	if AreWeServingThePage(c, hostconf) {
		// 		// if we are actually serving the content, then we can set the cookie to strict
		// 		// which should allow the cookie to remain longer
		// 		samesite = "Strict"
		// 	} else {
		// 		samesite = "Lax"
		// 	}
		// }

		// c.Cookie(&fiber.Cookie{
		// 	Name:     hostconf.CookieOpts.CookiePrefix + "_hello",
		// 	Value:    cookie,
		// 	Secure:   !hostconf.CookieOpts.NoSecure,
		// 	HTTPOnly: false,
		// 	SameSite: samesite,
		// })
		// c.Cookie(&fiber.Cookie{
		// 	Name:     hostconf.CookieOpts.CookiePrefix + "_helloss",
		// 	Value:    sscookie,
		// 	Secure:   !hostconf.CookieOpts.NoSecure,
		// 	HTTPOnly: true,
		// 	SameSite: samesite,
		// })

		var body string
		bounceS, _ := visitorBounceMap.NewBounceWithVisitor(visitor, func(b *model.Bounce) (err error) {
			// should only write to DB if changed
			err = b.Visitor.Commit(model.GetDB())
			if err != nil {
				log.Errorf("error committing visitor: %s", err.Error())
			}
			// should only write to DB if changed
			cookiem := b.Data[BMIndexCookieM].(*model.VisitorCookie)
			if cookiem != nil {
				err = cookiem.Commit(model.GetDB())
				if err != nil {
					log.Errorf("error committing cookie: %s", err.Error())
				}
			}
			// todo: lookup the cookie object by ID update last seen

			// should only write to DB if changed
			sscookiem := b.Data[BMIndexSSCookieM].(*model.VisitorCookie)
			if sscookiem != nil {
				err = sscookiem.Commit(model.GetDB())
				if err != nil {
					log.Errorf("error committing sscookie: %s", err.Error())
				}
			}
			url := b.Data[BMIndexURL]
			if len(url.(string)) > 0 {
				host, urlpath := utils.GetURLHostPath(url.(string))
				log.Debugf("saw url (hellonoscript): %s (%s)", url, urlpath)
				// since we have a URL we will create an event for it now
				ev := model.NewEvent(model.EventCodePageView)
				ev.SetURL(url.(string))
				ev.SetUrlPath(urlpath)
				ev.SetHost(host)
				ev.SetMethod("hellonoscript")
				ev.SetBrowserUA(b.Data[BMIndexUA].(string))
				ev.SetFromIP(b.Data[BMIndexIP].(string))
				err = ev.CommitTo(model.GetDB(), b.Visitor)
				if err != nil {
					log.Errorf("error committing event: %s", err.Error())
				}
				b.Flags |= VisitBounceFlagSawURL
			}

			return err

		}, map[uint32]interface{}{BMIndexURL: strings.Clone(url), BMIndexCookieM: cookiebaton.Cookiem, BMIndexSSCookieM: cookiebaton.Sscookiem,
			BMIndexUA: strings.Clone(c.Get("User-Agent")), BMIndexIP: strings.Clone(c.IP())})

		body, err = iframeTemplate.Render(map[string]string{"helloscript": app.GetConfig().PublishedHelloScriptFilename, "apipath": hostconf.APIPath, "h": hostconf.ID,
			"b": bounceS, "iframename": app.GetConfig().PublishedIFrameNoScriptFilename, "cookieprefix": hostconf.CookieOpts.CookiePrefix, "hulahost": hostconf.Host, "hulaurl": hostconf.GetExternalUrl()})
		if err != nil {
			return c.Status(500).SendString("error rendering iframe template: " + err.Error())
		}

		c.Set(fiber.HeaderContentType, fiber.MIMETextHTML)
		return c.SendString(body)

	}

	// }
	//	return c.SendString("ok")

}
