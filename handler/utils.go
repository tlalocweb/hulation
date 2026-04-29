package handler

import (
	"fmt"
	"net/url"

	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"
	"github.com/tlalocweb/hulation/pkg/store/storage"
	"github.com/tlalocweb/hulation/pkg/visitorid"
	"github.com/tlalocweb/hulation/utils"
)

// MintVisitor returns the visitor for this request, branching on
// the per-server tracking_mode. In "cookieless" mode it skips the
// cookie handshake entirely and derives a transient visitor id from
// (per-server salt, day, IP, UA). In "cookie" mode it delegates to
// GetOrSetVisitor which preserves the existing flow.
//
// Phase 4c.3.
func MintVisitor(ctx RequestCtx, hostconf *config.Server, baton *VisitorCookiesBaton) (visitor *model.Visitor, newvisitor bool, err error) {
	if hostconf == nil {
		err = &ResponseError{StatusCode: 500, RootCause: fmt.Errorf("hostconf is nil")}
		return
	}
	mode := hostconf.TrackingMode
	if mode == "cookieless" {
		// Derived id, no persistent visitor row.
		s := storage.Global()
		if s == nil {
			err = &ResponseError{StatusCode: 500, RootCause: fmt.Errorf("storage not initialised")}
			return
		}
		salt, sErr := hulabolt.GetOrCreateCookielessSalt(ctx.Context(), s, hostconf.ID)
		if sErr != nil {
			log.Errorf("cookieless: GetOrCreateCookielessSalt(%s): %s", hostconf.ID, sErr.Error())
			err = &ResponseError{StatusCode: 500, RootCause: sErr}
			return
		}
		id, dErr := visitorid.DeriveNow(salt, ctx.IP(), ctx.Header("User-Agent"))
		if dErr != nil {
			err = &ResponseError{StatusCode: 500, RootCause: dErr}
			return
		}
		visitor = model.NewVisitor()
		visitor.ID = id
		newvisitor = true
		// No baton fill — there are no cookies in cookieless mode.
		return
	}
	// Default: cookie path.
	return GetOrSetVisitor(ctx, hostconf, baton)
}

// GetHostConfig resolves the server configuration for the current request
// by examining the Host header, query params, and cached locals.
func GetHostConfig(ctx RequestCtx) (hostconf *config.Server, host string, httperror int, err error) {
	exist := ctx.Locals("hostconf")
	if exist != nil {
		log.Debugf("GetHostConfig: hostconf exist")
		hostconf = exist.(*config.Server)
		exist2 := ctx.Locals("host")
		if exist2 != nil {
			host = exist2.(string)
			return
		}
		log.Debugf("GetHostConfig: hostconf exist but no host")
	}
	// does the origin (o) query param exist?
	oquery := ctx.Query("o")
	var hostonly string
	if len(oquery) > 0 {
		log.Debugf("GetHostConfig: see 'o' query: %s", oquery)
		oquery, err = url.PathUnescape(oquery)
		if err != nil {
			log.Errorf("error unescaping o query: %s", err.Error())
		}
		hostonly = utils.GetHostOnlyFromHostPort(oquery)
		hostconf = app.GetConfig().GetServerByAnyAlias(hostonly)
	}
	// does the usehost local exist?
	usehost := ctx.Locals("usehost")
	if usehost != nil {
		hostonly = usehost.(string)
		log.Debugf("GetHostConfig: usehost: %s", hostonly)
		hostconf = app.GetConfig().GetServerByAnyAlias(hostonly)
	}
	if hostconf == nil {
		host = ctx.Header("Host")
		// HTTP/2 uses the :authority pseudo-header which Go puts in r.Host,
		// not in the Header map. Fall back to Hostname() for H2 requests.
		if len(host) == 0 {
			host = ctx.Hostname()
		}
		log.Debugf("GetHostConfig: host: %s", host)
		hostonly = utils.GetHostOnlyFromHostPort(host)
		hostconf = app.GetConfig().GetServerByAnyAlias(hostonly)
	}
	if hostconf != nil {
		log.Debugf("GetHostConfig: found host: %s", hostconf.Host)
		if !hostconf.RespectPortInLookup() {
			host = hostonly
		}
	} else {
		log.Securityf("GetHostConfig: unknown host %q from %s", host, ctx.IP())
		httperror = 404
		err = fmt.Errorf("unknown host: %s", host)
		return
	}
	// cache for later use
	ctx.SetLocals("hostconf", hostconf)
	ctx.SetLocals("host", host)
	return
}

