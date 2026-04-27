package enrich

import (
	"testing"
	"time"
)

func TestClassifyReferrer(t *testing.T) {
	cases := []struct {
		name       string
		referer    string
		ownHost    string
		wantChan   string
		wantHost   string
		wantSearch string
	}{
		{"empty → direct", "", "", ChannelDirect, "", ""},
		{"self → direct", "https://example.com/pricing", "example.com", ChannelDirect, "example.com", ""},
		{"self subdomain → direct", "https://blog.example.com/", "example.com", ChannelDirect, "blog.example.com", ""},
		{"google search", "https://www.google.com/search?q=hula+analytics", "", ChannelSearch, "www.google.com", "hula analytics"},
		{"bing", "https://www.bing.com/search?q=hula", "", ChannelSearch, "www.bing.com", "hula"},
		{"ddg", "https://duckduckgo.com/?q=hula", "", ChannelSearch, "duckduckgo.com", "hula"},
		{"twitter → social", "https://t.co/abc", "", ChannelSocial, "t.co", ""},
		{"x.com → social", "https://x.com/someone/status/123", "", ChannelSocial, "x.com", ""},
		{"gmail → email", "https://mail.google.com/mail", "", ChannelEmail, "mail.google.com", ""},
		{"unknown → referral", "https://some-blog.example.net/post", "example.com", ChannelReferral, "some-blog.example.net", ""},
		{"unparseable → direct", "::not::a::url::", "", ChannelDirect, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ClassifyReferrer(c.referer, c.ownHost)
			if got.Channel != c.wantChan {
				t.Errorf("channel: got %q, want %q", got.Channel, c.wantChan)
			}
			if got.Host != c.wantHost {
				t.Errorf("host: got %q, want %q", got.Host, c.wantHost)
			}
			if got.SearchTerm != c.wantSearch {
				t.Errorf("search term: got %q, want %q", got.SearchTerm, c.wantSearch)
			}
		})
	}
}

func TestParseUTM(t *testing.T) {
	u := "https://example.com/?utm_source=ghpost&utm_medium=social&utm_campaign=launch&utm_term=hula&utm_content=top&gclid=abc"
	got := ParseUTM(u)
	if got.Source != "ghpost" || got.Medium != "social" || got.Campaign != "launch" ||
		got.Term != "hula" || got.Content != "top" || got.GCLID != "abc" {
		t.Errorf("UTM mismatch: %+v", got)
	}
	if !got.HasAny() {
		t.Error("HasAny should be true")
	}

	empty := ParseUTM("https://example.com/")
	if empty.HasAny() {
		t.Errorf("empty URL should have no UTMs: %+v", empty)
	}
}

func TestParseUA(t *testing.T) {
	cases := []struct {
		name      string
		ua        string
		wantCat   string
		wantIsBot bool
	}{
		{
			"chrome desktop",
			"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
			"desktop", false,
		},
		{
			"iphone safari",
			"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
			"mobile", false,
		},
		{
			"ipad",
			"Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
			"tablet", false,
		},
		{
			"googlebot",
			"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
			"bot", true,
		},
		{
			"curl",
			"curl/7.81.0",
			"bot", true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParseUA(c.ua)
			if got.DeviceCategory != c.wantCat {
				t.Errorf("device category: got %q, want %q (fields=%+v)", got.DeviceCategory, c.wantCat, got)
			}
			if got.IsBot != c.wantIsBot {
				t.Errorf("isBot: got %v, want %v", got.IsBot, c.wantIsBot)
			}
		})
	}
}

func TestSessionIDReuseWithinWindow(t *testing.T) {
	ResetSessionCacheForTesting()
	t0 := time.Now()
	id1 := SessionIDForVisitor("v1", t0)
	id2 := SessionIDForVisitor("v1", t0.Add(5*time.Minute))
	if id1 != id2 {
		t.Errorf("expected session reuse within window; got %q then %q", id1, id2)
	}
}

func TestSessionIDRefreshesAfterWindow(t *testing.T) {
	ResetSessionCacheForTesting()
	t0 := time.Now()
	id1 := SessionIDForVisitor("v1", t0)
	id2 := SessionIDForVisitor("v1", t0.Add(SessionWindow+time.Second))
	if id1 == id2 {
		t.Errorf("expected new session after window; both were %q", id1)
	}
}

func TestSessionPrune(t *testing.T) {
	ResetSessionCacheForTesting()
	t0 := time.Now()
	SessionIDForVisitor("v1", t0)                          // last seen at t0
	SessionIDForVisitor("v2", t0.Add(45*time.Minute))      // last seen at t0+45m
	if SessionCacheSize() != 2 {
		t.Errorf("expected 2 cached, got %d", SessionCacheSize())
	}
	// At t0+60m, prune entries older than 30 min. Cutoff = t0+30m.
	// v1 (last seen t0) is older; v2 (last seen t0+45m) is not.
	removed := PruneExpiredSessions(t0.Add(60*time.Minute), 30*time.Minute)
	if removed != 1 {
		t.Errorf("expected 1 pruned, got %d", removed)
	}
	if SessionCacheSize() != 1 {
		t.Errorf("expected 1 remaining, got %d", SessionCacheSize())
	}
}
