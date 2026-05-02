package tune

import (
	"fmt"
	"time"

	"go.izuma.io/conftagz"
	"github.com/tlalocweb/hulation/log"
	"gopkg.in/yaml.v3"
)

var logtune = log.GetTaggedLogger("tune", "Tunables configuration")

// Tunables holds all tunable parameters for izcrd
type Tunables struct {
	// Storage key prefix - all other keys derive from this
	DBPrefix string `yaml:"db_prefix" default:"hula" env:"HULA_DB_PREFIX" usage:"Database key prefix for all storage keys"`

	// JWT/Auth configuration
	TokenDuration          string `yaml:"token_duration" default:"24h" env:"HULA_TOKEN_DURATION" usage:"JWT token validity duration"`
	KeyRotationInterval    string `yaml:"key_rotation_interval" default:"8760h" env:"HULA_KEY_ROTATION_INTERVAL" usage:"JWT signing key rotation interval (default 1 year)"`
	NumberOfTokenKeys      int    `yaml:"number_of_token_keys" default:"10" env:"HULA_NUMBER_OF_TOKEN_KEYS" usage:"Number of JWT signing keys to maintain"`
	JWTKeyInitMaxRetries   int    `yaml:"jwt_key_init_max_retries" default:"10" usage:"Max retries for JWT key initialization"`
	JWTKeyInitRetryDelay   string `yaml:"jwt_key_init_retry_delay" default:"500ms" usage:"Delay between JWT key init retries"`
	RootHashInitMaxRetries int    `yaml:"root_hash_init_max_retries" default:"10" usage:"Max retries for root hash initialization"`
	RootHashInitRetryDelay string `yaml:"root_hash_init_retry_delay" default:"500ms" usage:"Delay between root hash init retries"`

	// Registry conversion settings
	ConversionWorkers      int    `yaml:"conversion_workers" default:"4" env:"HULA_CONVERSION_WORKERS" usage:"Number of conversion worker goroutines"`
	ConversionMaxRetries   int    `yaml:"conversion_max_retries" default:"3" env:"HULA_CONVERSION_MAX_RETRIES" usage:"Max retries for failed conversions"`
	ConversionPollInterval string `yaml:"conversion_poll_interval" default:"5s" env:"HULA_CONVERSION_POLL_INTERVAL" usage:"Polling interval for conversion queue"`
	ConversionTempDir      string `yaml:"conversion_temp_dir" default:"/tmp/izcr-conversion" env:"HULA_CONVERSION_TEMP_DIR" usage:"Temporary directory for conversion operations"`
	ConversionStaleTimeout string `yaml:"conversion_stale_timeout" default:"10m" env:"HULA_CONVERSION_STALE_TIMEOUT" usage:"Time after which a stuck conversion job is considered stale"`

	// Raft consensus timeouts
	RaftTransportTimeout string `yaml:"raft_transport_timeout" default:"10s" env:"HULA_RAFT_TRANSPORT_TIMEOUT" usage:"Raft transport connection timeout"`
	RaftApplyTimeout     string `yaml:"raft_apply_timeout" default:"10s" env:"HULA_RAFT_APPLY_TIMEOUT" usage:"Raft log apply timeout"`
	RaftBarrierTimeout   string `yaml:"raft_barrier_timeout" default:"5s" env:"HULA_RAFT_BARRIER_TIMEOUT" usage:"Raft barrier timeout"`
	RaftTransportMaxPool int    `yaml:"raft_transport_max_pool" default:"3" env:"HULA_RAFT_TRANSPORT_MAX_POOL" usage:"Raft transport max connection pool size"`

	// Server lifecycle
	ServerStartupDelay      string `yaml:"server_startup_delay" default:"100ms" usage:"Delay before server starts accepting connections"`
	GracefulShutdownTimeout string `yaml:"graceful_shutdown_timeout" default:"5s" usage:"Timeout for graceful server shutdown"`

	// Network/gRPC
	GRPCKeepAliveInterval string `yaml:"grpc_keepalive_interval" default:"30s" env:"HULA_GRPC_KEEPALIVE_INTERVAL" usage:"gRPC keep-alive ping interval"`
	HTTPRequestTimeout    string `yaml:"http_request_timeout" default:"10s" env:"HULA_HTTP_REQUEST_TIMEOUT" usage:"Default HTTP request timeout"`
	TLSHandshakeTimeout   string `yaml:"tls_handshake_timeout" default:"10s" env:"HULA_TLS_HANDSHAKE_TIMEOUT" usage:"TLS handshake timeout"`

	// Cache
	InMemoryCacheExpiration string `yaml:"inmemory_cache_expiration" default:"5m" env:"HULA_CACHE_EXPIRATION" usage:"Default in-memory cache expiration"`

	// Certificates
	EdgeNodeCertValidityDays int `yaml:"edgenode_cert_validity_days" default:"365" env:"HULA_EDGENODE_CERT_VALIDITY_DAYS" usage:"Edge node certificate validity in days"`

	// Backing sync settings (GHCR, etc.)
	// Note: bool fields don't support conftagz default tags - use Go default (false)
	BackingSyncDisable      bool   `yaml:"backing_sync_disable" env:"HULA_BACKING_SYNC_DISABLE" usage:"Disable backing sync subsystem (enabled by default)"`
	BackingSyncWorkers      int    `yaml:"backing_sync_workers" default:"4" env:"HULA_BACKING_SYNC_WORKERS" usage:"Number of backing sync worker goroutines"`
	BackingSyncPollInterval string `yaml:"backing_sync_poll_interval" default:"5m" env:"HULA_BACKING_SYNC_POLL_INTERVAL" usage:"Default polling interval for backing entries"`
	BackingSyncMaxRetries   int    `yaml:"backing_sync_max_retries" default:"3" env:"HULA_BACKING_SYNC_MAX_RETRIES" usage:"Max retries for failed sync jobs"`
	BackingSyncStaleTimeout string `yaml:"backing_sync_stale_timeout" default:"30m" env:"HULA_BACKING_SYNC_STALE_TIMEOUT" usage:"Timeout after which a stuck sync job is considered stale"`
	GHCRRateLimitPerSec     int    `yaml:"ghcr_rate_limit_per_sec" default:"10" env:"HULA_GHCR_RATE_LIMIT_PER_SEC" usage:"GHCR API rate limit (requests per second)"`

	// PROXY protocol settings
	// Note: ProxyProtocolEnabled defaults to true (set in SetupTunables before YAML decode)
	ProxyProtocolEnabled bool `yaml:"proxy_protocol_enabled" env:"HULA_PROXY_PROTOCOL_ENABLED" usage:"Enable PROXY protocol auto-detection for load balancer compatibility (default: true)"`

	// Permission cache settings
	PermissionCacheMaxEntries int `yaml:"permission_cache_max_entries" default:"1000" env:"HULA_PERMISSION_CACHE_MAX_ENTRIES" usage:"Maximum number of user permission sets to cache in memory"`

	// IP-info (geo/ASN) cache settings — backs the visitor-analytics
	// country/region/city enrichment and the badactor ipinfo lookups.
	// Cache is in-process LRU sized by an approximate byte budget;
	// entries past the budget are evicted oldest-first. TTL is a soft
	// upper bound — entries older than this are refreshed on next hit.
	IPInfoCacheMaxMB         int    `yaml:"ipinfo_cache_max_mb" default:"16" env:"HULA_IPINFO_CACHE_MAX_MB" usage:"Approximate memory budget for the in-process IP-info cache (MB). 0 disables in-process caching (DB-only)."`
	IPInfoCacheTTL           string `yaml:"ipinfo_cache_ttl" default:"168h" env:"HULA_IPINFO_CACHE_TTL" usage:"How long an IP-info entry stays valid before re-lookup (default 7 days)"`
	IPInfoRateLimitPerMinute int    `yaml:"ipinfo_rate_limit_per_minute" default:"40" env:"HULA_IPINFO_RATE_LIMIT_PER_MINUTE" usage:"Outbound rate limit for the ip-api.com lookup. Free tier is 45/min; we default to 40 to leave headroom."`
	IPInfoUseHTTPS           bool   `yaml:"ipinfo_use_https" env:"HULA_IPINFO_USE_HTTPS" usage:"Use HTTPS for ip-api.com lookups (requires Pro plan)"`
}

