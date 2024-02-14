package handler

import (
	"github.com/gofiber/fiber/v2"
	"github.com/santhosh-tekuri/jsonschema"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
)

type FormPostReq struct {
	URL      string `json:"url"`
	Fields   string `json:"fields"`
	SSCookie string `json:"vc"`
}

func FormSubmit(c *fiber.Ctx) (err error) {
	hostconf, _, httperr, err := GetHostConfig(c)
	if err != nil {
		return c.Status(httperr).SendString(err.Error())
	}
	id := c.Query("h")
	if id != hostconf.ID {
		return c.Status(400).SendString("host id mismmatch")
	}

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

	// figure out visitor
	if formdata.SSCookie != "" {
		// we have a visitor sscookie via rhe request
		visitor, err = model.GetVisitorBySSCookie(model.GetDB(), formdata.SSCookie)
		if err != nil {
			log.Errorf("Error getting visitor by sscookie via /form request: %s", err.Error())
		} else {
			log.Debugf("visitor seen by sscookie (via /form): %s", visitor.ID)
		}
	}
	if visitor == nil {
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

	// ok - get the form model
	formmodel, err := model.GetFormModelById(model.GetDB(), formid)
	if err != nil {
		return c.Status(500).SendString("error getting form model: " + err.Error())
	}
	if formmodel == nil {
		return c.Status(404).SendString("404 Not Found - No form model by id " + formid)
	}
	submission, err := formmodel.NewFormSubmissionWithEvent(visitor, formdata.Fields)
	validationerr, ok := err.(*jsonschema.ValidationError)
	if ok {
		return c.Status(400).SendString("error validating form submission: " + validationerr.Error())
	} else if err != nil {
		return c.Status(500).SendString("error creating form submission: " + err.Error())
	}

	// TODO - now check turnstile validation if turnstile is included

	err = submission.Commit(model.GetDB())
	if err != nil {
		return c.Status(HTTPErrorDBFailure).SendString("error committing form submission: " + err.Error())
	}

	//	formmodel
	return c.Status(200).SendString("Form submitted")
}

type FormModelReq struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Schema string `json:"schema"`
}

func FormCreate(c *fiber.Ctx) (err error) {
	hostconf, _, httperr, err := GetHostConfig(c)
	if err != nil {
		return c.Status(httperr).SendString(err.Error())
	}
	id := c.Query("h")
	if id != hostconf.ID {
		return c.Status(400).SendString("host id mismmatch")
	}

	formmodelreq := new(FormModelReq)
	err = c.BodyParser(formmodelreq)
	if err != nil {
		return c.Status(400).SendString("bad parse: " + err.Error())
	}

	formmodel, err := model.CreateNewFormModel(formmodelreq.Name, formmodelreq.Name, formmodelreq.Name, formmodelreq.Schema)

	if err != nil {
		return c.Status(400).SendString("error creating form model: " + err.Error())
	}

	err = formmodel.Commit(model.GetDB())

	if err != nil {
		return c.Status(500).SendString("error committing form model: " + err.Error())
	}

	return c.Status(200).SendString("Form model created")
}

func FormModify(c *fiber.Ctx) (err error) {
	hostconf, _, httperr, err := GetHostConfig(c)
	if err != nil {
		return c.Status(httperr).SendString(err.Error())
	}
	id := c.Query("h")
	if id != hostconf.ID {
		return c.Status(400).SendString("host id mismmatch")
	}

	formmodelreq := new(FormModelReq)
	err = c.BodyParser(formmodelreq)
	if err != nil {
		return c.Status(400).SendString("bad parse: " + err.Error())
	}

	formmodel, err := model.GetFormModelById(model.GetDB(), formmodelreq.ID)
	if err != nil {
		return c.Status(500).SendString("error getting form model: " + err.Error())
	}
	if formmodel == nil {
		return c.Status(404).SendString("404 Not Found - No form model by id " + formmodelreq.ID)
	}

	if formmodelreq.Name != "" {
		formmodel.Name = formmodelreq.Name
	}
	if formmodelreq.Schema != "" {
		formmodel.Schema = formmodelreq.Schema
	}
	err = formmodel.Commit(model.GetDB())

	if err != nil {
		return c.Status(500).SendString("error committing form model: " + err.Error())
	}

	return c.Status(200).SendString("Form model modified")
}
