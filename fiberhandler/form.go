package fiberhandler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cbroglie/mustache"
	"github.com/gofiber/fiber/v2"
	"github.com/santhosh-tekuri/jsonschema"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
	"github.com/tlalocweb/hulation/utils"
)

type FormPostReq struct {
	URL      string            `json:"url"`
	Fields   map[string]string `json:"fields"`
	SSCookie string            `json:"vc"`
	// Google Captcha or Cloudflare turnsile field - some unique string used by their API
	Captcha string `json:"captcha"`
}

type FormPostResponse struct {
	Ok       string `json:"ok"`
	Feedback string `json:"feedback"`
	TicketID string `json:"ticketid"`
}

//var formHookRunner *utils.DeferredRunner

var precompileNewSubmitHooks = &utils.RunOnceSingleton{Run: func(p interface{}) (err error) {
	// formHookRunner = utils.NewDeferredRunner("formHookRunner")
	// formHookRunner.Start()
	// add in any global things which should be available during any time we call this hook
	// If we don't change the names of the globals later, the script should always
	// be precompiled and ready to run.
	hostconf := p.(*config.Server)
	if hostconf.Hooks != nil {
		hostconf.Hooks.PrecompileHooksOnNewFormSubmission(map[string]any{"visitorid": "", "url": "", "formname": "", "fields": "", "newvisitor": false})
	}
	return
}}

func FormSubmit(c *fiber.Ctx) (err error) {
	hostconf, host, httperr, err := GetHostConfig(c)
	if err != nil {
		return c.Status(httperr).SendString(err.Error())
	}
	id := c.Query("h")
	if id != hostconf.ID {
		return c.Status(400).SendString("host id mismmatch")
	}
	// run the precompile hooks once (only once) for each host
	precompileNewSubmitHooks.Verify(host, hostconf, "Failed to precompile new form submission hooks")
	onetimeid := c.Query("r")
	formid := c.Params("formid")
	if len(formid) == 0 {
		return c.Status(404).SendString("404 Not Found - No formid")
	}

	formdata := new(FormPostReq)

	err = c.BodyParser(formdata)
	if err != nil {
		c.Status(400).SendString("bad parse: " + err.Error())
	}

	// let's get the visitor:
	// if it's there (it should be) we will just use the cookie value passed by in the API call the /hello endpoint
	// processed when the script loaded. Why? b/c if the user's browser is blocking all cookies,
	// we will at least get the cookie we just created in the previous request (since we are passing it ourselves)
	// otherwise we will get the cookie in the headers as per normal.
	var visitor *model.Visitor
	var newvisitor bool
	// figure out visitor
	if formdata.SSCookie != "" {
		// we have a visitor sscookie via rhe request
		visitor, err = model.GetVisitorBySSCookie(model.GetDB(), formdata.SSCookie)
		if err != nil {
			log.Errorf("Error getting visitor by sscookie via /form request: %s", err.Error())
		} else {
			if visitor != nil {
				log.Debugf("visitor seen by sscookie (via /form): %s", visitor.ID)
			} else {
				log.Debugf("no known visitor by sscookie (via /form)")
			}
		}
	}
	if visitor == nil {
		newvisitor = true
		// attempt to get visitor by cookie in headers
		// check for httponly cookie
		sscookie := c.Cookies(hostconf.CookieOpts.CookiePrefix + "_helloss")
		if len(sscookie) > 0 {
			log.Debugf("saw sscookie (helloiframe): %s", sscookie)
			// cookie exists - find visitor
			visitor, err = model.GetVisitorBySSCookie(model.GetDB(), sscookie)
			if err != nil {
				// ignore not found error
				return c.Status(500).SendString("error getting visitor by sscookie: " + err.Error())
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
				return c.Status(500).SendString("error getting visitor by cookie: " + err.Error())
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
		}

	}
	// check the captcha
	// ok - get the form model
	formmodel := model.GetCachedFormModelByIdOrName(formid)
	if formmodel == nil {
		formmodel, err = model.GetFormModelById(model.GetDB(), formid)
		if err != nil {
			return c.Status(500).SendString("error getting form model: " + err.Error())
		}
		if formmodel == nil {
			// try by name then
			formmodel, err = model.GetFormModelByName(model.GetDB(), formid)
			if err != nil {
				return c.Status(500).SendString("error getting form model (by name): " + err.Error())
			}
			if formmodel == nil {
				return c.Status(404).SendString("404 Not Found - No form model by name or id " + formid)
			}
		}
	}
	// See if the form requires captcha - if it does, determine the type and
	// then validate the captcha
	err = formmodel.ValidateCaptcha(hostconf.CaptchaSecret, formdata.Captcha)
	if err != nil {
		return c.Status(http.StatusForbidden).SendString("error validating captcha: " + err.Error())
	}

	fieldsstr, err := json.Marshal(formdata.Fields)
	if err != nil {
		return c.Status(400).SendString("error marshalling fields: " + err.Error())
	}
	submission, err := formmodel.NewFormSubmissionWithEvent(visitor, string(fieldsstr))
	validationerr, ok := err.(*jsonschema.ValidationError)
	if ok {
		return c.Status(400).SendString("error validating form submission: " + validationerr.Error())
	} else if err != nil {
		return c.Status(528).SendString("error creating form submission: " + err.Error())
	}

	err = submission.Commit(model.GetDB())
	if err != nil {
		return c.Status(HTTPErrorDBFailure).SendString("error committing form submission: " + err.Error())
	}

	postresp := FormPostResponse{
		Ok:       onetimeid,
		TicketID: submission.Form.TicketId,
	}

	if formmodel.Feedback != "" {
		feedbacktmp, err := mustache.ParseString(formmodel.Feedback)
		if err == nil {
			var feedbackbuf bytes.Buffer
			vars := make(map[string]string)

			for k, v := range formdata.Fields {
				vars["field:"+k] = v
			}
			vars["ticketid"] = submission.Form.TicketId

			err = feedbacktmp.FRender(&feedbackbuf, vars)
			if err == nil {
				postresp.Feedback = feedbackbuf.String()
			} else {
				log.Errorf("error rendering feedback template: %s", err.Error())
			}
			// errors ignored - we did record the submission anyway
		}
	}
	// run the form submission hooks
	hostconf.Hooks.SubmitToHooksOnNewFormSubmission(map[string]any{"visitorid": visitor.ID, "url": formdata.URL, "fields": formdata.Fields, "formname": formmodel.Name, "newvisitor": newvisitor}, nil, nil)

	postrespout, err := json.Marshal(postresp)
	if err != nil {
		// just print an error. we don't want to fail the request - we did record it.
		log.Errorf("error marshalling post response: %s", err.Error())
		return c.Status(200).SendString(`{"ok": "` + onetimeid + `"}`)
	} else {
		return c.Status(200).Send(postrespout)
	}
	//	formmodel

}

type FormModelReq struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Schema      string `json:"schema"`
	Captcha     string `json:"captcha"`
	Feedback    string `json:"feedback"`
}

