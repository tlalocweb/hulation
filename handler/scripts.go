package handler

import (
	"bytes"

	"github.com/cbroglie/mustache"
	"github.com/gofiber/fiber/v2"
	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/utils"
)

// we keep the script in memory for fast processing
var helloJS *mustache.Template
var formsJS *mustache.Template

func HelloScriptFile(c *fiber.Ctx) (err error) {
	//host := c.Get("Host")
	c.Set(fiber.HeaderCacheControl, "no-cache, must-revalidate")

	if helloJS == nil {
		var script string
		// read the file using the golang std lib
		// we put this in a file (vs in the code here) to make it easier to customize
		data, err := utils.ReadFileFromConfigPath(app.GetConfig().ScriptFolder, app.GetConfig().LocalHelloScriptFilename)
		if err != nil {
			log.Errorf("error reading script file (%s): %s", app.GetConfig().LocalHelloScriptFilename, err.Error())
		} else {
			script = string(data)
			helloJS, err = mustache.ParseString(script)
			if err != nil {
				helloJS = nil
				return c.Status(500).SendString("error parsing hello iframe template: " + err.Error())
			}
		}
	}

	if helloJS == nil {
		return c.Status(500).SendString("error no script file template")
	}

	hostconf, _, httperr, err := GetHostConfig(c)

	if err != nil {
		return c.Status(httperr).SendString(err.Error())
	}

	bounce := c.Query("b")
	if len(bounce) > 0 {
		log.Debugf("saw bounce val (script): %s", bounce)
	} else {
		log.Errorf("no bounce val (script)")
		return c.Status(404).SendString("404 Not Found - No bounce query param")
	}
	var buf bytes.Buffer
	err = helloJS.FRender(&buf, map[string]string{"apipath": hostconf.APIPath, "hostid": hostconf.ID,
		"b": bounce, "cookieprefix": hostconf.CookieOpts.CookiePrefix, "hulahost": hostconf.Host, "hulaurl": hostconf.GetExternalUrl()})
	if err != nil {
		return c.Status(500).SendString("error rendering hello script template: " + err.Error())
	}
	// https://developer.mozilla.org/en-US/docs/Web/HTTP/Basics_of_HTTP/MIME_types#textjavascript
	c.Context().SetContentType("text/javascript")
	c.Send(buf.Bytes())
	return
}

func FormsScriptFile(c *fiber.Ctx) (err error) {
	//host := c.Get("Host")
	c.Set(fiber.HeaderCacheControl, "no-cache, must-revalidate")
	if formsJS == nil {
		var script string
		// read the file using the golang std lib
		// we put this in a file (vs in the code here) to make it easier to customize
		data, err := utils.ReadFileFromConfigPath(app.GetConfig().ScriptFolder, app.GetConfig().LocalFormsScriptFilename)
		if err != nil {
			log.Errorf("error reading script file (%s): %s", app.GetConfig().LocalFormsScriptFilename, err.Error())
		} else {
			script = string(data)
			formsJS, err = mustache.ParseString(script)
			if err != nil {
				formsJS = nil
				return c.Status(500).SendString("error parsing forms js template: " + err.Error())
			}
		}
	}

	if formsJS == nil {
		return c.Status(500).SendString("error no script file template")
	}

	hostconf, _, _, err := GetHostConfig(c)
	// httperr
	if err != nil {
		//		return c.Status(httperr).SendString(err.Error())
	}

	// bounce := c.Query("b")
	// if len(bounce) > 0 {
	// 	log.Debugf("saw bounce val (script): %s", bounce)
	// } else {
	// 	log.Errorf("no bounce val (script)")
	// 	return c.Status(404).SendString("404 Not Found - No bounce query param")
	// }
	var buf bytes.Buffer
	err = formsJS.FRender(&buf, map[string]string{"apipath": hostconf.APIPath, "hostid": hostconf.ID,
		//"b": bounce,
		"cookieprefix": hostconf.CookieOpts.CookiePrefix, "hulahost": hostconf.Host, "hulaurl": hostconf.GetExternalUrl()})
	if err != nil {
		return c.Status(500).SendString("error rendering forms script template: " + err.Error())
	}
	// https://developer.mozilla.org/en-US/docs/Web/HTTP/Basics_of_HTTP/MIME_types#textjavascript
	c.Context().SetContentType("text/javascript")
	c.Send(buf.Bytes())
	return
}
