package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	// Event codes
	EventCodePageView = 0x00000001
	EventCodeClick    = 0x00000002
	// The user started filling out a form
	EventCodeStartForm         = 0x00000004
	EventCodeReferredFromKnown = 0x00000008
	EventCodeScrolledIntoView  = 0x00000010
	EventCodeFormSubmission    = 0x00000020
	EventCodeLanderHit         = 0x00000100
)

type Event struct {
	HModel
	BelongsTo string    `json:"belongs_to"` // foriegn key to Visitor.ID
	Code      uint64    `json:"code"`
	Data      string    `json:"data"`
	Method    string    `json:"method"`  // method of data collection
	URL       string    `json:"url"`     // raw URL
	UrlPath   string    `json:"urlpath"` // just the path part of the URL
	Host      string    `json:"host"`    // just the host part of the URL
	When      time.Time `json:"when"`    // might be different than CreatedAt if the reporting is delayed
	FromIP    string    `json:"from_ip"` // foreign key references IP.IP
	// "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/..." etc.
	BrowserUA string `json:"browser_ua"`
}

func NewEvent(code uint64) (ret *Event) {
	ret = &Event{
		Code: code,
		// EventData: eventData,
		// URL:       url,
		When: time.Now(),
		// FromIP:    fromIP,
		// BrowserUA: browserUA,
	}
	uuid7, err := uuid.NewV7()
	if err != nil {
		model_attn_debugf("Error creating new event uuid: %v", err)
	}
	ret.ID = uuid7.String()
	return
}

func AddEventForVisitor(db *gorm.DB, visitor *Visitor, event *Event) error {
	// With Clickhouse we can't enforce a foreign key constraint anyway
	// so we just have to trust that the visitor exists
	// this: return db.Model(visitor).Association("Event").Append(event)
	// currently fails with: hulation/model/visitor.go:381 code: 420, message: Cannot UPDATE key column `updated_at`
	// because the gorm / gorm-clickhouse driver is trying to udpate the updated_at column - this should not be done
	event.BelongsTo = visitor.ID
	return db.Model(event).Create(event).Error
	// return db.Model(visitor).Association("Event").Append(event)
}

func (e *Event) SetData(data string) {
	e.Data = data
}

func (e *Event) SetURL(url string) {
	e.URL = url
}

func (e *Event) SetUrlPath(urlpath string) {
	e.UrlPath = urlpath
}

func (e *Event) SetHost(host string) {
	e.Host = host
}

func (e *Event) SetFromIP(fromIP string) {
	e.FromIP = fromIP
}

func (e *Event) SetBrowserUA(browserUA string) {
	e.BrowserUA = browserUA
}

func (e *Event) SetMethod(method string) {
	e.Method = method
}

func (e *Event) CommitTo(db *gorm.DB, v *Visitor) error {
	e.BelongsTo = v.ID
	return AddEventForVisitor(db, v, e)
}

func (e *Event) BeforeCreate(tx *gorm.DB) (err error) {
	if e.ID != "" {
		return
	}
	uuid7, err := uuid.NewV7()
	if err != nil {
		return err
	}
	e.ID = uuid7.String()
	return
}
