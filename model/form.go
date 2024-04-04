package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v5"
	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/utils"
	"gorm.io/gorm"
)

const (
	sqlCreateFormModel = `
	CREATE TABLE IF NOT EXISTS form_models
	(
		^id^ String,
		^created_at^ DateTime64(3),
		^updated_at^ DateTime64(3),
		^name^ String,
		^captcha^ String,
		^schema^ String,
		^description^ String,
		^feedback^ String
	)
	ENGINE = ReplacingMergeTree(updated_at)
	ORDER BY id;`
)

const (
	// Supported captcha types
	// Cloudflare captcha ()
	TURNSTILE_CAPTCHA = "turnstile"
	GOOGLE_RECAPTCHA  = "recaptcha2"
	GOOGLE_RECAPTCHA3 = "recaptcha3"
)

type FormModel struct {
	HModel
	// the name of the form - used to help identify it but not required
	Name string `json:"name" yaml:"name"`
	// jsonschema of the fields required in this FormModel
	// see: https://github.com/santhosh-tekuri/jsonschema
	Schema string `json:"schema" yaml:"schema"`
	// an optional description of the form
	Description string `json:"description" yaml:"description"`
	// A feedback template message. This will be sent in the reply
	// to the caller when the form is successfully submitted
	Feedback string `json:"feedback" yaml:"feedback"`
	// uses one of the supported captcha types
	// See: TURNSTILE_CAPTCHA, GOOGLE_RECAPTCHA, GOOGLE_RECAPTCHA3
	Captcha    string `json:"captcha" yaml:"captcha"`
	needCommit bool
	isDeleted  bool
}

func (f *FormModel) BeforeCreate(tx *gorm.DB) (err error) {
	// UUID version 7
	return
}

type FormSubmission struct {
	HModel
	VisitorID      string    `json:"visitorid"` // the ID of the visitor that submitted this form
	SubmissionDate time.Time `json:"submission_date"`
	ModelID        string    `json:"formid"` // one:many relationship with FormModel - which FormModel is this submission for?
	URL            string    `json:"url"`    // url that script pulled before furm submitted
	// Will be a randomly generated ID - which is safe to provide to the caller
	// which identifies the form submission. This is useful for tracking
	TicketId string `json:"ticket_id"`
	// The Event associated with this form submission
	SubmitEvent string `json:"template_event"` // ID of event that triggered this form submission
	FieldsJSON  string `json:"fields"`
	needCommit  bool
}

func (f *FormSubmission) BeforeCreate(tx *gorm.DB) (err error) {
	// UUID version 7
	if len(f.ID) < 1 {
		uuid7, err := uuid.NewV7()
		if err != nil {
			return err
		}
		f.ID = uuid7.String()
	}
	return
}
func AutoMigrateFormModels(db *gorm.DB) error {
	err := db.Exec(utils.SqlStr(sqlCreateFormModel)).Error
	if err != nil {
		return err
	}

	err = db.AutoMigrate(&FormSubmission{})
	if err != nil {
		return err
	}
	return nil
}

func FieldsToMap(jsonStr string) map[string]interface{} {
	result := make(map[string]interface{})
	json.Unmarshal([]byte(jsonStr), &result)
	return result
}

func (f *FormSubmission) Fields() map[string]interface{} {
	return FieldsToMap(f.FieldsJSON)
}

func (f *FormSubmission) SetFields(fields map[string]interface{}) {
	jsonStr, _ := json.Marshal(fields)
	f.FieldsJSON = string(jsonStr)
}

type DeferredSubmission struct {
	Event *Event
	Form  *FormSubmission
}

func (d *DeferredSubmission) Commit(db *gorm.DB) error {
	err := db.Create(d.Event).Error
	d.Form.SubmitEvent = d.Event.ID
	err2 := db.Create(d.Form).Error
	if err2 != nil {
		return err2
	}
	if err != nil {
		return err
	}
	return nil
}

// compiled schemas
var formSchemaCache *utils.InMemCache

// models by ID
var formModelIDCache *utils.InMemCache

// models by Name
var formModelNameCache *utils.InMemCache

