package model

import (
	"encoding/json"

	"gorm.io/gorm"
)

type Form struct {
	HModel
	ID            uint   `json:"id" gorm:"primary_key"`
	URL           string `json:"url"`
	Description   string `json:"description"`
	TemplateEvent *Event `json:"template_event" gorm:"foreignKey:ID"`
	FieldsJSON    string `json:"fields"`
}

func AutoMigrateFormModels(db *gorm.DB) error {
	err := db.AutoMigrate(&Form{})
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

func (f *Form) Fields() map[string]interface{} {
	return FieldsToMap(f.FieldsJSON)
}

func (f *Form) SetFields(fields map[string]interface{}) {
	jsonStr, _ := json.Marshal(fields)
	f.FieldsJSON = string(jsonStr)
}
