package model

import (
	"time"

	"gorm.io/gorm"
)

const (
	RoleAdmin = "admin"
	// can only add events to the system
	RoleSubmitter = "submitter"

	FunctionSubmitVisitor = "submit.visitor"
)

type Role struct {
	HModel
	Type string `gorm:"primaryKey"`
}

type User struct {
	HModel
	Email     string  `gorm:"index"`
	Roles     []*Role `gorm:"many2many:user_roles;"`
	FirstName string
	LastName  string
	Role      string
}

type APIKey struct {
	Key  string `gorm:"primaryKey" json:"apikey"`
	User string
	// Allowed API functions
	APIFunctions string
	FirstSeen    time.Time
	LastSeenAt   time.Time
	Expiration   time.Time
	Seen         int          // how many times this IP has been seen
	Events       []*AuthEvent `gorm:"foreignKey:FromAPIKey;references:Key"`
}

type AuthEvent struct {
	HModel
	FromAPIKey string `gorm:"index"`
	Code       int
	FromIP     string `gorm:"index"`
	FromUser   string `gorm:"index"`
}

func CreateNewAPIKey(db *gorm.DB, key string, expiration time.Time) (*APIKey, error) {
	apikey := &APIKey{
		Key:        key,
		Expiration: expiration,
	}
	err := db.Create(apikey).Error
	if err != nil {
		return nil, err
	}
	return apikey, nil
}

func AutoMigrateAuthModels(db *gorm.DB) (err error) {
	// // must call the create table raw SQL before AutoMigrate or
	// // otherwise gorm will create them for you
	// err := db.Exec(utils.SqlStr(sqlCreateAlias)).Error
	// if err != nil {
	// 	return err
	// }
	// err = db.Exec(utils.SqlStr(sqlCreateIP)).Error
	// if err != nil {
	// 	return err
	// }

	err = db.AutoMigrate(&User{}, &Role{}, &APIKey{}, &AuthEvent{})
	if err != nil {
		return err
	}

	//	err := db.Set()"gorm:table_options", "ENGINE=Distributed(cluster, default, hits)").AutoMigrate(&User{})
	return nil
}