func init() {
	// stores precompiled jsonschema for forms
	formSchemaCache = utils.NewInMemCache().WithExpiration(72 * time.Hour).Start()
	formModelIDCache = utils.NewInMemCache().WithExpiration(72 * time.Hour).Start()
	formModelNameCache = utils.NewInMemCache().WithExpiration(72 * time.Hour).Start()
}

func CheckIsNameUsedFormModel(db *gorm.DB, name string) (ret bool, err error) {
	// _, ok := formModelIDCache.Get(name)
	// if ok {
	// 	ret = true
	// 	return
	// }
	var count int64
	err = db.Model(&FormModel{}).Where("name = ?", name).Count(&count).Error
	ret = count > 0
	// formModelIDCache.SetAlways(name, 1)
	return
}

func CheckIsIdUsedFormModel(db *gorm.DB, id string) (ret bool, err error) {
	// _, ok := formModelIDCache.Get(name)
	// if ok {
	// 	ret = true
	// 	return
	// }
	var count int64
	err = db.Model(&FormModel{}).Where("id = ?", id).Count(&count).Error
	ret = count > 0
	// formModelIDCache.SetAlways(name, 1)
	return
}

func (f *FormModel) initForm() (err error) {

	f.needCommit = true
	return
}

func CreateNewFormModel(id string, name string, description string, schema string, captcha string, feedback string) (ret *FormModel, err error) {
	ret = &FormModel{
		HModel:      HModel{ID: id},
		Name:        name,
		Schema:      schema,
		Description: description,
		Captcha:     captcha,
		Feedback:    feedback,
		needCommit:  true,
	}
	err = ret.initForm()
	//	err = db.Create(ret).Error
	return
}

func OptimizeFormModels(db *gorm.DB) (err error) {
	// optimize table
	err = db.Exec("OPTIMIZE TABLE form_models").Error
	if err == nil {
		log.Debugf("model: form model optimized")
	} else {
		log.Errorf("model: form model optimize error: %v", err)
	}
	return
}

// Validate checks if the FormModel is valid
// it also creates a new ID
// This should be done before committing the FormModel to the database
func (f *FormModel) ValidateNewModel(db *gorm.DB) (id string, err error) {
	var yes bool
	id = utils.CamelCase(f.Name)
	yes, err = CheckIsIdUsedFormModel(db, id)
	if yes {
		id = fmt.Sprintf("%s-%s", id, utils.FastRandString(5))
	}
	f.ID = id
	if err != nil {
		err = fmt.Errorf("error checking if name is used: %v", err)
		return
	}
	// check schema to make sure its valid
	_, err = jsonschema.CompileString(fmt.Sprintf("form_model_%s.json", f.Name), f.Schema)
	if err != nil {
		err = fmt.Errorf("error compiling schema: %v", err)
		return
	}
	// now marshal/unmarshal json to remove spaces etc.
	var fields interface{}
	if err = json.Unmarshal([]byte(f.Schema), &fields); err != nil {
		err = fmt.Errorf("error unmarshalling schema: %v", err)
		return
	}
	var newSchema []byte
	newSchema, err = json.Marshal(fields)
	if err != nil {
		err = fmt.Errorf("error marshalling schema: %v", err)
		return
	}
	f.Schema = string(newSchema)
	return
}

func (f *FormModel) ValidateExistingModel(db *gorm.DB) (err error) {
	// check schema to make sure its valid
	_, err = jsonschema.CompileString(fmt.Sprintf("form_model_%s.json", f.Name), f.Schema)
	if err != nil {
		err = fmt.Errorf("error compiling schema: %v", err)
		return
	}
	// now marshal/unmarshal json to remove spaces etc.
	var fields interface{}
	if err = json.Unmarshal([]byte(f.Schema), &fields); err != nil {
		err = fmt.Errorf("error unmarshalling schema: %v", err)
		return
	}
	var newSchema []byte
	newSchema, err = json.Marshal(fields)
	if err != nil {
		err = fmt.Errorf("error marshalling schema: %v", err)
		return
	}
	f.Schema = string(newSchema)
	return
}