var globalTunables Tunables

// Cached duration values (parsed once at startup)
var (
	tokenDuration           time.Duration
	keyRotationInterval     time.Duration
	jwtKeyInitRetryDelay    time.Duration
	rootHashInitRetryDelay  time.Duration
	conversionPollInterval  time.Duration
	conversionStaleTimeout  time.Duration
	raftTransportTimeout    time.Duration
	raftApplyTimeout        time.Duration
	raftBarrierTimeout      time.Duration
	serverStartupDelay      time.Duration
	gracefulShutdownTimeout time.Duration
	grpcKeepAliveInterval   time.Duration
	httpRequestTimeout      time.Duration
	tlsHandshakeTimeout     time.Duration
	inMemoryCacheExpiration time.Duration
	backingSyncPollInterval time.Duration
	backingSyncStaleTimeout time.Duration
	ipInfoCacheTTL          time.Duration
)

// init applies default values from struct tags so tune.GetX() returns
// sane defaults even when SetupTunables is never called (e.g. tests,
// CLI tools, or boot paths that haven't been migrated yet). YAML
// overrides via SetupTunables, and env-var overrides via conftagz, are
// still honored when those entry points run.
//
// We deliberately exclude FLAGTAGS from OrderOfOps because conftagz's
// flag-tag handler triggers flag.Parse() — and a stray flag.Parse() at
// package-init time eats the runtime args before the testing package
// gets a chance to register its own -test.* flags, which breaks
// `go test` for any binary that transitively imports this package.
func init() {
	globalTunables.ProxyProtocolEnabled = true
	opts := &conftagz.ConfTagOpts{
		OrderOfOps: []int{conftagz.DEFAULTTAGS, conftagz.ENVTAGS, conftagz.TESTTAGS},
	}
	if err := conftagz.Process(opts, &globalTunables); err != nil {
		// struct-tag failures are programmer errors — fail fast.
		panic(fmt.Sprintf("tune: applying defaults: %v", err))
	}
	parseDurationTunables()
}

