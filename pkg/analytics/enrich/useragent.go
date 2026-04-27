package enrich

import (
	"strings"
	"sync"

	uaparser "github.com/ua-parser/uap-go/uaparser"
)

// UAFields is the enrichment result for a single User-Agent header.
type UAFields struct {
	Browser        string
	BrowserVersion string
	OS             string
	OSVersion      string
	// DeviceCategory: "mobile" | "tablet" | "desktop" | "bot" | "unknown".
	DeviceCategory string
	// IsBot is a convenience boolean; true when DeviceCategory == "bot".
	IsBot bool
}

// uaCachedParser is initialized lazily on first UA parse. uap-go loads a
// large regex file (~1MB) at startup, so we do it exactly once.
var (
	uaCachedParser *uaparser.Parser
	uaParserOnce   sync.Once
)

func getUAParser() *uaparser.Parser {
	uaParserOnce.Do(func() {
		uaCachedParser = uaparser.NewFromSaved()
	})
	return uaCachedParser
}

// ParseUA classifies a User-Agent string into its family, OS, and device
// category. Empty / unparseable strings return DeviceCategory="unknown".
func ParseUA(ua string) UAFields {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return UAFields{DeviceCategory: "unknown"}
	}

	client := getUAParser().Parse(ua)
	out := UAFields{
		Browser:        client.UserAgent.Family,
		BrowserVersion: joinVersion(client.UserAgent.Major, client.UserAgent.Minor, client.UserAgent.Patch),
		OS:             client.Os.Family,
		OSVersion:      joinVersion(client.Os.Major, client.Os.Minor, client.Os.Patch),
	}

	// Device classification. uap-go returns Device.Family for phones and
	// tablets (e.g., "iPhone", "iPad", "Samsung SM-G998B"); it returns
	// "Other" for desktops. The UA itself usually reveals mobile/tablet
	// via the "Mobile" / "Tablet" token.
	lcua := strings.ToLower(ua)
	switch {
	case isBotUA(lcua):
		out.DeviceCategory = "bot"
		out.IsBot = true
	case strings.Contains(lcua, "tablet") || client.Device.Family == "iPad":
		out.DeviceCategory = "tablet"
	case strings.Contains(lcua, "mobile") || strings.Contains(lcua, "android") || strings.Contains(lcua, "iphone"):
		out.DeviceCategory = "mobile"
	default:
		out.DeviceCategory = "desktop"
	}
	return out
}

func joinVersion(parts ...string) string {
	out := make([]string, 0, 3)
	for _, p := range parts {
		if p == "" {
			break
		}
		out = append(out, p)
	}
	return strings.Join(out, ".")
}

// isBotUA is a cheap substring check; uap-go's regex file doesn't cover
// every bot (especially new / obscure crawlers), so we supplement.
var botSubstrings = []string{
	"bot", "crawler", "spider", "scraper", "slurp", "mediapartners",
	"headlesschrome", "phantomjs", "curl/", "wget/", "python-requests/",
	"go-http-client/", "http.rb/", "java/", "libwww-perl", "httpclient",
	"facebookexternalhit", "ahrefs", "semrush", "mj12bot", "dotbot",
	"applebot", "googlebot", "bingbot", "yandexbot", "baiduspider",
	"duckduckbot",
}

func isBotUA(lcua string) bool {
	for _, s := range botSubstrings {
		if strings.Contains(lcua, s) {
			return true
		}
	}
	return false
}