// type FormModelReqAlt struct {
// 	Name        string                      `json:"name"`
// 	Description string                      `json:"description"`
// 	Schema       `json:"schema"`
// }

type FormModelResponse struct {
	Id string `json:"id"`
}

func FormRawJSONMessageToFormModelReq(raw map[string]json.RawMessage, formmodelreq *FormModelReq) (err error) {
	err = json.Unmarshal(raw["name"], &formmodelreq.Name)
	if err != nil {
		err = fmt.Errorf("error unmarshalling name: %w", err)
		return
	}
	if len(raw["description"]) > 0 {
		err = json.Unmarshal(raw["description"], &formmodelreq.Description)
		if err != nil {
			err = fmt.Errorf("error unmarshalling description: %w", err)
			return
		}
	}
	if len(raw["feedback"]) > 0 {
		err = json.Unmarshal(raw["feedback"], &formmodelreq.Feedback)
		if err != nil {
			err = fmt.Errorf("error unmarshalling feedback: %w", err)
			return
		}
	}
	if len(raw["captcha"]) > 0 {
		err = json.Unmarshal(raw["captcha"], &formmodelreq.Captcha)
		if err != nil {
			err = fmt.Errorf("error unmarshalling captcha: %w", err)
			return
		}
	}
	var schema interface{}

	err = json.Unmarshal(raw["schema"], &schema)
	if err != nil {
		err = fmt.Errorf("error unmarshalling schema: %w", err)
		return
	}

	schemas, err := json.Marshal(schema)
	if err != nil {
		err = fmt.Errorf("error marshalling schema: %w", err)
		return
	}

	// schema, err := raw["schema"].MarshalJSON()
	// if err != nil {
	// 	return
	// }
	// var m json.RawMessage
	// err = json.Unmarshal(raw["schema"], &m)
	// if err != nil {
	// 	return
	// }
	// schema, err := json.Marshal(m)
	// if err != nil {
	// 	return
	// }
	// schema, err := raw["schema"].MarshalJSON()
	// if err != nil {
	// 	return
	// }
	fmt.Printf("schema: %s", string(schemas))
	formmodelreq.Schema = string(schemas)
	// err = json.Unmarshal(, &formmodelreq.Schema)
	// if err != nil {
	// 	return
	// }
	return
}