// parseDurationTunables resolves all *string duration fields into
// their parsed time.Duration counterparts. Called from init() (with
// defaults) and again from SetupTunables (with operator overrides).
func parseDurationTunables() {
	parse := func(field, raw string) time.Duration {
		d, err := time.ParseDuration(raw)
		if err != nil {
			panic(fmt.Sprintf("invalid %s '%s': %v", field, raw, err))
		}
		return d
	}
	tokenDuration = parse("token_duration", globalTunables.TokenDuration)
	keyRotationInterval = parse("key_rotation_interval", globalTunables.KeyRotationInterval)
	jwtKeyInitRetryDelay = parse("jwt_key_init_retry_delay", globalTunables.JWTKeyInitRetryDelay)
	rootHashInitRetryDelay = parse("root_hash_init_retry_delay", globalTunables.RootHashInitRetryDelay)
	conversionPollInterval = parse("conversion_poll_interval", globalTunables.ConversionPollInterval)
	conversionStaleTimeout = parse("conversion_stale_timeout", globalTunables.ConversionStaleTimeout)
	raftTransportTimeout = parse("raft_transport_timeout", globalTunables.RaftTransportTimeout)
	raftApplyTimeout = parse("raft_apply_timeout", globalTunables.RaftApplyTimeout)
	raftBarrierTimeout = parse("raft_barrier_timeout", globalTunables.RaftBarrierTimeout)
	serverStartupDelay = parse("server_startup_delay", globalTunables.ServerStartupDelay)
	gracefulShutdownTimeout = parse("graceful_shutdown_timeout", globalTunables.GracefulShutdownTimeout)
	grpcKeepAliveInterval = parse("grpc_keepalive_interval", globalTunables.GRPCKeepAliveInterval)
	httpRequestTimeout = parse("http_request_timeout", globalTunables.HTTPRequestTimeout)
	tlsHandshakeTimeout = parse("tls_handshake_timeout", globalTunables.TLSHandshakeTimeout)
	inMemoryCacheExpiration = parse("inmemory_cache_expiration", globalTunables.InMemoryCacheExpiration)
	backingSyncPollInterval = parse("backing_sync_poll_interval", globalTunables.BackingSyncPollInterval)
	backingSyncStaleTimeout = parse("backing_sync_stale_timeout", globalTunables.BackingSyncStaleTimeout)
	ipInfoCacheTTL = parse("ipinfo_cache_ttl", globalTunables.IPInfoCacheTTL)
}

