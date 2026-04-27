package badactor

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// IPInfo holds cached geolocation and ASN data for an IP address.
type IPInfo struct {
	Country     string
	CountryCode string
	Region      string
	City        string
	ASN         string // e.g. "AS16509"
	Org         string // e.g. "Amazon.com, Inc."
	ISP         string
	LookedUpAt  time.Time
}

// Format returns a compact string like "AS16509 Amazon.com, SG"
func (i *IPInfo) Format() string {
	if i == nil {
		return ""
	}
	asn := i.ASN
	// ip-api returns "AS16509 Amazon.com, Inc." in the "as" field — split to get just the ASN number
	if idx := strings.IndexByte(asn, ' '); idx > 0 {
		asn = asn[:idx]
	}
	org := i.Org
	if org == "" {
		org = i.ISP
	}
	if asn == "" && org == "" && i.CountryCode == "" {
		return ""
	}
	parts := []string{}
	if asn != "" {
		parts = append(parts, asn)
	}
	if org != "" {
		parts = append(parts, org)
	}
	if i.CountryCode != "" {
		parts = append(parts, i.CountryCode)
	}
	return strings.Join(parts, " ")
}

// --- Cache ---

var (
	ipInfoCache sync.Map // map[string]*IPInfo
	ipInfoDB    *gorm.DB
	// Rate limiter: track requests to avoid exceeding 45/min
	ipInfoMu       sync.Mutex
	ipInfoRequests int
	ipInfoWindow   time.Time
	// ipInfoUseHTTPS toggles https:// vs http:// for ip-api.com. The free
	// tier is HTTP-only; HTTPS requires an ip-api Pro plan.
	ipInfoUseHTTPS bool
)

const (
	ipInfoCacheTTL     = 7 * 24 * time.Hour // 7 days
	ipInfoRateLimit    = 40                  // stay under 45/min
	ipInfoRateWindow   = time.Minute
	ipInfoAPITimeout   = 5 * time.Second
)

// InitIPInfoCache sets the database for persistent IP info caching. The
// useHTTPS flag controls whether the ip-api.com lookup uses https://
// (requires Pro plan) or http:// (free tier).
func InitIPInfoCache(db *gorm.DB, useHTTPS bool) {
	ipInfoDB = db
	ipInfoUseHTTPS = useHTTPS
	// Create table
	if err := db.Exec(sqlCreateIPInfoCache).Error; err != nil {
		baLog.Warnf("ipinfo: failed to create cache table: %s", err)
		return
	}
	// Load existing cache into memory
	var records []IPInfoRecord
	if err := db.Find(&records).Error; err != nil {
		baLog.Warnf("ipinfo: failed to load cache from DB: %s", err)
		return
	}
	loaded := 0
	for _, r := range records {
		if time.Since(r.LookedUpAt) < ipInfoCacheTTL {
			ipInfoCache.Store(r.IP, &IPInfo{
				Country:     r.Country,
				CountryCode: r.CountryCode,
				Region:      r.Region,
				City:        r.City,
				ASN:         r.ASN,
				Org:         r.Org,
				ISP:         r.ISP,
				LookedUpAt:  r.LookedUpAt,
			})
			loaded++
		}
	}
	if loaded > 0 {
		baLog.Infof("ipinfo: loaded %d cached IP lookups from DB", loaded)
	}
}

// GetIPInfo returns cached IP info if available, or nil.
// Does NOT trigger a lookup — use LookupIPInfoAsync for that.
func GetIPInfo(ip string) *IPInfo {
	if v, ok := ipInfoCache.Load(ip); ok {
		info := v.(*IPInfo)
		if time.Since(info.LookedUpAt) < ipInfoCacheTTL {
			return info
		}
	}
	return nil
}

// FormatIPInfoCached returns a formatted string for the IP if cached, or empty string.
func FormatIPInfoCached(ip string) string {
	info := GetIPInfo(ip)
	if info == nil {
		return ""
	}
	return info.Format()
}

// LookupAndFormatIPInfo does a synchronous lookup (cache first, then API) and returns
// the formatted string. Use when you can afford to block (e.g. dropping a connection anyway).
func LookupAndFormatIPInfo(ip string) string {
	info := GetIPInfo(ip)
	if info != nil {
		return info.Format()
	}
	info = fetchIPInfo(ip)
	if info == nil {
		return ""
	}
	ipInfoCache.Store(ip, info)
	if ipInfoDB != nil {
		go persistIPInfo(ip, info)
	}
	return info.Format()
}

