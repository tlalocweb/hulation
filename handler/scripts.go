package handler

import (
	"bytes"
	"net/url"

	"github.com/cbroglie/mustache"
	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/utils"
)

var helloJS *mustache.Template
var formsJS *mustache.Template

func HelloScriptFile(ctx RequestCtx) (err error) {
	ctx.SetHeader("Cache-Control", "no-cache, must-revalidate")

	if helloJS == nil {
		data, err := utils.ReadFileFromConfigPath(app.GetConfig().ScriptFolder, app.GetConfig().LocalHelloScriptFilename)
		if err != nil {
			log.Errorf("error reading script file (%s): %s", app.GetConfig().LocalHelloScriptFilename, err.Error())
		} else {
			helloJS, err = mustache.ParseString(string(data))
			if err != nil {
				helloJS = nil
				return ctx.Status(500).SendString("error parsing hello iframe template: " + err.Error())
			}
		}
	}

	if helloJS == nil {
		return ctx.Status(500).SendString("error no script file template")
	}

	var hostconf *config.Server
	var httperr int

	thisurl := ctx.Query("u")
	if len(thisurl) > 0 {
		thisurl, err = url.PathUnescape(thisurl)
		if err != nil {
			log.Errorf("error unescaping URL (script hello): %s", err.Error())
		}
		log.Debugf("saw url (script hello): %s", thisurl)
		hostconf, _, httperr, err = GetHostConfigFromUrl(thisurl)
	} else {
		hostconf, _, httperr, err = GetHostConfig(ctx)
	}

	if err != nil {
		return ctx.Status(httperr).SendString(err.Error())
	}
	log.Debugf("hostconf (hello script): %s", hostconf.Host)

	bounce := ctx.Query("b")
	if len(bounce) > 0 {
		log.Debugf("saw bounce val (script): %s", bounce)
	} else {
		log.Errorf("no bounce val (script)")
		return ctx.Status(404).SendString("404 Not Found - No bounce query param")
	}
	var buf bytes.Buffer
	err = helloJS.FRender(&buf, map[string]string{"apipath": hostconf.APIPath, "hostid": hostconf.ID, "visitorprefix": app.GetConfig().VisitorPrefix,
		"b": bounce, "cookieprefix": hostconf.CookieOpts.CookiePrefix, "visithost": hostconf.Host, "hulaurl": hostconf.GetExternalUrl(), "hulaapiurl": app.GetHulaOriginBaseUrl(), "thisurl": thisurl})
	if err != nil {
		return ctx.Status(500).SendString("error rendering hello script template: " + err.Error())
	}
	ctx.SetContentType("text/javascript")
	return ctx.SendBytes(buf.Bytes())
}

func FormsScriptFile(ctx RequestCtx) (err error) {
	ctx.SetHeader("Cache-Control", "no-cache, must-revalidate")
	if formsJS == nil {
		data, err := utils.ReadFileFromConfigPath(app.GetConfig().ScriptFolder, app.GetConfig().LocalFormsScriptFilename)
		if err != nil {
			log.Errorf("error reading script file (%s): %s", app.GetConfig().LocalFormsScriptFilename, err.Error())
		} else {
			formsJS, err = mustache.ParseString(string(data))
			if err != nil {
				formsJS = nil
				return ctx.Status(500).SendString("error parsing forms js template: " + err.Error())
			}
		}
	}

	if formsJS == nil {
		return ctx.Status(500).SendString("error no script file template")
	}

	hostconf, _, httperr, err := GetHostConfig(ctx)
	if err != nil {
		log.Errorf("error getting host config: %s", err.Error())
		return ctx.Status(httperr).SendString(err.Error())
	}

	var buf bytes.Buffer
	err = formsJS.FRender(&buf, map[string]string{"apipath": hostconf.APIPath,
		"hostid":       hostconf.ID,
		"cookieprefix": hostconf.CookieOpts.CookiePrefix,
		"hulahost":     hostconf.Host,
		"hulaurl":      hostconf.GetExternalUrl()})
	if err != nil {
		return ctx.Status(500).SendString("error rendering forms script template: " + err.Error())
	}
	ctx.SetContentType("text/javascript")
	return ctx.SendBytes(buf.Bytes())
}
