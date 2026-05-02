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
	lru "github.com/hashicorp/golang-lru/v2/expirable"
	"gorm.io/gorm"

	"github.com/tlalocweb/hulation/pkg/tune"
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
//
// In-process cache is a bounded LRU sized by an approximate byte
// budget (tune.GetIPInfoCacheMaxBytes). When the budget is exhausted
// the oldest entry is evicted. Entries past the TTL are filtered on
// read so a stale-but-still-resident hit triggers a refresh instead
// of being returned. The DB-backed cache (ClickHouse `ip_info_cache`
// table) survives restarts and reseeds the LRU at boot.
//
// Per-entry size is hard to measure exactly in Go (struct + map
// bookkeeping + LRU linked-list nodes + the variable-length string
// fields), so we estimate at avgEntryBytes and turn the byte budget
// into a fixed entry count at init time. avgEntryBytes deliberately
// errs high so the in-memory footprint stays under the budget.

const (
	avgEntryBytes      = 512
	defaultMaxEntries  = 32 * 1024 // safety floor when budget is misconfigured
	ipInfoRateWindow   = time.Minute
	ipInfoAPITimeout   = 5 * time.Second
)

var (
	ipInfoCache *lru.LRU[string, *IPInfo]
	ipInfoCacheMu sync.RWMutex
	ipInfoDB    *gorm.DB
	// Rate limiter: track requests to avoid exceeding the configured limit.
	ipInfoMu       sync.Mutex
	ipInfoRequests int
	ipInfoWindow   time.Time
)

// loadCache returns the active LRU snapshot. Read-locked because the
// pointer can be swapped by future reload paths; the LRU itself is
// internally synchronised so we don't hold the lock during use.
func loadCache() *lru.LRU[string, *IPInfo] {
	ipInfoCacheMu.RLock()
	defer ipInfoCacheMu.RUnlock()
	return ipInfoCache
}

// InitIPInfoCache sets the database for persistent IP info caching
// and builds the in-process LRU sized from tune.GetIPInfoCacheMaxBytes.
// The useHTTPS flag is retained for backwards compatibility with the
// existing call site but the canonical source is now
// tune.GetIPInfoUseHTTPS — the param is ignored when the tunable is
// also true. (Yes, it's a temporary belt-and-suspenders.)
func InitIPInfoCache(db *gorm.DB, _useHTTPS bool) {
	ipInfoDB = db

	// Size the LRU from the byte-budget tunable. 0 = disabled →
	// build a minimal cache so existing callers don't have to nil-check.
	maxBytes := tune.GetIPInfoCacheMaxBytes()
	maxEntries := int(maxBytes / avgEntryBytes)
	if maxEntries <= 0 {
		maxEntries = 1 // disabled — caller still gets a working pointer
	}
	if maxEntries > defaultMaxEntries*4 {
		// soft ceiling: even a generous 64MB budget caps at ~131k entries
		maxEntries = defaultMaxEntries * 4
	}
	ttl := tune.GetIPInfoCacheTTL()

	ipInfoCacheMu.Lock()
	ipInfoCache = lru.NewLRU[string, *IPInfo](maxEntries, nil, ttl)
	ipInfoCacheMu.Unlock()

	baLog.Infof("ipinfo: cache budget=%dMB entries=%d ttl=%s",
		maxBytes/1024/1024, maxEntries, ttl)

	// Create table
	if err := db.Exec(sqlCreateIPInfoCache).Error; err != nil {
		baLog.Warnf("ipinfo: failed to create cache table: %s", err)
		return
	}
	// Load existing cache into memory (oldest-first so newer rows win
	// on collision — ReplacingMergeTree may surface duplicates).
	var records []IPInfoRecord
	if err := db.Find(&records).Error; err != nil {
		baLog.Warnf("ipinfo: failed to load cache from DB: %s", err)
		return
	}
	cache := loadCache()
	loaded := 0
	for _, r := range records {
		if time.Since(r.LookedUpAt) < ttl {
			cache.Add(r.IP, &IPInfo{
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
	cache := loadCache()
	if cache == nil {
		return nil
	}
	if info, ok := cache.Get(ip); ok {
		// LRU enforces TTL on its own; this second check guards against
		// records that were resurrected from the DB with a stale
		// timestamp before our LRU TTL kicked in.
		if time.Since(info.LookedUpAt) < tune.GetIPInfoCacheTTL() {
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
	if cache := loadCache(); cache != nil {
		cache.Add(ip, info)
	}
	if ipInfoDB != nil {
		go persistIPInfo(ip, info)
	}
	return info.Format()
}

// pendingLookups dedupes concurrent async lookups for the same IP.
// Without it a burst of analytics events for a freshly-seen visitor
// would each spawn their own goroutine and each consume one of our
// 40-per-minute outbound API tokens.
var pendingLookups sync.Map // map[string]struct{}

// LookupIPInfoAsync looks up IP info in the background and caches the
// result. Safe to call from hot paths — the function returns
// immediately. No-op when the IP is already cached or another lookup
// for the same IP is already in flight.
func LookupIPInfoAsync(ip string) {
	if ip == "" {
		return
	}
	if GetIPInfo(ip) != nil {
		return
	}
	if _, busy := pendingLookups.LoadOrStore(ip, struct{}{}); busy {
		return
	}
	go func() {
		defer pendingLookups.Delete(ip)
		info := fetchIPInfo(ip)
		if info == nil {
			return
		}
		if cache := loadCache(); cache != nil {
			cache.Add(ip, info)
		}
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
	rateLimit := tune.GetIPInfoRateLimit()
	if rateLimit <= 0 {
		rateLimit = 40
	}
	ipInfoMu.Lock()
	now := time.Now()
	if now.Sub(ipInfoWindow) > ipInfoRateWindow {
		ipInfoRequests = 0
		ipInfoWindow = now
	}
	if ipInfoRequests >= rateLimit {
		ipInfoMu.Unlock()
		baLog.Debugf("ipinfo: rate limited, skipping lookup for %s", ip)
		return nil
	}
	ipInfoRequests++
	ipInfoMu.Unlock()

	client := &http.Client{Timeout: ipInfoAPITimeout}
	scheme := "http"
	if tune.GetIPInfoUseHTTPS() {
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
