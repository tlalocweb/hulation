package config

// OPAQUEConfig carries the operator-pinned key material for the
// OPAQUE PAKE subsystem (pkg/auth/opaque).
//
// Both fields are base64-encoded (raw or padded, url-safe or std
// alphabet — pkg/auth/opaque.tryDecode is permissive). When unset,
// hula generates fresh values at boot and logs them ONCE with a
// loud WRN so the operator can paste them into config or env.
//
// IMPORTANT: changing either of these invalidates every existing
// OPAQUE record. Treat them as part of the server's identity.
type OPAQUEConfig struct {
	// OPRFSeed is 64 bytes (Ristretto255-SHA512 default). Env-bound
	// to HULA_OPAQUE_OPRF_SEED, which wins over yaml.
	OPRFSeed string `yaml:"oprf_seed,omitempty" env:"HULA_OPAQUE_OPRF_SEED"`
	// AKESecret is the long-lived AKE private-scalar (encoded
	// scalar bytes for the OPRF group). Env: HULA_OPAQUE_AKE_SECRET.
	AKESecret string `yaml:"ake_secret,omitempty" env:"HULA_OPAQUE_AKE_SECRET"`
}