// GetHostConfigFromUrl resolves a server config from a URL string.
func GetHostConfigFromUrl(u string) (hostconf *config.Server, host string, httperror int, err error) {
	var hostonly string
	hostonly, err = utils.GetHostFromUrl(u)
	if err != nil {
		httperror = 500
		return
	}
	hostconf = app.GetConfig().GetServerByAnyAlias(hostonly)
	if hostconf != nil {
		host = hostonly
	} else {
		httperror = 404
		err = fmt.Errorf("unknown host: %s", hostonly)
	}
	return
}

// AreWeServingThePage checks if the current request's referrer matches the host.
func AreWeServingThePage(ctx RequestCtx, hostconf *config.Server) bool {
	referrer := ctx.Header("Referer")
	if referrer == "" {
		return false
	}
	rUrl, err := url.Parse(referrer)
	if err != nil {
		log.Errorf("error parsing referrer: %s", err.Error())
		return false
	}
	rHost := ctx.Header("Host")
	if !hostconf.RespectPortInLookup() {
		if rUrl.Hostname() != utils.GetHostOnlyFromHostPort(rHost) {
			return false
		}
	} else {
		if rUrl.Host != rHost {
			return false
		}
	}
	return true
}

// SetCSP sets the Content-Security-Policy header from the server config.
func SetCSP(ctx RequestCtx, hostconf *config.Server) {
	cspmap := hostconf.GetCSPMap()
	policy := ""
	for k, v := range cspmap {
		policy = fmt.Sprintf("%s; %s %s", policy, k, v)
	}
	ctx.SetHeader("Content-Security-Policy", policy)
}

