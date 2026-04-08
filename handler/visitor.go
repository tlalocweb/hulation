package handler

import (
	"net/url"
	"strings"

	"github.com/cbroglie/mustache"
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
	BMIndexHelloMsg = BMIndexData1
)

type HelloMsg struct {
	Bounce    string `json:"b"`
	EventCode int    `json:"e"`
	URL       string `json:"u"`
	Data      string `json:"d"`
}

var visitorBounceMap *model.BounceMap

func InitVisitorHandlers() {
	if visitorBounceMap == nil {
		visitorBounceMap = model.NewBounceMap(app.GetConfig().BounceTimeout)
	}
	visitorBounceMap.Start()
}

var precompileNewVisitorHooks = &utils.RunOnceSingleton{Run: func(p interface{}) (err error) {
	hostconf := p.(*config.Server)
	if hostconf.Hooks != nil {
		hostconf.Hooks.PrecompileHooksOnNewVisitor(map[string]any{"visitorid": "", "url": ""})
	}
	return
}}

// Hello is the basic API call made by the client anytime a visitor hits a page.
func Hello(ctx RequestCtx) error {
	var err error
	var hello HelloMsg

	err = ctx.BodyParser(&hello)
	if err != nil {
		return ctx.Status(400).SendString("bad parse: " + err.Error())
	}

	if len(hello.URL) > 0 {
		log.Debugf("saw url (hello): %s", hello.URL)
	}

	bounce := ctx.Query("b")
	if len(bounce) > 0 {
		log.Debugf("saw bounce val (hello): %s", bounce)
		if hello.Bounce != bounce {
			log.Errorf("bounce mismatch: %s != %s", hello.Bounce, bounce)
			return ctx.Status(400).SendString("bounce mismatch")
		}
	} else {
		log.Errorf("no bounce val (hello)")
		return ctx.Status(404).SendString("404 Not Found - No bounce query param")
	}

	hconf, host, httperr, err := GetHostConfig(ctx)

	precompileNewVisitorHooks.Verify(host, hconf, "error precompiling new visitor hooks (hello)")

	if err != nil {
		return ctx.Status(httperr).SendString(err.Error())
	}

	ctx.SetContentType("application/json")
	ctx.SetHeader("Cache-Control", "no-cache, no-store, must-revalidate")
	SetCSP(ctx, hconf)

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
		err = b.Visitor.Commit(model.GetDB())
		if err != nil {
			log.Errorf("error committing visitor: %s", err.Error())
		}
		hello := b.Data[BMIndexHelloMsg].(*HelloMsg)
		if hello.EventCode == model.EventCodePageView {
			if b.Flags&VisitBounceFlagSawURL == 0 {
				url := hello.URL
				host, urlpath := utils.GetURLHostPath(url)
				log.Debugf("saw url (hello): %s (%s)", url, urlpath)
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
	}, map[uint32]interface{}{BMIndexHelloMsg: &hello, BMIndexUA: strings.Clone(ctx.Header("User-Agent")), BMIndexIP: strings.Clone(ctx.IP()), BMHostConf: hconf})

	return ctx.SendString(`{"status":"ok","vc":"` + sscookie + `"}`)
}

const iframehello = `<!doctype html>
<html lang=en>
<head><meta charset=utf-8><title>ha</title>
<script type="text/javascript" src="{{hulaurl}}/scripts/{{helloscript}}?b={{b}}&u={{thisurl}}"></script>
</head>
<body></body>
</html>`

var iframeHelloTemplate *mustache.Template

// HelloIframe handles the normal (not noscript) iframe load.
// Sets visitor cookies and renders iframe HTML with the hello script.
func HelloIframe(ctx RequestCtx) error {
	var err error
	if iframeHelloTemplate == nil {
		iframeHelloTemplate, err = mustache.ParseString(iframehello)
		if err != nil {
			return ctx.Status(500).SendString("error parsing hello iframe template: " + err.Error())
		}
	}

	ctx.SetHeader("Cache-Control", "no-cache, no-store, must-revalidate")

	var thisurl string
	thisurlescaped := ctx.Query("u")
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
	}

	SetCSP(ctx, hostconf)
	id := ctx.Query("h")
	if id != hostconf.ID {
		log.Errorf("host id mismmatch: %s != %s", id, hostconf.ID)
		return ctx.Status(400).SendString("host id mismmatch")
	}
	precompileNewVisitorHooks.Verify(host, hostconf, "error precompiling new visitor hooks (helloiframe)")

	var cookiebaton VisitorCookiesBaton
	visitor, _, err := GetOrSetVisitor(ctx, hostconf, &cookiebaton)

	if err != nil {
		resperr, ok := err.(*ResponseError)
		if ok {
			return ctx.Status(resperr.StatusCode).SendString(resperr.Error())
		}
		return ctx.Status(500).SendString("error getting or setting visitor: " + err.Error())
	}

	var body string
	bounceS, _ := visitorBounceMap.NewBounceWithVisitor(visitor, func(b *model.Bounce) (err error) {
		err = b.Visitor.Commit(model.GetDB())
		if err != nil {
			log.Errorf("error committing visitor: %s", err.Error())
		}
		cookiem := b.Data[BMIndexCookieM].(*model.VisitorCookie)
		if cookiem != nil {
			err = cookiem.Commit(model.GetDB())
			if err != nil {
				log.Errorf("error committing cookie: %s", err.Error())
			}
		}
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
	}, map[uint32]interface{}{BMIndexURL: strings.Clone(thisurl), BMIndexCookieM: cookiebaton.Cookiem, BMIndexSSCookieM: cookiebaton.Sscookiem,
		BMIndexUA: strings.Clone(ctx.Header("User-Agent")), BMIndexIP: strings.Clone(ctx.IP()), BMHostConf: hostconf})

	hulaurl, err := utils.GetBaseUrl(ctx.OriginalURL())
	if err != nil {
		log.Errorf("error parsing original URL: %s - using hostconf url", err.Error())
		hulaurl = hostconf.GetExternalUrl()
	}
	body, err = iframeHelloTemplate.Render(map[string]string{"helloscript": app.GetConfig().PublishedHelloScriptFilename, "apipath": hostconf.APIPath, "h": hostconf.ID,
		"b": bounceS, "cookieprefix": hostconf.CookieOpts.CookiePrefix, "visithost": hostconf.Host, "hulaurl": hulaurl, "thisurl": thisurl})
	if err != nil {
		return ctx.Status(500).SendString("error rendering iframe template: " + err.Error())
	}

	ctx.SetContentType("text/html")
	return ctx.SendString(body)
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

