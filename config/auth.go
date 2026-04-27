package config

// AuthConfig holds the list of authentication providers configured for
// this hulation instance. Each provider is consumed by pkg/server/
// authware/provider during startup.
//
// Example config.yaml:
//
//   auth:
//     providers:
//       - name: internal
//         provider: internal
//       - name: google
//         provider: oidc
//         config:
//           display_name: Google
//           discovery_url: https://accounts.google.com/.well-known/openid-configuration
//           client_id: ${HULA_GOOGLE_CLIENT_ID}
//           client_secret: ${HULA_GOOGLE_CLIENT_SECRET}
//           redirect_url: https://${HULA_HOST}/api/v1/auth/callback/google
//           scopes: [openid, email, profile]
//           icon_url: /analytics/icons/google.svg
//       - name: github
//         provider: oidc
//         config:
//           display_name: GitHub
//           ...
//       - name: microsoft
//         provider: oidc
//         config:
//           display_name: Microsoft
//           ...
//
// The `config` field on each provider is decoded by the provider
// implementation itself — each provider type defines its own config
// schema.
type AuthConfig struct {
	// Providers is the ordered list of configured auth providers. The
	// local "internal" provider (username + password + TOTP) is
	// automatically registered by hulation if not present here, so the
	// admin break-glass account always works.
	Providers []*AuthProviderConfig `yaml:"providers,omitempty"`
}

// AuthProviderConfig is the outer shape of a single provider entry.
// The `Config` field is parsed by the provider implementation.
//
// Mirrors pkg/server/authware/provider/base.AuthProviderConfig so the
// two can be marshalled from the same YAML.
type AuthProviderConfig struct {
	// Name uniquely identifies this provider instance (used by hulactl
	// and the API). Must be unique within the providers list.
	Name string `yaml:"name"`
	// Provider is the provider type: "internal" or "oidc".
	Provider string `yaml:"provider"`
	// Config is the provider-specific configuration blob. Keys vary by
	// provider type.
	Config map[string]any `yaml:"config,omitempty"`
}
