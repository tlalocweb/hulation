package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/filesystem"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
	"github.com/tlalocweb/hulation/utils"
	"gorm.io/gorm"
)

type LanderReq struct {
	// ID can never be set by the client
	ID                string `json:"id"`
	Server            string `json:"server"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	RequestUrlPostfix string `json:"request_url_postfix"` // the caller can request a specific postfix - but it is not guaranteed to be used
	Redirect          string `json:"redirect"`
	IgnorePort        *bool  `json:"ignore_port,omitempty"`
	// By default if we are serving the target of the redirect,
	// then we will attempt to just serve the static page
	// directly instead of redirecting to it.
	// if NoServe is true then we will _not_ do this.
	NoServe *bool `json:"no_serve,omitempty"`
}

type LanderPostResp struct {
	FinalUrl string `json:"final_url"`
	ID       string `json:"id"`
}

var landerOptimizeRunner *utils.RunOnceMaxInterval
var landerRunnerCommit *utils.DeferredRunner

func LanderCreate(c *fiber.Ctx) (err error) {

	// get the request body
	var landerreq LanderReq

	body := c.Body()
	err = json.Unmarshal(body, &landerreq)
	if err != nil {
		return c.Status(400).SendString("bad parse: " + err.Error())
	}

	// create a new lander
	lander := model.NewLander()
	lander.Name = landerreq.Name
	lander.Description = landerreq.Description
	lander.Server = landerreq.Server
	//	lander.UrlPostfix = landerreq.UrlPostfix
	lander.Redirect = landerreq.Redirect
	if landerreq.IgnorePort != nil {
		lander.IgnorePort = *landerreq.IgnorePort
	}
	if landerreq.NoServe != nil {
		lander.NoServe = *landerreq.NoServe
	}

	// commit the lander
	inst, err := lander.Commit(landerreq.RequestUrlPostfix, model.GetDB())
	if err != nil {
		log.Errorf("error committing lander form model: %s", err.Error())
		resp := &ResponseError{StatusCode: 500, RootCause: fmt.Errorf("error committing lander: %v", err)}
		// if validation error
		if _, ok := err.(*model.ValidationError); ok {
			resp.StatusCode = 400
			resp.RootCause = fmt.Errorf("error comitting lander (validation): %v", err)
			return c.Status(resp.StatusCode).SendString(resp.JsonBody())
		}
		return c.Status(resp.StatusCode).SendString(resp.JsonBody())
	}

	// return the final url
	return c.Status(201).JSON(&LanderPostResp{FinalUrl: inst.GetFinalUrl(), ID: lander.ID})
}

func init() {
	// don't optimize the form models table too often (no more than every 5 seconds)
	landerOptimizeRunner = utils.NewRunOnceMaxInterval(5 * time.Second)
	landerRunnerCommit = utils.NewDeferredRunner("landerRunnerCommit")
	landerRunnerCommit.Start()
}

func LanderModify(c *fiber.Ctx) (err error) {

	landerid := c.Params("landerid")
	if len(landerid) == 0 {
		return c.Status(404).SendString("404 Not Found - No landerid")
	}

	// get the request body
	var landerreq LanderReq

	body := c.Body()

	err = json.Unmarshal(body, &landerreq)
	if err != nil {
		return c.Status(400).SendString("bad parse: " + err.Error())
	}

	lander, _, err := model.GetLanderById(model.GetDB(), landerid)
	if err != nil {
		log.Errorf("error getting form model: %s", err.Error())
		return c.Status(500).SendString("error getting lander model: " + err.Error())
	}
	if lander == nil {
		return c.Status(404).SendString("404 Not Found - No lander model by id " + c.Params("formid"))
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
	// if len(landerreq.UrlPostfix) > 0 {
	// 	lander.UrlPostfix = landerreq.UrlPostfix
	// }
	if len(landerreq.Redirect) > 0 {
		lander.Redirect = landerreq.Redirect
	}

	if landerreq.IgnorePort != nil {
		lander.IgnorePort = *landerreq.IgnorePort
	}
	if landerreq.NoServe != nil {
		lander.NoServe = *landerreq.NoServe
	}

	// commit the lander
	inst, err := lander.Commit(landerreq.RequestUrlPostfix, model.GetDB())
	if err != nil {
		log.Errorf("error committing lander form model: %s", err.Error())
		resp := &ResponseError{StatusCode: 500, RootCause: fmt.Errorf("error committing lander: %v", err)}
		// if validation error
		if _, ok := err.(*model.ValidationError); ok {
			resp.StatusCode = 400
			resp.RootCause = fmt.Errorf("error comitting lander (validation): %v", err)
			return c.Status(resp.StatusCode).SendString(resp.JsonBody())
		}
		return c.Status(resp.StatusCode).SendString(resp.JsonBody())
	}

	// we will have multiple entries in the table (which uses ReplacingMergeTree)
	// for a single form. so we need to OPTIMIZE the table to remove the old entries
	// at some point soon.
	landerOptimizeRunner.Run(func() (err error) {
		err = model.OptimizeLanderModels(model.GetDB())
		if err != nil {
			log.Errorf("error optimizing lander model: %s", err.Error())
		}
		return
	})

	return c.Status(201).JSON(&LanderPostResp{FinalUrl: inst.GetFinalUrl(), ID: lander.ID})
}

func LanderDelete(c *fiber.Ctx) (err error) {
	landerid := c.Params("landerid")
	if len(landerid) == 0 {
		return c.Status(404).SendString("404 Not Found - No landerid")
	}
	err = model.DeleteLander(model.GetDB(), landerid)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return c.Status(404).SendString("404 Not Found - No lander by id " + landerid)
		} else {
			return c.Status(500).SendString("error deleting lander: " + err.Error())
		}
	}

	return c.Status(200).SendString("lander deleted")
}

func DoLanding(c *fiber.Ctx) (err error) {
	log.Debugf("DoLanding: %s", c.Params("landerid"))
	landerid := c.Params("landerid")
	if len(landerid) == 0 {
		return c.Status(404).SendString("404 Not Found - No landerid")
	}
	lander, inst, err := model.GetLanderByUrlPostfix(model.GetDB(), landerid)
	if err != nil {
		log.Errorf("error getting lander: %s", err.Error())
		return c.Status(500).SendString("error getting lander: " + err.Error())
	}
	if lander == nil {
		return c.Status(404).SendString("404 Not Found - No lander by id " + c.Params("landerid"))
	}
	// push to the commit thread
	landerRunnerCommit.Run(func() (err error) {
		err = lander.AddHit(model.GetDB())
		if err != nil {
			err = fmt.Errorf("error committing lander: %s", err.Error())
		}
		// optimize the lander table at some point soon
		landerOptimizeRunner.Run(func() (err error) {
			err = model.OptimizeLanderModels(model.GetDB())
			if err != nil {
				log.Errorf("error optimizing lander model: %s", err.Error())
			}
			return
		})
		return
	})
	if inst == nil {
		return c.Status(500).SendString("500 - error getting lander instance " + c.Params("landerid"))
	}
	ok, redirect := inst.DoRedirect()
	if ok {
		return c.Redirect(redirect)
	}
	ok, _ = inst.DoStatic()
	if ok {
		err := filesystem.SendFile(c, http.Dir(inst.GetFsRoot()), inst.GetStaticPath())
		if err != nil {
			// Handle the error, e.g., return a 404 Not Found response
			return c.Status(fiber.StatusNotFound).SendString("File not found")
		}
		c.Context().SetStatusCode(200)
		return nil
		//		return c.SendFile(static)
	}
	return c.Status(500).SendString("500 - error handling lander " + c.Params("landerid"))
}

// DoLandingHit increments the hit count for a lander
// but does not do a redirect
func DoLandingHit(c *fiber.Ctx) (err error) {
	landerid := c.Params("landerid")
	if len(landerid) == 0 {
		return c.Status(404).SendString("404 Not Found - No landerid")
	}
	lander, _, err := model.GetLanderById(model.GetDB(), landerid)
	if err != nil {
		log.Errorf("error getting lander: %s", err.Error())
		return c.Status(500).SendString("error getting lander: " + err.Error())
	}
	if lander == nil {
		return c.Status(404).SendString("404 Not Found - No lander by id " + c.Params("landerid"))
	}
	// push to the commit thread
	landerRunnerCommit.Run(func() (err error) {
		err = lander.AddHit(model.GetDB())
		if err != nil {
			err = fmt.Errorf("error committing lander: %s", err.Error())
		}
		// optimize the lander table at some point soon
		landerOptimizeRunner.Run(func() (err error) {
			err = model.OptimizeLanderModels(model.GetDB())
			if err != nil {
				log.Errorf("error optimizing lander model: %s", err.Error())
			}
			return
		})
		return
	})
	return c.Status(200).SendString(`{"ok":true}`)
}
