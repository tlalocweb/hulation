// Package builtinstatics holds Hula's bundled chat-widget assets
// (JS, CSS) plus a thin lookup API for the request handler.
//
// The files here are mustache templates rendered per-request with
// per-host variables (server_id, API paths, prefix-derived asset URLs,
// etc). They land in the binary via Go's embed directive so a hula
// build is self-contained — no sidecar assets to ship.
//
// Customer overlays:
//
//	Customers can override any of these files by dropping a copy at
//	the same URL path under their per-server static root. The
//	handler in handler/builtinstatic.go checks the host's Root
//	directory first; if a file exists there it wins. Only when no
//	overlay is present do we render the embedded template.
package builtinstatics

import (
	"embed"
	"io/fs"
)

//go:embed scripts styles
var assetsFS embed.FS

// Get returns the raw template bytes for an asset by its relative
// path inside this package's FS (e.g. "scripts/hula-chat.js"). The
// returned slice is a fresh copy — callers can mustache-render it
// safely without aliasing the embedded bytes.
func Get(relPath string) ([]byte, error) {
	data, err := fs.ReadFile(assetsFS, relPath)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(data))
	copy(out, data)
	return out, nil
}

// Has reports whether an asset exists at relPath. Used by tests +
// boot-time registration: only attach a handler when the embedded
// asset it would render is actually present.
func Has(relPath string) bool {
	f, err := assetsFS.Open(relPath)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
