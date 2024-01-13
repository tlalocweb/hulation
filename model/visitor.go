package model

import (
	"time"

	"gorm.io/gorm"
)

type IP struct {
	gorm.Model
	IP         string
	LastSeenAt time.Time
}

func (ip *IP) Scan(value interface{}) error {
	ip.IP = string(value.([]byte))
	return nil
}

func (ip IP) Value() (interface{}, error) {
	return ip.IP, nil
}

type AcceptedOnWeb struct {
	gorm.Model
	ServiceName string // "google analytics" or "google tag manager" or "yandex metrika" or "facebook pixel" etc.
	Answer      bool   // true or false // true - accepted, false - not accepted
}

// keeps track of the last broswer side cookies
// this visitor used. Useful if Safari or other browser (or the user) decide to wipe out
// cookies on the browser side.
// See: https://stape.io/blog/safari-itp-update-limits-cookies-to-7-days-for-responses-from-3rd-party-ips
// See: https://webkit.org/blog/10218/full-third-party-cookie-blocking-and-more/
// See: https://www.ablecdp.com/blog/how-conversions-can-be-tracked-without-third-party-cookies
type LastCookies struct {
	UserCookie string `json:"user_cookie"`
}

type Visitor struct {
	gorm.Model
	Email string `gorm:"" json:"email"`
	// server-side cookie
	SSCookie    string
	IPs         []IP            `gorm:"many2many:ip_list;" json:"ips"`                   // many:many
	Event       []Event         `gorm:"foreignKey:EventID"`                              // 1:many
	Accepted    []AcceptedOnWeb `gorm:"many2many:accepted_on_web_list;" json:"accepted"` // many:many
	LastCookies LastCookies     `json:"last_cookies" gorm:"embedded"`
}

type Event struct {
	gorm.Model
	EventID   uint64    `json:"event_id"`
	EventData string    `json:"event_data"`
	When      time.Time `json:"when"`
	VisitIP   string    `json:"visit_ip"`
	IP        *IP       `json:"ip" gorm:"foreignKey:VisitIP"`
	// "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/..." etc.
	BrowserUA string `json:"browser_ua"`
}

func AutoMigrateVisitorModels(db *gorm.DB) error {
	err := db.AutoMigrate(&Visitor{}, &IP{}, &Event{}, &AcceptedOnWeb{})
	if err != nil {
		return err
	}
	return nil
}