// func (f *FormModel) Update(db *gorm.DB) (err error) {
// 	err = db.Connection(func(tx *gorm.DB) (err error) {
// 		err = tx.Exec("ALTER TABLE form_models UPDATE name = ?, description = ?, server = ?, url_postfix = ?, redirect = ? WHERE id = ?", f.Name, l.Description, l.Server, l.UrlPostfix, l.Redirect, l.ID).Error
// 		return
// 	})
// 	if err != nil {
// 		return
// 	}
// 	return
// }

// PrelaodDefinedLanders will preload the landers defined in the hula config file
// into the database and the http handlers
// It only throws an error if it can't create a lander at all - not if it has
// an issue with the db
func PreloadDefinedForms(db *gorm.DB) (err error) {
	//lander *config.DefinedLander, server *config.Server
	for _, server := range app.GetConfig().Servers {
		for _, form := range server.Forms {
			var m *FormModel
			defname := getPredifinedNameFromName(form.Name, server.Host)
			m, err = GetFormModelByName(db, defname)
			if err != nil {
				log.Errorf("PreloadDefinedForms: error getting form by name: %s", err.Error())
			}
			if m != nil {
				// update (may or may not have changed)
				m.Name = form.Name
				m.Schema = form.Schema
				m.Description = form.Description
				m.Captcha = form.Captcha
				m.Feedback = form.Feedback
				err = m.Commit(db)
				if err != nil {
					log.Errorf("PreloadDefinedForms: error updating lander: %s", err.Error())
					err = nil
					return
				}
				log.Debugf("PreloadDefinedForms: updated form model: id: %s name: %s", defname, m.Name)
				return
			}

			m, err = CreateNewFormModel(defname, form.Name, form.Description, form.Schema, form.Captcha, form.Feedback)
			if err != nil {
				log.Errorf("PreloadDefinedForms: error creating form model: %s", err.Error())
				return
			}
			err = m.Commit(db)
			if err != nil {
				log.Errorf("PreloadDefinedForms: error committing form model: %s", err.Error())
				return
			}
			log.Debugf("PreloadDefinedForms: created form model: id: %s name: %s", defname, m.Name)
		}
	}
	return
}

type TurnstileResponse struct {
	Success bool     `json:"success"`
	Errors  []string `json:"error-codes"`
}

func (f *FormModel) ValidateCaptcha(captchasecret string, captchadat string) (err error) {

	switch f.Captcha {
	case TURNSTILE_CAPTCHA:
		// check if the visitor has a valid token
		url := "https://challenges.cloudflare.com/turnstile/v0/siteverify"
		body := fmt.Sprintf("secret=%s&response=%s", captchasecret, captchadat)
		var req *http.Request
		var resp *http.Response
		req, err = http.NewRequest("POST", url, bytes.NewBuffer([]byte(body)))
		if err != nil {
			return fmt.Errorf("error validating captcha response (req): %v", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		client := &http.Client{Timeout: time.Second * 10}
		log.Debugf("Validating captcha response: POST to %s body %s", url, body)
		resp, err = client.Do(req)
		if err != nil {
			return fmt.Errorf("error validating captcha response (resp): %v", err)
		}
		defer resp.Body.Close()
		respbody, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("error validating captcha response (body): %v", err)
		}
		if resp.StatusCode != 200 {
			return fmt.Errorf("error validating captcha response: %d  Response body: %s", resp.StatusCode, string(respbody))
		}
		var authresp TurnstileResponse
		err = json.Unmarshal(respbody, &authresp)
		if err != nil {
			return fmt.Errorf("error unmarshalling captcha response: %v", err)
		}
		if !authresp.Success {
			return fmt.Errorf("captcha failed: %v", authresp.Errors)
		} else {
			log.Debugf("Captcha success.")
		}
		return nil
	case GOOGLE_RECAPTCHA:
		return fmt.Errorf("not implemented")
	case GOOGLE_RECAPTCHA3:
		return fmt.Errorf("not implemented")
	}
	return
}

// returns the ID of the form model
func (f *FormModel) Commit(db *gorm.DB) (err error) {
	err = db.Create(f).Error
	if err != nil {
		return
	}
	return
}

func GetFormModelByName(db *gorm.DB, name string) (m *FormModel, err error) {
	retraw, ok := formModelNameCache.Get(name)
	if ok {
		m = retraw.(*FormModel)
		return
	}
	m = &FormModel{}
	err = db.Model(m).Where("name = ?", name).First(m).Error
	if err != nil {
		// landerInstances.Del(id)
		if err == gorm.ErrRecordNotFound {
			log.Debugf("no form by name %s", name)
			err = nil
			m = nil
			return
		}
	} else {
		formModelNameCache.SetAlways(m.Name, m)
		formModelIDCache.SetAlways(m.ID, m)
	}
	return
}

func GetCachedFormModelByIdOrName(id string) (ret *FormModel) {
	retraw, ok := formModelIDCache.Get(id)
	if ok {
		ret = retraw.(*FormModel)
		return
	}
	retraw, ok = formModelNameCache.Get(id)
	if ok {
		ret = retraw.(*FormModel)
		return
	}
	return
}
func GetFormModelById(db *gorm.DB, id string) (ret *FormModel, err error) {
	retraw, ok := formModelIDCache.Get(id)
	if ok {
		ret = retraw.(*FormModel)
		return
	}
	ret = &FormModel{}
	err = db.Model(ret).Where("id = ?", id).First(ret).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			err = nil
			ret = nil
			return
		}
	} else {
		formModelIDCache.SetAlways(ret.ID, ret)
		formModelNameCache.SetAlways(ret.Name, ret)
	}

	return
}

