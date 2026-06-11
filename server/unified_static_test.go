package server

import (
	"testing"

	"github.com/tlalocweb/hulation/pkg/tune"
)

// TestStaticPassthroughIncludesWidgetManifest locks the fix for the bug where
// the signed widget manifest (a single file at /<prefix>widget-manifest.json,
// outside the scripts/styles/html namespaces) was intercepted by the per-host
// static FileServer and 404'd, silently disabling manifest verification on
// every install with a configured root.
func TestStaticPassthroughIncludesWidgetManifest(t *testing.T) {
	bp := tune.GetBuiltinStaticPrefix() // default "hula-"

	cases := []struct {
		path string
		want bool
	}{
		// Service routes.
		{"/api/v1/chat/start", true},
		{"/v/hello", true},
		{"/analytics", true},
		{"/hulastatus", true},
		// Built-in assets.
		{"/" + bp + "scripts/hula-chat.js", true},
		{"/" + bp + "styles/hula-chat.css", true},
		{"/" + bp + "html/anything", true},
		// The widget manifest — the regression this test guards.
		{"/" + bp + "widget-manifest.json", true},
		// Ordinary static-site content must NOT pass through.
		{"/index.html", false},
		{"/about/", false},
		{"/" + bp + "widget-manifest.json.bak", false},
		{"/" + bp + "other.json", false},
	}
	for _, c := range cases {
		if got := staticPassthrough(c.path); got != c.want {
			t.Errorf("staticPassthrough(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