// LookupIPInfoAsync looks up IP info in the background and caches the result.
func LookupIPInfoAsync(ip string) {
	// Skip if already cached
	if GetIPInfo(ip) != nil {
		return
	}
	go func() {
		info := fetchIPInfo(ip)
		if info == nil {
			return
		}
		ipInfoCache.Store(ip, info)
		// Persist to DB
		if ipInfoDB != nil {
			persistIPInfo(ip, info)
		}
	}()
}

// --- API ---

type ipAPIResponse struct {
	Status      string `json:"status"`
	Country     string `json:"country"`
	CountryCode string `json:"countryCode"`
	RegionName  string `json:"regionName"`
	City        string `json:"city"`
	ISP         string `json:"isp"`
	Org         string `json:"org"`
	AS          string `json:"as"`
}

func fetchIPInfo(ip string) *IPInfo {
	// Rate limiting
	ipInfoMu.Lock()
	now := time.Now()
	if now.Sub(ipInfoWindow) > ipInfoRateWindow {
		ipInfoRequests = 0
		ipInfoWindow = now
	}
	if ipInfoRequests >= ipInfoRateLimit {
		ipInfoMu.Unlock()
		baLog.Debugf("ipinfo: rate limited, skipping lookup for %s", ip)
		return nil
	}
	ipInfoRequests++
	ipInfoMu.Unlock()

	client := &http.Client{Timeout: ipInfoAPITimeout}
	scheme := "http"
	if ipInfoUseHTTPS {
		scheme = "https"
	}
	url := fmt.Sprintf("%s://ip-api.com/json/%s?fields=status,country,countryCode,regionName,city,isp,org,as", scheme, ip)
	resp, err := client.Get(url)
	if err != nil {
		baLog.Debugf("ipinfo: lookup failed for %s: %s", ip, err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		baLog.Debugf("ipinfo: HTTP %d for %s", resp.StatusCode, ip)
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	var apiResp ipAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil
	}
	if apiResp.Status != "success" {
		return nil
	}

	return &IPInfo{
		Country:     apiResp.Country,
		CountryCode: apiResp.CountryCode,
		Region:      apiResp.RegionName,
		City:        apiResp.City,
		ASN:         apiResp.AS,
		Org:         apiResp.Org,
		ISP:         apiResp.ISP,
		LookedUpAt:  time.Now(),
	}
}

// --- DB model ---

type IPInfoRecord struct {
	ID          string    `gorm:"column:id;primaryKey"`
	IP          string    `gorm:"column:ip"`
	Country     string    `gorm:"column:country"`
	CountryCode string    `gorm:"column:country_code"`
	Region      string    `gorm:"column:region"`
	City        string    `gorm:"column:city"`
	ASN         string    `gorm:"column:asn"`
	Org         string    `gorm:"column:org"`
	ISP         string    `gorm:"column:isp"`
	LookedUpAt  time.Time `gorm:"column:looked_up_at"`
}

func (IPInfoRecord) TableName() string { return "ip_info_cache" }

const sqlCreateIPInfoCache = `CREATE TABLE IF NOT EXISTS ` + "`ip_info_cache`" + `
(
    ` + "`id`" + ` String,
    ` + "`ip`" + ` String,
    ` + "`country`" + ` String,
    ` + "`country_code`" + ` String,
    ` + "`region`" + ` String,
    ` + "`city`" + ` String,
    ` + "`asn`" + ` String,
    ` + "`org`" + ` String,
    ` + "`isp`" + ` String,
    ` + "`looked_up_at`" + ` DateTime64(3)
)
ENGINE = ReplacingMergeTree(looked_up_at)
ORDER BY (ip);`

func persistIPInfo(ip string, info *IPInfo) {
	rec := IPInfoRecord{
		ID:          uuid.Must(uuid.NewV7()).String(),
		IP:          ip,
		Country:     info.Country,
		CountryCode: info.CountryCode,
		Region:      info.Region,
		City:        info.City,
		ASN:         info.ASN,
		Org:         info.Org,
		ISP:         info.ISP,
		LookedUpAt:  info.LookedUpAt,
	}
	if err := ipInfoDB.Create(&rec).Error; err != nil {
		baLog.Debugf("ipinfo: failed to persist cache for %s: %s", ip, err)
	}
}
