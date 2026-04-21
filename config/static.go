package config

// StaticHostConfig holds configuration for a static-file serving host.
// Used by pkg/server/static/static.go to route hostnames to filesystem
// paths served over the unified HTTPS listener.
//
// In hula, per-server site build output is typically served via the
// Server config's build path; StaticHostConfig provides a lighter-weight
// way to serve prebuilt static assets (e.g., the analytics Svelte SPA
// mounted at analytics.hula.local).
type StaticHostConfig struct {
	Server            string              `yaml:"server"`               // Hostname to match
	Root              string              `yaml:"root"`                 // Filesystem path to serve
	SPA               bool                `yaml:"spa"`                  // Serve index.html for missing paths
	TLSCert           string              `yaml:"tls_cert"`             // Optional per-host certificate
	TLSKey            string              `yaml:"tls_key"`              // Optional per-host key
	LogAccess         bool                `yaml:"log_access"`           // Log access to static files (default: false)
	LogAccessLogLevel string              `yaml:"log_access_log_level"` // debug, trace, log, info (default: debug)
	MimeOverrides     []map[string]string `yaml:"mime_overrides"`       // e.g., ".js": "application/javascript"
}
