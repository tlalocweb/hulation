package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
	"github.com/tlalocweb/hulation/utils"
	"gorm.io/gorm"
)

type LanderReq struct {
	ID                string `json:"id"`
	Server            string `json:"server"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	RequestUrlPostfix string `json:"request_url_postfix"`
	Redirect          string `json:"redirect"`
	IgnorePort        *bool  `json:"ignore_port,omitempty"`
	NoServe           *bool  `json:"no_serve,omitempty"`
}

type LanderPostResp struct {
	FinalUrl string `json:"final_url"`
	ID       string `json:"id"`
}

var landerOptimizeRunner *utils.RunOnceMaxInterval
var landerRunnerCommit *utils.DeferredRunner

var precompileLanderVisitHooks = &utils.RunOnceSingleton{Run: func(p interface{}) (err error) {
	hostconf := p.(*config.Server)
	if hostconf.Hooks != nil {
		hostconf.Hooks.PrecompileHooksOnNewLanderVisit(map[string]any{"visitorid": "", "url": "", "landerid": "", "newvisitor": false,
			"landerdescription": "", "landercount": 0, "landerdbname": ""})
	}
	return
}}

func init() {
	landerOptimizeRunner = utils.NewRunOnceMaxInterval(5 * time.Second)
	landerRunnerCommit = utils.NewDeferredRunner("landerRunnerCommit")
	landerRunnerCommit.Start()
}

func LanderCreate(ctx RequestCtx) (err error) {
	var landerreq LanderReq
	body := ctx.Body()
	err = json.Unmarshal(body, &landerreq)
	if err != nil {
		return ctx.Status(400).SendString("bad parse: " + err.Error())
	}

	lander := model.NewLander()
	lander.Name = landerreq.Name
	lander.Description = landerreq.Description
	lander.Server = landerreq.Server
	lander.Redirect = landerreq.Redirect
	if landerreq.IgnorePort != nil {
		lander.IgnorePort = *landerreq.IgnorePort
	}
	if landerreq.NoServe != nil {
		lander.NoServe = *landerreq.NoServe
	}

	inst, err := lander.Commit(landerreq.RequestUrlPostfix, model.GetDB())
	if err != nil {
		log.Errorf("error committing lander form model: %s", err.Error())
		resp := &ResponseError{StatusCode: 500, RootCause: fmt.Errorf("error committing lander: %v", err)}
		if _, ok := err.(*model.ValidationError); ok {
			resp.StatusCode = 400
			resp.RootCause = fmt.Errorf("error comitting lander (validation): %v", err)
			return ctx.Status(resp.StatusCode).SendString(resp.JsonBody())
		}
		return ctx.Status(resp.StatusCode).SendString(resp.JsonBody())
	}

	return ctx.Status(201).SendJSON(&LanderPostResp{FinalUrl: inst.GetFinalUrl(), ID: lander.ID})
}

func LanderModify(ctx RequestCtx) (err error) {
	landerid := ctx.Param("landerid")
	if len(landerid) == 0 {
		return ctx.Status(404).SendString("404 Not Found - No landerid")
	}

	var landerreq LanderReq
	body := ctx.Body()
	err = json.Unmarshal(body, &landerreq)
	if err != nil {
		return ctx.Status(400).SendString("bad parse: " + err.Error())
	}

	lander, _, err := model.GetLanderById(model.GetDB(), landerid)
	if err != nil {
		log.Errorf("error getting form model: %s", err.Error())
		return ctx.Status(500).SendString("error getting lander model: " + err.Error())
	}
	if lander == nil {
		return ctx.Status(404).SendString("404 Not Found - No lander model by id " + landerid)
	}
	if len(landerreq.Server) > 0 {
		lander.Server = landerreq.Server
	}
	if len(landerreq.Name) > 0 {
		lander.Name = landerreq.Name
	}
	if len(landerreq.Description) > 0 {
		lander.Description = landerreq.Description
	}
	if len(landerreq.Redirect) > 0 {
		lander.Redirect = landerreq.Redirect
	}
	if landerreq.IgnorePort != nil {
		lander.IgnorePort = *landerreq.IgnorePort
	}
	if landerreq.NoServe != nil {
		lander.NoServe = *landerreq.NoServe
	}

	inst, err := lander.Commit(landerreq.RequestUrlPostfix, model.GetDB())
	if err != nil {
		log.Errorf("error committing lander form model: %s", err.Error())
		resp := &ResponseError{StatusCode: 500, RootCause: fmt.Errorf("error committing lander: %v", err)}
		if _, ok := err.(*model.ValidationError); ok {
			resp.StatusCode = 400
			resp.RootCause = fmt.Errorf("error comitting lander (validation): %v", err)
			return ctx.Status(resp.StatusCode).SendString(resp.JsonBody())
		}
		return ctx.Status(resp.StatusCode).SendString(resp.JsonBody())
	}

	landerOptimizeRunner.Run(func() (err error) {
		err = model.OptimizeLanderModels(model.GetDB())
		if err != nil {
			log.Errorf("error optimizing lander model: %s", err.Error())
		}
		return
	})

	return ctx.Status(201).SendJSON(&LanderPostResp{FinalUrl: inst.GetFinalUrl(), ID: lander.ID})
}

func LanderDelete(ctx RequestCtx) (err error) {
	landerid := ctx.Param("landerid")
	if len(landerid) == 0 {
		return ctx.Status(404).SendString("404 Not Found - No landerid")
	}
	err = model.DeleteLander(model.GetDB(), landerid)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return ctx.Status(404).SendString("404 Not Found - No lander by id " + landerid)
		}
		return ctx.Status(500).SendString("error deleting lander: " + err.Error())
	}
	return ctx.Status(200).SendString("lander deleted")
}

func DoLanding(ctx RequestCtx) (err error) {
	landerid := ctx.Param("landerid")
	log.Debugf("DoLanding: %s", landerid)
	if len(landerid) == 0 {
		return ctx.Status(404).SendString("404 Not Found - No landerid")
	}
	lander, inst, err := model.GetLanderByUrlPostfix(model.GetDB(), landerid)
	if err != nil {
		log.Errorf("error getting lander: %s", err.Error())
		return ctx.Status(500).SendString("error getting lander: " + err.Error())
	}
	hostconf, host, _, err := GetHostConfig(ctx)
	if hostconf != nil {
		precompileLanderVisitHooks.Verify(host, hostconf, "precompileLanderVisitHooks error")
	} else {
		if err != nil {
			log.Errorf("lander: Error getting GetHostConfig: %s", err.Error())
		} else {
			log.Errorf("lander: Error getting GetHostConfig: hostconf is nil")
		}
	}

	if lander == nil {
		return ctx.Status(404).SendString("404 Not Found - No lander by id " + landerid)
	}

	landerRunnerCommit.Run(func() (err error) {
		err = lander.AddHit(model.GetDB())
		if err != nil {
			err = fmt.Errorf("error committing lander: %s", err.Error())
		}
		landerOptimizeRunner.Run(func() (err error) {
			err = model.OptimizeLanderModels(model.GetDB())
			if err != nil {
				log.Errorf("error optimizing lander model: %s", err.Error())
			}
			return
		})
		return
	})

	requrl := ctx.OriginalURL()
	visitor, newvisitor, err2 := GetOrSetVisitor(ctx, hostconf, nil)
	if err2 != nil {
		log.Errorf("error getting or setting visitor for lander: %s", err2.Error())
	}
	visitorid := ""
	if visitor != nil {
		visitorid = visitor.ID
	}
	if hostconf != nil {
		hostconf.Hooks.SubmitToHooksOnNewLanderVisit(map[string]any{"visitorid": visitorid, "url": strings.Clone(requrl), "landerid": strings.Clone(landerid), "newvisitor": newvisitor,
			"landerdescription": lander.Description, "landercount": lander.Hits, "landerdbname": lander.Name}, nil, nil)
	}
	if inst == nil {
		return ctx.Status(500).SendString("500 - error getting lander instance " + landerid)
	}

	ctx.SetHeader("Cache-Control", "no-cache, must-revalidate, s-maxage=0, max-age=120, private")
	ok, redirect := inst.DoRedirect()
	if ok {
		return ctx.Redirect(redirect)
	}
	ok, _ = inst.DoStatic()
	if ok {
		err := ctx.SendFile(http.Dir(inst.GetFsRoot()), inst.GetStaticPath())
		if err != nil {
			return ctx.Status(404).SendString("File not found")
		}
		return nil
	}
	return ctx.Status(500).SendString("500 - error handling lander " + landerid)
}

func DoLandingHit(ctx RequestCtx) (err error) {
	hostconf, _, _, err := GetHostConfig(ctx)
	if err != nil {
		log.Warnf("Error getting GetHostConfig: %s", err.Error())
	}

	landerid := ctx.Param("landerid")
	if len(landerid) == 0 {
		return ctx.Status(404).SendString("404 Not Found - No landerid")
	}
	lander, _, err := model.GetLanderById(model.GetDB(), landerid)
	if err != nil {
		log.Errorf("error getting lander: %s", err.Error())
		return ctx.Status(500).SendString("error getting lander: " + err.Error())
	}
	if lander == nil {
		return ctx.Status(404).SendString("404 Not Found - No lander by id " + landerid)
	}

	v, _, _, err_resp := GetVisitorFromContext(ctx, hostconf)
	if err_resp != nil {
		return err_resp.Send(ctx)
	}

	requrl := strings.Clone(ctx.OriginalURL())
	host := hostconf.Host
	ua := ctx.Header("User-Agent")
	ip := ctx.IP()

	visitor, newvisitor, err2 := GetOrSetVisitor(ctx, hostconf, nil)
	if err2 != nil {
		log.Errorf("error getting or setting visitor for lander: %s", err2.Error())
	}
	visitorid := ""
	if visitor != nil {
		visitorid = visitor.ID
	}

	if hostconf != nil {
		hostconf.Hooks.SubmitToHooksOnNewLanderVisit(map[string]any{"visitorid": visitorid, "url": fmt.Sprintf("Referer:%s", ctx.Referer()), "landerid": strings.Clone(landerid), "newvisitor": newvisitor,
			"landerdescription": lander.Description, "landercount": lander.Hits, "landerdbname": lander.Name}, nil, nil)
	}

	landerRunnerCommit.Run(func() (err error) {
		err = lander.AddHit(model.GetDB())
		if err != nil {
			err = fmt.Errorf("error committing lander: %s", err.Error())
		}
		ev := model.NewEvent(model.EventCodeLanderHit)
		ev.SetURL(requrl)
		u, err := url.Parse(requrl)
		if err != nil {
			log.Errorf("error parsing url: %s", err.Error())
		}
		ev.SetUrlPath(u.Path)
		ev.SetHost(host)
		ev.SetBrowserUA(ua)
		ev.SetFromIP(ip)
		err = ev.CommitTo(model.GetDB(), v)
		if err != nil {
			log.Errorf("error committing event: %s", err.Error())
		}
		landerOptimizeRunner.Run(func() (err error) {
			err = model.OptimizeLanderModels(model.GetDB())
			if err != nil {
				log.Errorf("error optimizing lander model: %s", err.Error())
			}
			return
		})
		return
	})
	return ctx.Status(200).SendString(`{"ok":true}`)
}