// HelloNoScript handles the noscript/iframe way of tracking visitors.
func HelloNoScript(ctx RequestCtx) error {
	var err error
	if iframeTemplate == nil {
		iframeTemplate, err = mustache.ParseString(iframe)
		if err != nil {
			return ctx.Status(500).SendString("error parsing hello iframe template: " + err.Error())
		}
	}
	ctx.SetHeader("Cache-Control", "no-cache, no-store, must-revalidate")

	hostconf, _, httperr, err := GetHostConfig(ctx)
	if err != nil {
		return ctx.Status(httperr).SendString(err.Error())
	}

	id := ctx.Query("h")
	if id != hostconf.ID {
		urlU := ctx.Query("u")
		if len(urlU) < 1 {
			return ctx.Status(400).SendString("host id mismmatch")
		}
		parsed, err := url.Parse(urlU)
		if err != nil {
			return ctx.Status(400).SendString("could not parse u param: " + err.Error())
		}
		hostconf = app.GetConfig().GetServerByAnyAlias(parsed.Host)
		if hostconf == nil {
			return ctx.Status(400).SendString("unknown host (2)")
		}
		if id != hostconf.ID {
			return ctx.Status(400).SendString("host id mismmatch (2)")
		}
	}

	SetCSP(ctx, hostconf)
	bouncid := ctx.Query("b")

	refurl := ctx.Referer()

	if len(bouncid) < 1 && len(refurl) > 0 {
		log.Debugf("saw url (hellonoscript / referer): %s", refurl)
	}

	// if there is bounceid, then we are reporting a bounceback
	if len(bouncid) > 0 {

		visitorBounceMap.ReportBounceBack(bouncid, func(b *model.Bounce) (err error) {
			err = b.Visitor.Commit(model.GetDB())
			if err != nil {
				log.Errorf("error committing visitor: %s", err.Error())
			}
			return err
		})

		ctx.SetContentType("text/html")
		return ctx.SendString(iframe2)

	}

	var cookiebaton VisitorCookiesBaton
	visitor, _, err := GetOrSetVisitor(ctx, hostconf, &cookiebaton)

	var body string
	bounceS, _ := visitorBounceMap.NewBounceWithVisitor(visitor, func(b *model.Bounce) (err error) {
		err = b.Visitor.Commit(model.GetDB())
		if err != nil {
			log.Errorf("error committing visitor: %s", err.Error())
		}
		cookiem := b.Data[BMIndexCookieM].(*model.VisitorCookie)
		if cookiem != nil {
			err = cookiem.Commit(model.GetDB())
			if err != nil {
				log.Errorf("error committing cookie: %s", err.Error())
			}
		}
		sscookiem := b.Data[BMIndexSSCookieM].(*model.VisitorCookie)
		if sscookiem != nil {
			err = sscookiem.Commit(model.GetDB())
			if err != nil {
				log.Errorf("error committing sscookie: %s", err.Error())
			}
		}
		burl := b.Data[BMIndexURL]
		if len(burl.(string)) > 0 {
			host, urlpath := utils.GetURLHostPath(burl.(string))
			log.Debugf("saw url (hellonoscript): %s (%s)", burl, urlpath)
			ev := model.NewEvent(model.EventCodePageView)
			ev.SetURL(burl.(string))
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

	}, map[uint32]interface{}{BMIndexURL: strings.Clone(refurl), BMIndexCookieM: cookiebaton.Cookiem, BMIndexSSCookieM: cookiebaton.Sscookiem,
		BMIndexUA: strings.Clone(ctx.Header("User-Agent")), BMIndexIP: strings.Clone(ctx.IP())})

	body, err = iframeTemplate.Render(map[string]string{"helloscript": app.GetConfig().PublishedHelloScriptFilename, "apipath": hostconf.APIPath, "h": hostconf.ID,
		"b": bounceS, "iframename": app.GetConfig().PublishedIFrameNoScriptFilename, "cookieprefix": hostconf.CookieOpts.CookiePrefix, "hulahost": hostconf.Host, "hulaurl": hostconf.GetExternalUrl()})
	if err != nil {
		return ctx.Status(500).SendString("error rendering iframe template: " + err.Error())
	}

	ctx.SetContentType("text/html")
	return ctx.SendString(body)
}
