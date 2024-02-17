package model

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v5"
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
		^schema^ String,
		^description^ String,
	)
	ENGINE = ReplacingMergeTree(updated_at)
	ORDER BY id;`
)

type FormModel struct {
	HModel
	// the name of the form - used to help identify it but not required
	Name string
	// jsonschema of the fields required in this FormModel
	// see: https://github.com/santhosh-tekuri/jsonschema
	Schema string `json:"schema"`
	// an optional description of the form
	Description string `json:"description"`
	needCommit  bool
	isDeleted   bool
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

var formSchemaCache *utils.InMemCache
var formModelIDCache *utils.InMemCache

func init() {
	// stores precompiled jsonschema for forms
	formSchemaCache = utils.NewInMemCache().WithExpiration(5 * time.Hour).Start()
	formModelIDCache = utils.NewInMemCache().WithExpiration(72 * time.Hour).Start()
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

func CreateNewFormModel(id string, name string, description string, schema string) (ret *FormModel, err error) {
	ret = &FormModel{
		HModel:      HModel{ID: id},
		Name:        name,
		Schema:      schema,
		Description: description,
		needCommit:  true,
	}
	err = ret.initForm()
	//	err = db.Create(ret).Error
	return
}

// Validate checks if the FormModel is valid
// it also creates a new ID
// This should be done before committing the FormModel to the database
func (f *FormModel) ValidateModel(db *gorm.DB) (id string, err error) {
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

// returns the ID of the form model
func (f *FormModel) Commit(db *gorm.DB) (err error) {
	err = db.Create(f).Error
	if err != nil {
		return
	}
	return
}

func GetFormModelById(db *gorm.DB, id string) (ret *FormModel, err error) {
	ret = &FormModel{}
	err = db.Model(ret).Where("id = ?", id).First(ret).Error
	if err != nil {
		formModelIDCache.Del(id)
		if err == gorm.ErrRecordNotFound {
			err = nil
			return
		}
	} else {
		formModelIDCache.SetAlways(ret.Name, ret.ID)
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
			model_debugf("Error compiling schema: %v", err)
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
