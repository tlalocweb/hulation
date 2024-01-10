package model

type Form struct {
	ID            uint   `json:"id" gorm:"primary_key"`
	URL           string `json:"url"`
	Description   string `json:"description"`
	TemplateEvent Event  `json:"template_event"`
}