// SetupTunables initializes tunables from YAML config node
// Panics on invalid duration strings (fail fast at startup)
func SetupTunables(node yaml.Node) error {
	// Set defaults for bool fields (conftagz doesn't support default tag for bools)
	globalTunables.ProxyProtocolEnabled = true // Default enabled - auto-detect mode works with or without PROXY header

	// Decode YAML node into struct (will override defaults if specified)
	if err := node.Decode(&globalTunables); err != nil {
		return fmt.Errorf("error decoding tunables: %w", err)
	}

	// Process conftagz (apply defaults, env vars)
	if err := conftagz.Process(nil, &globalTunables); err != nil {
		return fmt.Errorf("error processing tunables: %w", err)
	}

	logtune.Debugf("Tunables loaded: %+v", globalTunables)

	parseDurationTunables()
	return nil
}

// DumpTunables returns all tunables as YAML string (for debugging)
func DumpTunables() string {
	buf, err := yaml.Marshal(&globalTunables)
	if err != nil {
		return fmt.Sprintf("error marshalling tunables: %v", err)
	}
	return string(buf)
}

// ==================== Storage Key Getters ====================

// GetDBPrefix returns the base database key prefix
func GetDBPrefix() string { return globalTunables.DBPrefix }

// GetStorageKey returns a full storage key: "<prefix>:<suffix>:"
func GetStorageKey(suffix string) string {
	return globalTunables.DBPrefix + ":" + suffix + ":"
}

// Pre-built storage key getters for common keys
func GetUsersStorageKey() string            { return GetStorageKey("users") }
func GetProjectsStorageKey() string         { return GetStorageKey("registryprojects") }
func GetRegUsersStorageKey() string         { return GetStorageKey("regusers") }
func GetEdgeNodesStorageKey() string        { return GetStorageKey("edgenodes") }
func GetFleetsStorageKey() string           { return GetStorageKey("fleets") }
func GetJWTKeysStorageKey() string          { return GetStorageKey("jwt:keys") }
func GetJWTTokensStorageKey() string        { return GetStorageKey("jwt:tokens") }
func GetRootHashStorageKey() string         { return globalTunables.DBPrefix + ":root:hash" }
func GetConversionStorageKey() string       { return GetStorageKey("conversion") }
func GetBackingEntriesStorageKey() string   { return GetStorageKey("backingentries") }
func GetBackingSyncJobsStorageKey() string  { return GetStorageKey("backingsyncjobs") }
func GetBackingProvidersStorageKey() string { return GetStorageKey("backingproviders") }
func GetImageBackingsStorageKey() string    { return GetStorageKey("imagebackings") }
func GetTenantsStorageKey() string          { return GetStorageKey("tenants") }

// ==================== JWT/Auth Getters ====================

func GetTokenDuration() time.Duration          { return tokenDuration }
func GetKeyRotationInterval() time.Duration    { return keyRotationInterval }
func GetNumberOfTokenKeys() int                { return globalTunables.NumberOfTokenKeys }
func GetJWTKeyInitMaxRetries() int             { return globalTunables.JWTKeyInitMaxRetries }
func GetJWTKeyInitRetryDelay() time.Duration   { return jwtKeyInitRetryDelay }
func GetRootHashInitMaxRetries() int           { return globalTunables.RootHashInitMaxRetries }
func GetRootHashInitRetryDelay() time.Duration { return rootHashInitRetryDelay }

