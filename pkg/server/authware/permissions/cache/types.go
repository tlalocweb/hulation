package cache

import (
	"github.com/tlalocweb/hulation/pkg/utils"
)

// PermissionSet represents a user's resolved permissions stored in HashTrees
type PermissionSet struct {
	UserUUID         string          // User's UUID
	AllowTree        *utils.HashTree // Allow permissions in HashTree
	DenyTree         *utils.HashTree // Deny permissions in HashTree
	AllowChecksum    uint64          // Checksum of allow permissions
	DenyChecksum     uint64          // Checksum of deny permissions
	CombinedChecksum uint64          // XOR of allow and deny checksums (stored in JWT)
	Version          int64           // Incremented on permission changes
	CreatedAt        int64           // Unix timestamp when created
	SerializedSize   int             // Approximate memory size for cache management
}

// StoredPermissionSet is the storage representation (serialized to raft)
type StoredPermissionSet struct {
	UserUUID         string // User's UUID
	AllowData        []byte // Serialized HashTree for allow permissions
	DenyData         []byte // Serialized HashTree for deny permissions
	CombinedChecksum uint64 // XOR of allow and deny checksums
	Version          int64  // Version for optimistic locking
	CreatedAt        int64  // Unix timestamp
}

// CacheInvalidationSignal is written to raft to trigger cross-node cache invalidation
type CacheInvalidationSignal struct {
	UserUUID  string // Empty string means invalidate all users
	Timestamp int64  // Unix nano timestamp
	Reason    string // Reason for invalidation: "permission_change", "user_delete", "tenant_delete", etc.
}

// PermissionCacheStore manages permission caching with in-memory FIFO cache and raft storage
type PermissionCacheStore interface {
	// Get retrieves a permission set for a user with the given checksum.
	// Returns nil if not found in cache or storage, or if checksum doesn't match.
	// First checks in-memory cache, then falls back to raft storage.
	Get(userUUID string, checksum uint64) (*PermissionSet, error)

	// Store stores a permission set and returns its combined checksum.
	// This will:
	// 1. Delete any existing permission entries for the user
	// 2. Store the new entry with the new checksum
	// 3. Trigger cross-node invalidation
	Store(userUUID string, allowPerms, denyPerms []string) (checksum uint64, err error)

	// Invalidate removes a user's permissions from the local in-memory cache.
	// Does not affect raft storage.
	Invalidate(userUUID string) error

	// InvalidateAll clears the entire local in-memory cache.
	// Does not affect raft storage.
	InvalidateAll() error

	// TriggerCrossNodeInvalidation writes a signal to raft that causes all nodes
	// to invalidate their in-memory caches for the specified user (or all if userUUID is empty).
	TriggerCrossNodeInvalidation(userUUID string, reason string) error

	// GetAllPermissions extracts all permission strings from a PermissionSet.
	// Returns (allowPerms, denyPerms).
	// Deprecated: Use CheckAccess or HasRequiredPermissions for efficient tree-based matching.
	GetAllPermissions(ps *PermissionSet) ([]string, []string)

	// CheckAccess checks if the permission set allows the required permission.
	// Uses efficient radix tree matching with MatchWithWildcards - O(k) per check.
	CheckAccess(ps *PermissionSet, required string) bool

	// HasRequiredPermissions checks if permission set satisfies API requirements.
	// Uses efficient radix tree matching instead of string/regex comparisons.
	// requireAll=true means ALL permissions must be satisfied (AND logic).
	// requireAll=false means ANY permission is sufficient (OR logic).
	HasRequiredPermissions(ps *PermissionSet, required []string, requireAll bool) bool

	// CheckAccessWithMatch is like CheckAccess but also returns which permission matched.
	// Useful for audit/diagnostic APIs that need to report which permission granted/denied access.
	CheckAccessWithMatch(ps *PermissionSet, required string) (allowed bool, matchedAllow, matchedDeny string)
}

// Config holds configuration options for PermissionCacheStore
type Config struct {
	// StorageKeyPrefix is the prefix for permission set keys in raft storage.
	// Default: "v1/permission-cache/"
	StorageKeyPrefix string

	// InvalidationKeyPrefix is the prefix for invalidation signal keys in raft storage.
	// Default: "v1/permission-cache-signals/"
	InvalidationKeyPrefix string

	// MaxCacheEntries is the maximum number of users that can be cached in memory.
	// Default: 1000
	MaxCacheEntries int

	// MaxCacheMemoryBytes is the optional memory limit for the cache.
	// Default: 50 * 1024 * 1024 (50 MB)
	MaxCacheMemoryBytes int64

	// DebugLevel controls logging verbosity.
	// Default: 0
	DebugLevel int
}

// DefaultConfig returns a Config with default values
func DefaultConfig() *Config {
	return &Config{
		StorageKeyPrefix:      "v1/permission-cache/",
		InvalidationKeyPrefix: "v1/permission-cache-signals/",
		MaxCacheEntries:       1000,
		MaxCacheMemoryBytes:   50 * 1024 * 1024, // 50 MB
		DebugLevel:            0,
	}
}

// Option is a function that modifies Config
type Option func(*Config)

// WithStorageKeyPrefix sets the storage key prefix
func WithStorageKeyPrefix(prefix string) Option {
	return func(c *Config) {
		c.StorageKeyPrefix = prefix
	}
}

// WithInvalidationKeyPrefix sets the invalidation signal key prefix
func WithInvalidationKeyPrefix(prefix string) Option {
	return func(c *Config) {
		c.InvalidationKeyPrefix = prefix
	}
}

// WithMaxCacheEntries sets the maximum number of cached entries
func WithMaxCacheEntries(max int) Option {
	return func(c *Config) {
		c.MaxCacheEntries = max
	}
}

// WithMaxCacheMemoryBytes sets the maximum memory usage for the cache
func WithMaxCacheMemoryBytes(maxBytes int64) Option {
	return func(c *Config) {
		c.MaxCacheMemoryBytes = maxBytes
	}
}

// WithDebugLevel sets the debug level for logging
func WithDebugLevel(level int) Option {
	return func(c *Config) {
		c.DebugLevel = level
	}
}

// PermissionsStaleError is returned when a permission checksum doesn't match
// the current stored permissions (i.e., permissions have changed since the token was issued)
type PermissionsStaleError struct {
	UserUUID    string
	OldChecksum uint64
	Message     string
}

func (e *PermissionsStaleError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "PERMISSIONS_STALE"
}

// IsPermissionsStaleError checks if an error is a PermissionsStaleError
func IsPermissionsStaleError(err error) bool {
	_, ok := err.(*PermissionsStaleError)
	return ok
}
