package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cbroglie/mustache"
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
	Captcha  string            `json:"captcha"`
}

type FormPostResponse struct {
	Ok       string `json:"ok"`
	Feedback string `json:"feedback"`
	TicketID string `json:"ticketid"`
}

var precompileNewSubmitHooks = &utils.RunOnceSingleton{Run: func(p interface{}) (err error) {
	hostconf := p.(*config.Server)
	if hostconf.Hooks != nil {
		hostconf.Hooks.PrecompileHooksOnNewFormSubmission(map[string]any{"visitorid": "", "url": "", "formname": "", "fields": "", "newvisitor": false})
	}
	return
}}

func FormSubmit(ctx RequestCtx) (err error) {
	hostconf, host, httperr, err := GetHostConfig(ctx)
	if err != nil {
		return ctx.Status(httperr).SendString(err.Error())
	}
	id := ctx.Query("h")
	if id != hostconf.ID {
		return ctx.Status(400).SendString("host id mismmatch")
	}
	precompileNewSubmitHooks.Verify(host, hostconf, "Failed to precompile new form submission hooks")
	onetimeid := ctx.Query("r")
	formid := ctx.Param("formid")
	if len(formid) == 0 {
		return ctx.Status(404).SendString("404 Not Found - No formid")
	}

	formdata := new(FormPostReq)
	err = ctx.BodyParser(formdata)
	if err != nil {
		return ctx.Status(400).SendString("bad parse: " + err.Error())
	}

	var visitor *model.Visitor
	var newvisitor bool
	if formdata.SSCookie != "" {
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
		sscookie := ctx.Cookie(hostconf.CookieOpts.CookiePrefix + "_helloss")
		if len(sscookie) > 0 {
			log.Debugf("saw sscookie (helloiframe): %s", sscookie)
			visitor, err = model.GetVisitorBySSCookie(model.GetDB(), sscookie)
			if err != nil {
				return ctx.Status(500).SendString("error getting visitor by sscookie: " + err.Error())
			}
			if visitor != nil {
				log.Debugf("visitor seen by sscookie: %s", visitor.ID)
			} else {
				log.Debugf("no known visitor by sscookie")
			}
		}
		cookie := ctx.Cookie(hostconf.CookieOpts.CookiePrefix + "_hello")
		if visitor == nil && len(cookie) > 0 {
			log.Debugf("saw cookie (helloiframe): %s", cookie)
			visitor, err = model.GetVisitorByCookie(model.GetDB(), cookie)
			if err != nil {
				return ctx.Status(500).SendString("error getting visitor by cookie: " + err.Error())
			}
			if visitor != nil {
				log.Debugf("visitor seen by cookie (helloiframe): %s", visitor.ID)
			} else {
				log.Debugf("no known visitor by cookie")
			}
		}

		if visitor == nil {
			visitor = model.NewVisitor()
		}
	}

	formmodel := model.GetCachedFormModelByIdOrName(formid)
	if formmodel == nil {
		formmodel, err = model.GetFormModelById(model.GetDB(), formid)
		if err != nil {
			return ctx.Status(500).SendString("error getting form model: " + err.Error())
		}
		if formmodel == nil {
			formmodel, err = model.GetFormModelByName(model.GetDB(), formid)
			if err != nil {
				return ctx.Status(500).SendString("error getting form model (by name): " + err.Error())
			}
			if formmodel == nil {
				return ctx.Status(404).SendString("404 Not Found - No form model by name or id " + formid)
			}
		}
	}

	err = formmodel.ValidateCaptcha(hostconf.CaptchaSecret, formdata.Captcha)
	if err != nil {
		return ctx.Status(http.StatusForbidden).SendString("error validating captcha: " + err.Error())
	}

	fieldsstr, err := json.Marshal(formdata.Fields)
	if err != nil {
		return ctx.Status(400).SendString("error marshalling fields: " + err.Error())
	}
	submission, err := formmodel.NewFormSubmissionWithEvent(visitor, string(fieldsstr))
	validationerr, ok := err.(*jsonschema.ValidationError)
	if ok {
		return ctx.Status(400).SendString("error validating form submission: " + validationerr.Error())
	} else if err != nil {
		return ctx.Status(528).SendString("error creating form submission: " + err.Error())
	}

	err = submission.Commit(model.GetDB())
	if err != nil {
		return ctx.Status(HTTPErrorDBFailure).SendString("error committing form submission: " + err.Error())
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
		}
	}
	hostconf.Hooks.SubmitToHooksOnNewFormSubmission(map[string]any{"visitorid": visitor.ID, "url": formdata.URL, "fields": formdata.Fields, "formname": formmodel.Name, "newvisitor": newvisitor}, nil, nil)

	postrespout, err := json.Marshal(postresp)
	if err != nil {
		log.Errorf("error marshalling post response: %s", err.Error())
		return ctx.Status(200).SendString(`{"ok": "` + onetimeid + `"}`)
	}
	return ctx.Status(200).SendBytes(postrespout)
}

type FormModelReq struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Schema      string `json:"schema"`
	Captcha     string `json:"captcha"`
	Feedback    string `json:"feedback"`
}

type FormModelResponse struct {
	Id string `json:"id"`
}

