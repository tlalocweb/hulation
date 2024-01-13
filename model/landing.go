package model

import "gorm.io/gorm"

// LandingLink is a model for the landing link feature.
// Landing links allow yuo to create a URL that things like a Google Ad or similar can go to
// where the user is then immediately redirected to the URL you specify, while a cookie is set
// on the user's browser to track the user - and the user is added to the database.
type LandingLink struct {
	ID          uint   `json:"id" gorm:"primary_key"`
	Link        string `json:"link"`
	Description string `json:"description"`
	RedirectURL string `json:"redirect_url"`
}

func AutoMigrateLandingModels(db *gorm.DB) error {
	err := db.AutoMigrate(&LandingLink{})
	if err != nil {
		return err
	}
	return nil
}
