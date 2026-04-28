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
)

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

	// Parse and cache all duration values - panic on invalid
	var err error

	tokenDuration, err = time.ParseDuration(globalTunables.TokenDuration)
	if err != nil {
		panic(fmt.Sprintf("invalid token_duration '%s': %v", globalTunables.TokenDuration, err))
	}

	keyRotationInterval, err = time.ParseDuration(globalTunables.KeyRotationInterval)
	if err != nil {
		panic(fmt.Sprintf("invalid key_rotation_interval '%s': %v", globalTunables.KeyRotationInterval, err))
	}

	jwtKeyInitRetryDelay, err = time.ParseDuration(globalTunables.JWTKeyInitRetryDelay)
	if err != nil {
		panic(fmt.Sprintf("invalid jwt_key_init_retry_delay '%s': %v", globalTunables.JWTKeyInitRetryDelay, err))
	}

	rootHashInitRetryDelay, err = time.ParseDuration(globalTunables.RootHashInitRetryDelay)
	if err != nil {
		panic(fmt.Sprintf("invalid root_hash_init_retry_delay '%s': %v", globalTunables.RootHashInitRetryDelay, err))
	}

	conversionPollInterval, err = time.ParseDuration(globalTunables.ConversionPollInterval)
	if err != nil {
		panic(fmt.Sprintf("invalid conversion_poll_interval '%s': %v", globalTunables.ConversionPollInterval, err))
	}

	conversionStaleTimeout, err = time.ParseDuration(globalTunables.ConversionStaleTimeout)
	if err != nil {
		panic(fmt.Sprintf("invalid conversion_stale_timeout '%s': %v", globalTunables.ConversionStaleTimeout, err))
	}

	raftTransportTimeout, err = time.ParseDuration(globalTunables.RaftTransportTimeout)
	if err != nil {
		panic(fmt.Sprintf("invalid raft_transport_timeout '%s': %v", globalTunables.RaftTransportTimeout, err))
	}

	raftApplyTimeout, err = time.ParseDuration(globalTunables.RaftApplyTimeout)
	if err != nil {
		panic(fmt.Sprintf("invalid raft_apply_timeout '%s': %v", globalTunables.RaftApplyTimeout, err))
	}

	raftBarrierTimeout, err = time.ParseDuration(globalTunables.RaftBarrierTimeout)
	if err != nil {
		panic(fmt.Sprintf("invalid raft_barrier_timeout '%s': %v", globalTunables.RaftBarrierTimeout, err))
	}

	serverStartupDelay, err = time.ParseDuration(globalTunables.ServerStartupDelay)
	if err != nil {
		panic(fmt.Sprintf("invalid server_startup_delay '%s': %v", globalTunables.ServerStartupDelay, err))
	}

	gracefulShutdownTimeout, err = time.ParseDuration(globalTunables.GracefulShutdownTimeout)
	if err != nil {
		panic(fmt.Sprintf("invalid graceful_shutdown_timeout '%s': %v", globalTunables.GracefulShutdownTimeout, err))
	}

	grpcKeepAliveInterval, err = time.ParseDuration(globalTunables.GRPCKeepAliveInterval)
	if err != nil {
		panic(fmt.Sprintf("invalid grpc_keepalive_interval '%s': %v", globalTunables.GRPCKeepAliveInterval, err))
	}

	httpRequestTimeout, err = time.ParseDuration(globalTunables.HTTPRequestTimeout)
	if err != nil {
		panic(fmt.Sprintf("invalid http_request_timeout '%s': %v", globalTunables.HTTPRequestTimeout, err))
	}

	tlsHandshakeTimeout, err = time.ParseDuration(globalTunables.TLSHandshakeTimeout)
	if err != nil {
		panic(fmt.Sprintf("invalid tls_handshake_timeout '%s': %v", globalTunables.TLSHandshakeTimeout, err))
	}

	inMemoryCacheExpiration, err = time.ParseDuration(globalTunables.InMemoryCacheExpiration)
	if err != nil {
		panic(fmt.Sprintf("invalid inmemory_cache_expiration '%s': %v", globalTunables.InMemoryCacheExpiration, err))
	}

	backingSyncPollInterval, err = time.ParseDuration(globalTunables.BackingSyncPollInterval)
	if err != nil {
		panic(fmt.Sprintf("invalid backing_sync_poll_interval '%s': %v", globalTunables.BackingSyncPollInterval, err))
	}

	backingSyncStaleTimeout, err = time.ParseDuration(globalTunables.BackingSyncStaleTimeout)
	if err != nil {
		panic(fmt.Sprintf("invalid backing_sync_stale_timeout '%s': %v", globalTunables.BackingSyncStaleTimeout, err))
	}

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
