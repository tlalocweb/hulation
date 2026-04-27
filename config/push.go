package config

// APNSConfig carries Apple Push Notification service credentials.
// Optional — when any required field is empty the notifier's APNs
// backend reports ErrNotConfigured per recipient and the composite
// notifier skips the channel. This lets an operator bring hula up
// without Apple creds and still deliver push via FCM (or email-only
// if neither is configured).
//
// KeyPEMPath should point at the p8 private-key file Apple issues
// for a push-notification key. The key stays on disk in PEM form;
// the APNs backend loads it at boot into an *ecdsa.PrivateKey.
type APNSConfig struct {
	// TeamID is the 10-char Apple Developer team identifier.
	TeamID string `yaml:"team_id,omitempty" env:"HULA_APNS_TEAM_ID"`
	// KeyID — the p8 key's ID (shown in the Apple Developer portal).
	KeyID string `yaml:"key_id,omitempty" env:"HULA_APNS_KEY_ID"`
	// KeyPEMPath is the filesystem path to the p8 private-key PEM.
	KeyPEMPath string `yaml:"key_pem_path,omitempty" env:"HULA_APNS_KEY_PEM_PATH"`
	// BundleID is the iOS app's bundle identifier. Carried on every
	// push as the apns-topic HTTP/2 header.
	BundleID string `yaml:"bundle_id,omitempty" env:"HULA_APNS_BUNDLE_ID"`
	// Endpoint lets operators override the destination. Default is
	// api.push.apple.com (production). For development, set
	// api.sandbox.push.apple.com.
	Endpoint string `yaml:"endpoint,omitempty" env:"HULA_APNS_ENDPOINT"`
}

// Configured reports whether the required fields are set. The APNs
// backend itself treats zero values as "not configured" regardless,
// but this helper powers boot-time logging.
func (c *APNSConfig) Configured() bool {
	if c == nil {
		return false
	}
	return c.TeamID != "" && c.KeyID != "" && c.KeyPEMPath != "" && c.BundleID != ""
}

// FCMConfig carries Firebase Cloud Messaging credentials. The
// ServiceAccountJSON is a ~2 KB Google-issued JSON blob the
// notifier's FCM backend turns into OAuth2 access tokens via
// golang.org/x/oauth2/google.
type FCMConfig struct {
	// ProjectID is the Firebase project identifier — carried in the
	// FCM v1 endpoint URL.
	ProjectID string `yaml:"project_id,omitempty" env:"HULA_FCM_PROJECT_ID"`
	// ServiceAccountJSONPath is the filesystem path to the Google
	// service-account JSON.
	ServiceAccountJSONPath string `yaml:"service_account_json_path,omitempty" env:"HULA_FCM_SA_PATH"`
}

// Configured reports whether the required fields are set.
func (c *FCMConfig) Configured() bool {
	if c == nil {
		return false
	}
	return c.ProjectID != "" && c.ServiceAccountJSONPath != ""
}
