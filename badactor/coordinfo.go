package badactor

// Reverse-geocode (lat, lon) → ISO country code via OpenStreetMap
// Nominatim. Designed for the chat-start hot path: the iOS / Android
// operator app surfaces a Location card on each session header, and
// when a visitor widget posts browser-derived coordinates we want a
// 2-letter country to display. Pairs with badactor/ipinfo.go, which
// handles the IP-fallback case.
//
// Nominatim's free public endpoint allows ~1 req/sec — that's the
// hard ceiling. The LRU cache (24h, ~1km grid) absorbs the rest;
// returning visitors and visitor bursts from the same region miss
// the network entirely.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	lru "github.com/hashicorp/golang-lru/v2/expirable"
)

const (
	nominatimURL     = "https://nominatim.openstreetmap.org/reverse"
	nominatimUA      = "Hulation/1.0 (+https://github.com/tlalocweb/hulation)"
	coordsCacheTTL   = 24 * time.Hour
	coordsCacheSize  = 4096
	nominatimMinGap  = 1100 * time.Millisecond // 1 req/sec policy + slack
	nominatimTimeout = 5 * time.Second
	coordsMaxWait    = 2 * time.Second // give up rather than blocking chat-start
)

// CoordsInfo is the reverse-geocoded result for a single (lat, lon)
// pair. Sparse on purpose — callers today need CountryCode only; add
// fields when other surfaces grow new requirements.
type CoordsInfo struct {
	CountryCode string
	Country     string
	LookedUpAt  time.Time
}

var (
	coordsCache  *lru.LRU[string, *CoordsInfo]
	nominatimTok chan struct{}
)

func init() {
	coordsCache = lru.NewLRU[string, *CoordsInfo](coordsCacheSize, nil, coordsCacheTTL)
	// Token bucket of size 1 refilled every nominatimMinGap. The
	// initial token is added immediately so the first request doesn't
	// have to wait for the ticker.
	nominatimTok = make(chan struct{}, 1)
	nominatimTok <- struct{}{}
	go func() {
		t := time.NewTicker(nominatimMinGap)
		defer t.Stop()
		for range t.C {
			select {
			case nominatimTok <- struct{}{}:
			default:
			}
		}
	}()
}

// LookupCoordsCountry returns a 2-letter uppercase ISO country code
// for the visitor's reported (lat, lon) via Nominatim. Cache hits
// return instantly; misses do one rate-limited HTTP fetch. Returns
// "" on bounds-check failure, rate-limit timeout, network error, or
// unparseable response — callers fall back to IP-based enrichment.
func LookupCoordsCountry(lat, lon float64) string {
	if math.IsNaN(lat) || math.IsNaN(lon) ||
		lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return ""
	}
	key := coordsCacheKey(lat, lon)
	if info, ok := coordsCache.Get(key); ok {
		return info.CountryCode
	}
	select {
	case <-nominatimTok:
	case <-time.After(coordsMaxWait):
		baLog.Debugf("coordinfo: rate-limit timeout for %s", key)
		return ""
	}
	info := fetchCoordsInfo(lat, lon)
	if info == nil {
		return ""
	}
	coordsCache.Add(key, info)
	return info.CountryCode
}

// coordsCacheKey rounds coords to a ~1km grid (0.01°). Country
// boundaries are wide enough that this almost never collides across
// borders, and dedupes neighbouring visitors aggressively.
func coordsCacheKey(lat, lon float64) string {
	return fmt.Sprintf("%.2f,%.2f", lat, lon)
}

func fetchCoordsInfo(lat, lon float64) *CoordsInfo {
	u, err := url.Parse(nominatimURL)
	if err != nil {
		return nil
	}
	q := u.Query()
	q.Set("lat", fmt.Sprintf("%.6f", lat))
	q.Set("lon", fmt.Sprintf("%.6f", lon))
	q.Set("format", "json")
	q.Set("zoom", "3") // country-level granularity is all we need
	q.Set("accept-language", "en")
	u.RawQuery = q.Encode()

	ctx, cancel := context.WithTimeout(context.Background(), nominatimTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", nominatimUA)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		baLog.Debugf("coordinfo: nominatim fetch error: %s", err.Error())
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		baLog.Debugf("coordinfo: nominatim status %d", resp.StatusCode)
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil
	}
	var parsed struct {
		Address struct {
			Country     string `json:"country"`
			CountryCode string `json:"country_code"`
		} `json:"address"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		baLog.Debugf("coordinfo: parse error: %s", err.Error())
		return nil
	}
	cc := strings.ToUpper(strings.TrimSpace(parsed.Address.CountryCode))
	if cc == "" {
		return nil
	}
	return &CoordsInfo{
		CountryCode: cc,
		Country:     parsed.Address.Country,
		LookedUpAt:  time.Now(),
	}
}