// GetOrSetVisitor reads visitor cookies, looks up or creates a visitor, and sets cookies on the response.
func GetOrSetVisitor(ctx RequestCtx, hostconf *config.Server, baton *VisitorCookiesBaton) (visitor *model.Visitor, newvisitor bool, err error) {
	if hostconf == nil {
		err = &ResponseError{StatusCode: 500, RootCause: fmt.Errorf("hostconf is nil")}
		return
	}
	// check for httponly cookie
	sscookie := ctx.Cookie(hostconf.CookieOpts.CookiePrefix + "_helloss")
	if len(sscookie) > 0 {
		log.Debugf("saw sscookie (helloiframe): %s", sscookie)
		visitor, err = model.GetVisitorBySSCookie(model.GetDB(), sscookie)
		if err != nil {
			err = &ResponseError{StatusCode: 500, RootCause: fmt.Errorf("error getting visitor by sscookie: %s", err.Error())}
			return
		}
		if visitor != nil {
			log.Debugf("visitor seen by sscookie: %s", visitor.ID)
		} else {
			log.Debugf("no known visitor by sscookie")
		}
	}
	// check for normal cookie
	cookie := ctx.Cookie(hostconf.CookieOpts.CookiePrefix + "_hello")
	if visitor == nil && len(cookie) > 0 {
		log.Debugf("saw cookie (helloiframe): %s", cookie)
		visitor, err = model.GetVisitorByCookie(model.GetDB(), cookie)
		if err != nil {
			err = &ResponseError{StatusCode: 500, RootCause: fmt.Errorf("error getting visitor by sscookie: %s", err.Error())}
			return
		}
		if visitor != nil {
			log.Debugf("visitor seen by cookie (helloiframe): %s", visitor.ID)
		} else {
			log.Debugf("no known visitor by cookie")
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
		}
		cookie = cookiem.Cookie
		log.Debugf("new cookie (helloiframe): %s", cookie)
	} else {
		log.Debugf("CookieFromCookieVal: %s", cookie)
		cookiem, err = model.CookieFromCookieVal(model.GetDB(), cookie, visitor)
		if err != nil {
			log.Errorf("error getting cookie from cookie val: %s", err.Error())
			cookiem, err = visitor.NewVisitorCookie()
			if err != nil {
				log.Errorf("error creating cookie: %s", err.Error())
			}
			log.Debugf("new cookie (helloiframe): %s", cookie)
		}
		cookie = cookiem.Cookie
		log.Debugf("CookieFromCookieVal (new?): %s", cookie)
	}

	if baton != nil {
		baton.Cookiem = cookiem
	}

	if len(sscookie) < 1 {
		sscookiem, err = visitor.NewVisitorSSCookie()
		if err != nil {
			err = &ResponseError{StatusCode: 500, RootCause: fmt.Errorf("error creating cookies: %s", err.Error())}
			return
		}
		sscookie = sscookiem.Cookie
		log.Debugf("new sscookie (helloiframe): %s", sscookie)
	} else {
		log.Debugf("SSCookieFromSSCookieVal: %s", sscookie)
		sscookiem, err = model.SSCookieFromSSCookieVal(model.GetDB(), sscookie, visitor)
		if err != nil {
			log.Errorf("error getting sscookie from sscookie val: %s", err.Error())
			sscookiem, err = visitor.NewVisitorSSCookie()
			if err != nil {
				log.Errorf("error creating sscookie: %s", err.Error())
			}
			log.Debugf("new cookie (helloiframe): %s", cookie)
		}
		sscookie = sscookiem.Cookie
		log.Debugf("SSCookieFromSSCookieVal (new?): %s", sscookie)
	}
	if baton != nil {
		baton.Sscookiem = sscookiem
	}
	samesite := hostconf.CookieOpts.SameSite
	if len(samesite) < 1 {
		if AreWeServingThePage(ctx, hostconf) {
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
	ctx.SetCookie(&Cookie{
		Name:     hostconf.CookieOpts.CookiePrefix + "_hello",
		Value:    cookie,
		Secure:   !hostconf.CookieOpts.NoSecure,
		HTTPOnly: false,
		SameSite: samesite,
		Domain:   domain,
		MaxAge:   60 * 60 * 24 * hostconf.HelloCookieMaxAge, // 30 days
	})
	ctx.SetCookie(&Cookie{
		Name:     hostconf.CookieOpts.CookiePrefix + "_helloss",
		Value:    sscookie,
		Secure:   !hostconf.CookieOpts.NoSecure,
		HTTPOnly: true,
		Domain:   domain,
		SameSite: samesite,
	})

	return
}

// GetVisitorFromContext reads visitor cookies and looks up the visitor, without setting cookies.
func GetVisitorFromContext(ctx RequestCtx, hostconf *config.Server) (visitor *model.Visitor, sscookiem *model.VisitorCookie, cookiem *model.VisitorCookie, errresp *ResponseError) {
	var err error
	var cookieprefix string
	if hostconf != nil {
		cookieprefix = hostconf.CookieOpts.CookiePrefix
	}
	if len(cookieprefix) < 1 {
		cookieprefix = "hula"
	}

	cookie := ctx.Cookie(cookieprefix + "_hello")
	sscookie := ctx.Cookie(cookieprefix + "_helloss")

	if len(sscookie) > 0 {
		log.Debugf("saw sscookie (hellonoscript): %s", sscookie)
		visitor, err = model.GetVisitorBySSCookie(model.GetDB(), sscookie)
		if err != nil {
			errresp = &ResponseError{StatusCode: 500, RootCause: fmt.Errorf("error getting visitor by sscookie: %w", err)}
			return
		}
		if visitor != nil {
			log.Debugf("visitor seen by sscookie: %s", visitor.ID)
		} else {
			log.Debugf("no known visitor by sscookie")
		}
	}
	if visitor == nil && len(cookie) > 0 {
		log.Debugf("saw cookie (hellonoscript): %s", cookie)
		visitor, err = model.GetVisitorByCookie(model.GetDB(), cookie)
		if err != nil {
			errresp = &ResponseError{StatusCode: 500, RootCause: fmt.Errorf("error getting visitor by cookie: %w", err)}
		}
		if visitor != nil {
			log.Debugf("visitor seen by cookie (helloiframe): %s", visitor.ID)
		} else {
			log.Debugf("no known visitor by cookie")
		}
	}

	if len(cookie) < 1 {
		cookiem, err = visitor.NewVisitorCookie()
		if err != nil {
			log.Errorf("error creating cookie: %s", err.Error())
		}
		cookie = cookiem.Cookie
		log.Debugf("new cookie (hellonoscript): %s", cookie)
	} else {
		log.Debugf("CookieFromCookieVal: %s", cookie)
		cookiem, err = model.CookieFromCookieVal(model.GetDB(), cookie, visitor)
		if err != nil {
			log.Errorf("error getting cookie from cookie val: %s", err.Error())
			cookiem, err = visitor.NewVisitorCookie()
			if err != nil {
				log.Errorf("error creating cookie: %s", err.Error())
			}
			log.Debugf("new cookie (hellonoscript): %s", cookie)
		}
		cookie = cookiem.Cookie
		log.Debugf("CookieFromCookieVal (new?): %s", cookie)
	}
	return
}