// ==================== Conversion Getters ====================

func GetConversionWorkers() int                { return globalTunables.ConversionWorkers }
func GetConversionMaxRetries() int             { return globalTunables.ConversionMaxRetries }
func GetConversionPollInterval() time.Duration { return conversionPollInterval }
func GetConversionTempDir() string             { return globalTunables.ConversionTempDir }
func GetConversionStaleTimeout() time.Duration { return conversionStaleTimeout }

// GetConversionPollIntervalSeconds returns poll interval in seconds (for backward compatibility)
func GetConversionPollIntervalSeconds() int {
	return int(conversionPollInterval.Seconds())
}

// ==================== Raft Getters ====================

func GetRaftTransportTimeout() time.Duration { return raftTransportTimeout }
func GetRaftApplyTimeout() time.Duration     { return raftApplyTimeout }
func GetRaftBarrierTimeout() time.Duration   { return raftBarrierTimeout }
func GetRaftTransportMaxPool() int           { return globalTunables.RaftTransportMaxPool }

// ==================== Server Lifecycle Getters ====================

func GetServerStartupDelay() time.Duration      { return serverStartupDelay }
func GetGracefulShutdownTimeout() time.Duration { return gracefulShutdownTimeout }

// ==================== Network Getters ====================

func GetGRPCKeepAliveInterval() time.Duration { return grpcKeepAliveInterval }
func GetHTTPRequestTimeout() time.Duration    { return httpRequestTimeout }
func GetTLSHandshakeTimeout() time.Duration   { return tlsHandshakeTimeout }

// ==================== Cache Getters ====================

func GetInMemoryCacheExpiration() time.Duration { return inMemoryCacheExpiration }

// ==================== Certificate Getters ====================

func GetEdgeNodeCertValidityDays() int { return globalTunables.EdgeNodeCertValidityDays }
func GetEdgeNodeCertValidity() time.Duration {
	return time.Duration(globalTunables.EdgeNodeCertValidityDays) * 24 * time.Hour
}

// ==================== Backing Sync Getters ====================

func GetBackingSyncDisable() bool               { return globalTunables.BackingSyncDisable }
func GetBackingSyncEnabled() bool               { return !globalTunables.BackingSyncDisable }
func GetBackingSyncWorkers() int                { return globalTunables.BackingSyncWorkers }
func GetBackingSyncPollInterval() time.Duration { return backingSyncPollInterval }
func GetBackingSyncMaxRetries() int             { return globalTunables.BackingSyncMaxRetries }
func GetBackingSyncStaleTimeout() time.Duration { return backingSyncStaleTimeout }
func GetGHCRRateLimitPerSec() int               { return globalTunables.GHCRRateLimitPerSec }

// ==================== PROXY Protocol Getters ====================

func GetProxyProtocolEnabled() bool { return globalTunables.ProxyProtocolEnabled }

// ==================== Permission Cache Getters ====================

func GetPermissionCacheMaxEntries() int     { return globalTunables.PermissionCacheMaxEntries }
func GetPermissionCacheStoragePrefix() string { return GetStorageKey("permcache") }

// ==================== IP-info (geo/ASN) Cache Getters ====================

// GetIPInfoCacheMaxBytes returns the in-process IP-info cache budget
// in bytes. 0 means in-process caching is disabled (DB-only).
func GetIPInfoCacheMaxBytes() int64 {
	if globalTunables.IPInfoCacheMaxMB <= 0 {
		return 0
	}
	return int64(globalTunables.IPInfoCacheMaxMB) * 1024 * 1024
}

func GetIPInfoCacheTTL() time.Duration  { return ipInfoCacheTTL }
func GetIPInfoRateLimit() int           { return globalTunables.IPInfoRateLimitPerMinute }
func GetIPInfoUseHTTPS() bool           { return globalTunables.IPInfoUseHTTPS }
