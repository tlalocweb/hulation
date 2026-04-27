package handler

import (
	"fmt"
	"net/http"

	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/utils"
)

// CheckRedirectAlias checks if the incoming request's hostname is a redirect alias.
// If so, it sends a 301 Moved Permanently redirect to the primary host, preserving
// the original path and query string. Returns true if a redirect was sent.
func CheckRedirectAlias(ctx RequestCtx) (redirected bool, err error) {
	hostconf, _, _, err := GetHostConfig(ctx)
	if err != nil || hostconf == nil {
		return false, nil
	}
	// Use Host header, falling back to Hostname() for HTTP/2 (:authority pseudo-header)
	reqHostRaw := ctx.Header("Host")
	if len(reqHostRaw) == 0 {
		reqHostRaw = ctx.Hostname()
	}
	requestHost := utils.GetHostOnlyFromHostPort(reqHostRaw)
	if !hostconf.IsRedirectAlias(requestHost) {
		return false, nil
	}
	target := fmt.Sprintf("%s://%s%s", hostconf.HttpScheme, hostconf.Host, ctx.OriginalURL())
	log.Debugf("redirect alias: %s -> %s", requestHost, target)
	err = ctx.Redirect(target, http.StatusMovedPermanently)
	return true, err
}
