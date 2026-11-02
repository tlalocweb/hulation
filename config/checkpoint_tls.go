package config

// ProxyUpstreamTLS presents a client certificate to a verified HTTPS backend.
// CheckpointBinding additionally forwards the frontend TLS binding; enable it
// only on the dedicated checkpoint route and trust a dedicated proxy CA there.
type ProxyUpstreamTLS struct {
	ClientCert        string `yaml:"client_cert"`
	ClientKey         string `yaml:"client_key"`
	CheckpointBinding bool   `yaml:"checkpoint_binding,omitempty"`
}