func FormCreate(c *fiber.Ctx) (err error) {
	// hostconf, _, httperr, err := GetHostConfig(c)
	// if err != nil {
	// 	return c.Status(httperr).SendString(err.Error())
	// }
	// id := c.Query("h")
	// if id != hostconf.ID {
	// 	return c.Status(400).SendString("host id mismmatch")
	// }

	body := c.Body()

	var formmodelreq FormModelReq
	var formmodelreqalt map[string]json.RawMessage

	err = json.Unmarshal(body, &formmodelreq)
	if err != nil {
		err = json.Unmarshal(body, &formmodelreqalt)
		if err != nil {
			return c.Status(400).SendString("bad parse: " + err.Error())
		}
		err = FormRawJSONMessageToFormModelReq(formmodelreqalt, &formmodelreq)
		if err != nil {
			return c.Status(400).SendString("bad parse (2): " + err.Error())
		}
	}
	// err = c.BodyParser(formmodelreq)
	// if err != nil {
	// 	return c.Status(400).SendString("bad parse: " + err.Error())
	// }

	if formmodelreq.Name == "" {
		return c.Status(400).SendString("name is required")
	}
	if formmodelreq.Schema == "" {
		return c.Status(400).SendString("schema is required")
	}

	formmodel, err := model.CreateNewFormModel(formmodelreq.Name, formmodelreq.Name, formmodelreq.Description, formmodelreq.Schema, formmodelreq.Captcha, formmodelreq.Feedback)

	if err != nil {
		return c.Status(400).SendString("error creating form model: " + err.Error())
	}

	id, err := formmodel.ValidateNewModel(model.GetDB())

	if err != nil {
		return c.Status(400).SendString("error validating form model: " + err.Error())
	}

	err = formmodel.Commit(model.GetDB())

	if err != nil {
		return c.Status(500).SendString("error committing form model: " + err.Error())
	}

	resp, err := json.Marshal(FormModelResponse{Id: id})
	if err != nil {
		return c.Status(500).SendString("error marshalling response: " + err.Error())
	}

	return c.Status(200).Send(resp)
}

var formOptimizeRunner *utils.RunOnceMaxInterval

func init() {
	// don't optimize the form models table too often (no more than every 5 seconds)
	formOptimizeRunner = utils.NewRunOnceMaxInterval(5 * time.Second)
}

func FormModify(c *fiber.Ctx) (err error) {

	formid := c.Params("formid")
	if len(formid) == 0 {
		return c.Status(404).SendString("404 Not Found - No formid")
	}

	formmodelreq := new(FormModelReq)

	err = json.Unmarshal(c.Body(), &formmodelreq)
	if err != nil {
		log.Errorf("error unmarshalling form model request: %s", err.Error())
		return c.Status(400).SendString("bad parse: " + err.Error())
	}

	formmodel, err := model.GetFormModelById(model.GetDB(), c.Params("formid"))
	if err != nil {
		log.Errorf("error getting form model: %s", err.Error())
		return c.Status(500).SendString("error getting form model: " + err.Error())
	}
	if formmodel == nil {
		return c.Status(404).SendString("404 Not Found - No form model by id " + c.Params("formid"))
	}

	if formmodelreq.Name != "" {
		formmodel.Name = formmodelreq.Name
	}
	if formmodelreq.Schema != "" {
		formmodel.Schema = formmodelreq.Schema
	}
	if formmodelreq.Description != "" {
		formmodel.Description = formmodelreq.Description
	}
	if formmodelreq.Captcha != "" {
		formmodel.Captcha = formmodelreq.Captcha
	}
	if formmodelreq.Feedback != "" {
		formmodel.Feedback = formmodelreq.Feedback
	}

	err = formmodel.ValidateExistingModel(model.GetDB())
	if err != nil {
		log.Errorf("error validating form model: %s", err.Error())
		return c.Status(400).SendString("error validating form model: " + err.Error())
	}
	err = formmodel.Commit(model.GetDB())
	if err != nil {
		return c.Status(500).SendString("error committing form model: " + err.Error())
	}

	// we will have multiple entries in the table (which uses ReplacingMergeTree)
	// for a single form. so we need to OPTIMIZE the table to remove the old entries
	// at some point soon.
	formOptimizeRunner.Run(func() (err error) {
		err = model.OptimizeFormModels(model.GetDB())
		if err != nil {
			log.Errorf("error optimizing form model: %s", err.Error())
		}
		return
	})

	return c.Status(200).SendString("Form model modified")
}

func FormDelete(c *fiber.Ctx) (err error) {
	formid := c.Params("formid")
	if len(formid) == 0 {
		return c.Status(404).SendString("404 Not Found - No formid")
	}

	// get form by id
	formmodel, err := model.GetFormModelById(model.GetDB(), c.Params("formid"))
	if err != nil {
		log.Errorf("error getting form model: %s", err.Error())
		return c.Status(500).SendString("error getting form model: " + err.Error())
	}
	if formmodel == nil {
		return c.Status(404).SendString("404 Not Found - No form model by id " + c.Params("formid"))
	}
	err = formmodel.Delete(model.GetDB())
	if err != nil {
		return c.Status(500).SendString("error deleting form: " + err.Error())
	}
	return c.Status(200).SendString("form model deleted")
}
