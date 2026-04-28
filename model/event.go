package model

import (
	"time"

	"github.com/google/uuid"
	"github.com/tlalocweb/hulation/pkg/analytics/enrich"
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

	// --- Phase 0 stage 0.9 enrichment fields ---
	// Populated by pkg/analytics/enrich at ingest in handler/visitor.go.
	// Legacy rows have empty values; the events_v1 migration in
	// pkg/store/clickhouse/migrations/ fills defaults during the
	// RENAME-TABLE swap.

	// SessionID groups events from the same visitor within a 30-minute
	// inactivity window. Derived in enrich.SessionIDForVisitor.
	SessionID string `json:"session_id" gorm:"column:session_id"`
	// ServerID is the hula virtual-server ID that owned the traffic.
	// Denormalized from Host for per-server analytics filtering without
	// a JOIN.
	ServerID string `json:"server_id" gorm:"column:server_id"`
	// Referer header as captured (previously dropped entirely).
	Referer     string `json:"referer" gorm:"column:referer"`
	RefererHost string `json:"referer_host" gorm:"column:referer_host"`
	// Channel classification: Direct / Search / Social / Referral / Email.
	Channel    string `json:"channel" gorm:"column:channel"`
	SearchTerm string `json:"search_term" gorm:"column:search_term"`
	// UTM + click-ID attribution.
	UTMSource   string `json:"utm_source" gorm:"column:utm_source"`
	UTMMedium   string `json:"utm_medium" gorm:"column:utm_medium"`
	UTMCampaign string `json:"utm_campaign" gorm:"column:utm_campaign"`
	UTMTerm     string `json:"utm_term" gorm:"column:utm_term"`
	UTMContent  string `json:"utm_content" gorm:"column:utm_content"`
	GCLID       string `json:"gclid" gorm:"column:gclid"`
	FBCLID      string `json:"fbclid" gorm:"column:fbclid"`
	// User-agent classification (via uap-go).
	Browser        string `json:"browser" gorm:"column:browser"`
	BrowserVersion string `json:"browser_version" gorm:"column:browser_version"`
	OS             string `json:"os" gorm:"column:os"`
	OSVersion      string `json:"os_version" gorm:"column:os_version"`
	// DeviceCategory: mobile / tablet / desktop / bot / unknown.
	DeviceCategory string `json:"device_category" gorm:"column:device_category"`
	IsBot          bool   `json:"is_bot" gorm:"column:is_bot"`
	// Geo enrichment (sourced from the ipinfo cache).
	CountryCode string `json:"country_code" gorm:"column:country_code"`
	Region      string `json:"region" gorm:"column:region"`
	City        string `json:"city" gorm:"column:city"`

	// --- Phase 4c.1 consent state ---
	// ConsentAnalytics — visitor consents to analytics processing.
	// ConsentMarketing — visitor consents to marketing/advertising
	// processing (gates server-side forwarders to ad platforms).
	ConsentAnalytics bool `json:"consent_analytics" gorm:"column:consent_analytics"`
	ConsentMarketing bool `json:"consent_marketing" gorm:"column:consent_marketing"`
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

// ApplyEnrichment populates the Phase-0 enrichment fields on this Event
// from raw request inputs. Called by handler/visitor.go just before
// CommitTo. Safe to call with empty inputs — each enrichment step
// no-ops on missing data.
//
// Arguments:
//
//	visitorID   — the visitor owning this event (used for session ID).
//	ownHost     — hula's own hostname (used to classify self-referrals as Direct).
//	serverID    — the hula server_id from config that owned the traffic.
//	referer     — raw Referer header (usually empty for server-rendered sites).
//	ua          — raw User-Agent header.
//	countryCode — populated from badactor.GetIPInfo (caller-supplied).
//	region      — same.
//	city        — same.
//
// The landing URL used for UTM parsing is e.URL; callers set that via
// SetURL before invoking this function.
func (e *Event) ApplyEnrichment(visitorID, ownHost, serverID, referer, ua, countryCode, region, city string) {
	e.ServerID = serverID
	e.CountryCode = countryCode
	e.Region = region
	e.City = city

	if visitorID != "" {
		when := e.When
		if when.IsZero() {
			when = time.Now()
		}
		e.SessionID = enrich.SessionIDForVisitor(visitorID, when)
	}
	if referer != "" {
		e.Referer = referer
		ri := enrich.ClassifyReferrer(referer, ownHost)
		e.Channel = ri.Channel
		e.RefererHost = ri.Host
		e.SearchTerm = ri.SearchTerm
	} else {
		e.Channel = enrich.ChannelDirect
	}
	if e.URL != "" {
		u := enrich.ParseUTM(e.URL)
		e.UTMSource = u.Source
		e.UTMMedium = u.Medium
		e.UTMCampaign = u.Campaign
		e.UTMTerm = u.Term
		e.UTMContent = u.Content
		e.GCLID = u.GCLID
		e.FBCLID = u.FBCLID
	}
	if ua != "" {
		uaf := enrich.ParseUA(ua)
		e.Browser = uaf.Browser
		e.BrowserVersion = uaf.BrowserVersion
		e.OS = uaf.OS
		e.OSVersion = uaf.OSVersion
		e.DeviceCategory = uaf.DeviceCategory
		e.IsBot = uaf.IsBot
	}
}