// Takes in a string with JSON directly from a request to hulation
// It immediately validates if the JSON is valid and meets the FromModel requirements
// If ok, it then creates an Event and a FormSubmission - but does not comit them
// It returns a DeferredSubmission struct which can be used to write the Event + FormSubmission to the DB
// at a later time (possibly after the request is responded and closed)
func (m *FormModel) NewFormSubmissionWithEvent(visitor *Visitor, formdata string) (ret *DeferredSubmission, err error) {
	var sch *jsonschema.Schema
	if m.isDeleted {
		err = fmt.Errorf("form model is deleted")
		return
	}
	// check cache
	result, ok := formSchemaCache.Get(m.ID)
	if !ok {
		// compile jsonschema
		id := m.Name
		if len(id) < 1 {
			id = m.ID
		}
		model_debugf("Compiling schema for form model: %s", id)
		sch, err = jsonschema.CompileString(fmt.Sprintf("form_model_%s.json", id), m.Schema)
		if err != nil {
			log.Errorf("Error compiling schema for form %s: %v", m.Name, err)
			return
		}
		// store in cache
		formSchemaCache.Set(m.ID, sch)
	} else {
		sch = result.(*jsonschema.Schema)
	}
	var fields interface{}
	if err = json.Unmarshal([]byte(formdata), &fields); err != nil {
		return
	}
	if err = sch.Validate(fields); err != nil {
		return
	}
	ret = &DeferredSubmission{
		Form: &FormSubmission{
			VisitorID:      visitor.ID,
			SubmissionDate: time.Now(),
			ModelID:        m.ID,
			FieldsJSON:     formdata,
			TicketId:       utils.FastRandString(10),
		},
	}
	ret.Event = NewEvent(EventCodeFormSubmission)
	ret.Form.SubmitEvent = ret.Event.ID
	ret.Event.BelongsTo = visitor.ID
	// NOTE: nothing is committed yet
	return
}

func (f *FormSubmission) Commit(db *gorm.DB) (err error) {
	err = db.Save(f).Error
	return
}

func (f *FormSubmission) Delete(db *gorm.DB) (err error) {
	err = db.Delete(f).Where("id = ?", f.ID).Error
	return
}

func (f *FormModel) Delete(db *gorm.DB) (err error) {
	// remove from cache
	formSchemaCache.Del(f.ID)
	err = db.Delete(f).Where("id = ?", f.ID).Error
	f.isDeleted = true
	formModelIDCache.Del(f.Name)
	return
}