func FormRawJSONMessageToFormModelReq(raw map[string]json.RawMessage, formmodelreq *FormModelReq) (err error) {
	err = json.Unmarshal(raw["name"], &formmodelreq.Name)
	if err != nil {
		return fmt.Errorf("error unmarshalling name: %w", err)
	}
	if len(raw["description"]) > 0 {
		err = json.Unmarshal(raw["description"], &formmodelreq.Description)
		if err != nil {
			return fmt.Errorf("error unmarshalling description: %w", err)
		}
	}
	if len(raw["feedback"]) > 0 {
		err = json.Unmarshal(raw["feedback"], &formmodelreq.Feedback)
		if err != nil {
			return fmt.Errorf("error unmarshalling feedback: %w", err)
		}
	}
	if len(raw["captcha"]) > 0 {
		err = json.Unmarshal(raw["captcha"], &formmodelreq.Captcha)
		if err != nil {
			return fmt.Errorf("error unmarshalling captcha: %w", err)
		}
	}
	var schema interface{}
	err = json.Unmarshal(raw["schema"], &schema)
	if err != nil {
		return fmt.Errorf("error unmarshalling schema: %w", err)
	}
	schemas, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("error marshalling schema: %w", err)
	}
	fmt.Printf("schema: %s", string(schemas))
	formmodelreq.Schema = string(schemas)
	return
}

func FormCreate(ctx RequestCtx) (err error) {
	body := ctx.Body()

	var formmodelreq FormModelReq
	var formmodelreqalt map[string]json.RawMessage

	err = json.Unmarshal(body, &formmodelreq)
	if err != nil {
		err = json.Unmarshal(body, &formmodelreqalt)
		if err != nil {
			return ctx.Status(400).SendString("bad parse: " + err.Error())
		}
		err = FormRawJSONMessageToFormModelReq(formmodelreqalt, &formmodelreq)
		if err != nil {
			return ctx.Status(400).SendString("bad parse (2): " + err.Error())
		}
	}

	if formmodelreq.Name == "" {
		return ctx.Status(400).SendString("name is required")
	}
	if formmodelreq.Schema == "" {
		return ctx.Status(400).SendString("schema is required")
	}

	formmodel, err := model.CreateNewFormModel(formmodelreq.Name, formmodelreq.Name, formmodelreq.Description, formmodelreq.Schema, formmodelreq.Captcha, formmodelreq.Feedback)
	if err != nil {
		return ctx.Status(400).SendString("error creating form model: " + err.Error())
	}

	id, err := formmodel.ValidateNewModel(model.GetDB())
	if err != nil {
		return ctx.Status(400).SendString("error validating form model: " + err.Error())
	}

	err = formmodel.Commit(model.GetDB())
	if err != nil {
		return ctx.Status(500).SendString("error committing form model: " + err.Error())
	}

	return ctx.Status(200).SendJSON(FormModelResponse{Id: id})
}

var formOptimizeRunner *utils.RunOnceMaxInterval

func init() {
	formOptimizeRunner = utils.NewRunOnceMaxInterval(5 * time.Second)
}

func FormModify(ctx RequestCtx) (err error) {
	formid := ctx.Param("formid")
	if len(formid) == 0 {
		return ctx.Status(404).SendString("404 Not Found - No formid")
	}

	formmodelreq := new(FormModelReq)
	err = json.Unmarshal(ctx.Body(), &formmodelreq)
	if err != nil {
		log.Errorf("error unmarshalling form model request: %s", err.Error())
		return ctx.Status(400).SendString("bad parse: " + err.Error())
	}

	formmodel, err := model.GetFormModelById(model.GetDB(), formid)
	if err != nil {
		log.Errorf("error getting form model: %s", err.Error())
		return ctx.Status(500).SendString("error getting form model: " + err.Error())
	}
	if formmodel == nil {
		return ctx.Status(404).SendString("404 Not Found - No form model by id " + formid)
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
		return ctx.Status(400).SendString("error validating form model: " + err.Error())
	}
	err = formmodel.Commit(model.GetDB())
	if err != nil {
		return ctx.Status(500).SendString("error committing form model: " + err.Error())
	}

	formOptimizeRunner.Run(func() (err error) {
		err = model.OptimizeFormModels(model.GetDB())
		if err != nil {
			log.Errorf("error optimizing form model: %s", err.Error())
		}
		return
	})

	return ctx.Status(200).SendString("Form model modified")
}

func FormDelete(ctx RequestCtx) (err error) {
	formid := ctx.Param("formid")
	if len(formid) == 0 {
		return ctx.Status(404).SendString("404 Not Found - No formid")
	}

	formmodel, err := model.GetFormModelById(model.GetDB(), formid)
	if err != nil {
		log.Errorf("error getting form model: %s", err.Error())
		return ctx.Status(500).SendString("error getting form model: " + err.Error())
	}
	if formmodel == nil {
		return ctx.Status(404).SendString("404 Not Found - No form model by id " + formid)
	}
	err = formmodel.Delete(model.GetDB())
	if err != nil {
		return ctx.Status(500).SendString("error deleting form: " + err.Error())
	}
	return ctx.Status(200).SendString("form model deleted")
}
