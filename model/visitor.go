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
	IPs         []IP `gorm:"many2many:ip_list;" json:"ips"`
	Event       []Event
	Accepted    []AcceptedOnWeb
	LastCookies LastCookies `json:"last_cookies" gorm:"embedded"`
}

type Event struct {
	gorm.Model
	EventID   uint64    `json:"event_id"`
	EventData string    `json:"event_data"`
	When      time.Time `json:"when"`
	IP        IP        `json:"ip"`
	// "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/..." etc.
	BrowserUA string `json:"browser_ua"`
}
