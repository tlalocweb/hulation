// Package enrich adds structured fields to raw visitor events before
// they're written to ClickHouse. Phase 0 stage 0.9a — referrer, UTM,
// user-agent, and session-id derivation. This package has no ClickHouse
// dependency; the event writer calls these helpers and attaches the
// results to the model.Event row.

package enrich

import (
	"net/url"
	"strings"
)

// Channel values assigned by ClassifyReferrer.
const (
	ChannelDirect   = "Direct"
	ChannelSearch   = "Search"
	ChannelSocial   = "Social"
	ChannelReferral = "Referral"
	ChannelEmail    = "Email"
)

// ReferrerInfo is the enrichment result for a single referrer header.
type ReferrerInfo struct {
	// Channel is one of the Channel* constants.
	Channel string
	// Host is the parsed referrer hostname (lowercased, without port).
	// Empty if referrer was empty or unparseable.
	Host string
	// SearchTerm is the q=... / query=... value if the referrer is a search
	// engine. Empty otherwise.
	SearchTerm string
}

// knownSearch maps hostname suffix → channel classification. Lookup is
// longest-suffix match via a simple loop; the list is small enough that
// a trie is overkill.
var knownSearch = []string{
	"google.", "bing.com", "duckduckgo.com", "yandex.", "yahoo.",
	"baidu.com", "ecosia.org", "startpage.com", "brave.com", "kagi.com",
}

var knownSocial = []string{
	"twitter.com", "x.com", "t.co", "facebook.com", "fb.com",
	"linkedin.com", "lnkd.in", "reddit.com", "redd.it",
	"mastodon.social", "bsky.app", "threads.net",
	"youtube.com", "youtu.be", "tiktok.com", "instagram.com",
	"pinterest.com", "tumblr.com", "discord.com", "news.ycombinator.com",
}

var knownEmail = []string{
	"mail.google.com", "gmail.com", "outlook.com", "outlook.live.com",
	"outlook.office.com", "mail.yahoo.com", "mail.proton.me",
	"fastmail.com", "mail.qq.com",
}

// searchQueryKeys are the common query-string keys carrying the user's
// search term across known search engines.
var searchQueryKeys = []string{"q", "query", "p", "text", "wd"}

// ClassifyReferrer parses a Referer header value and returns channel,
// host, and optional search term. Empty referrer → ChannelDirect.
func ClassifyReferrer(referer, ownHost string) ReferrerInfo {
	referer = strings.TrimSpace(referer)
	if referer == "" {
		return ReferrerInfo{Channel: ChannelDirect}
	}
	u, err := url.Parse(referer)
	if err != nil || u.Host == "" {
		return ReferrerInfo{Channel: ChannelDirect}
	}

	host := strings.ToLower(u.Host)
	if strings.Contains(host, ":") {
		host = host[:strings.Index(host, ":")]
	}

	// Self-referral — treat as Direct; we don't want pagination inside
	// the site to look like external referrers.
	if ownHost != "" && (host == ownHost || strings.HasSuffix(host, "."+ownHost)) {
		return ReferrerInfo{Channel: ChannelDirect, Host: host}
	}

	info := ReferrerInfo{Host: host}

	// Email first — several webmail hosts live under search-engine domains
	// (mail.google.com, mail.yahoo.com), so this would otherwise be
	// misclassified as Search.
	for _, e := range knownEmail {
		if host == e || strings.HasSuffix(host, "."+e) {
			info.Channel = ChannelEmail
			return info
		}
	}

	// Social.
	for _, s := range knownSocial {
		if host == s || strings.HasSuffix(host, "."+s) {
			info.Channel = ChannelSocial
			return info
		}
	}

	// Search engines.
	for _, pfx := range knownSearch {
		if strings.Contains(host, pfx) {
			info.Channel = ChannelSearch
			// Pluck the search term from the query string.
			q := u.Query()
			for _, k := range searchQueryKeys {
				if v := q.Get(k); v != "" {
					info.SearchTerm = v
					break
				}
			}
			return info
		}
	}

	// Fallback: anything else with a host is a referral.
	info.Channel = ChannelReferral
	return info
}

// UTMFields captures the five standard UTM parameters plus any gclid/fbclid
// click IDs that may accompany them.
type UTMFields struct {
	Source   string
	Medium   string
	Campaign string
	Term     string
	Content  string
	// Click IDs — useful for stitching with ad-platform attribution.
	GCLID  string
	FBCLID string
}

// ParseUTM extracts UTM query params from a landing URL.
func ParseUTM(landingURL string) UTMFields {
	u, err := url.Parse(landingURL)
	if err != nil {
		return UTMFields{}
	}
	q := u.Query()
	return UTMFields{
		Source:   q.Get("utm_source"),
		Medium:   q.Get("utm_medium"),
		Campaign: q.Get("utm_campaign"),
		Term:     q.Get("utm_term"),
		Content:  q.Get("utm_content"),
		GCLID:    q.Get("gclid"),
		FBCLID:   q.Get("fbclid"),
	}
}

// HasAny reports whether any UTM or click-ID field is populated.
func (u UTMFields) HasAny() bool {
	return u.Source != "" || u.Medium != "" || u.Campaign != "" ||
		u.Term != "" || u.Content != "" || u.GCLID != "" || u.FBCLID != ""
}
