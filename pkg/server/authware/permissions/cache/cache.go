package cache

import (
	"bytes"
	"container/list"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/store/common"
	"github.com/tlalocweb/hulation/pkg/utils"
)

var (
	instance *permissionCacheStore
	once     sync.Once
	logger   *log.TaggedLogger
)

func init() {
	logger = log.GetTaggedLogger("permcache", "Permission cache store")
}

// permissionCacheStore implements PermissionCacheStore
type permissionCacheStore struct {
	mu                    sync.RWMutex
	cache                 map[string]*cacheEntry // userUUID -> entry
	fifoList              *list.List             // FIFO order for eviction (front = newest, back = oldest)
	currentMemoryBytes    int64                  // Current memory usage estimate
	config                *Config
	storage               common.Storage
	informer              common.Informer
	stopCh                chan struct{}
	watcherStarted        bool
	informerFactory       common.InformerFactory
}

// cacheEntry holds a cached permission set and its position in the FIFO list
type cacheEntry struct {
	permSet *PermissionSet
	element *list.Element // Position in FIFO list
}

// Init initializes the singleton PermissionCacheStore.
// Must be called before GetStore().
// The storage parameter is required for raft persistence.
// The informerFactory is optional - if provided, enables cross-node cache invalidation.
func Init(storage common.Storage, informerFactory common.InformerFactory, opts ...Option) error {
	var initErr error
	once.Do(func() {
		config := DefaultConfig()
		for _, opt := range opts {
			opt(config)
		}

		instance = &permissionCacheStore{
			cache:           make(map[string]*cacheEntry),
			fifoList:        list.New(),
			config:          config,
			storage:         storage,
			stopCh:          make(chan struct{}),
			informerFactory: informerFactory,
		}

		// Start watching for invalidation signals if informer factory is available
		if informerFactory != nil {
			instance.informer = informerFactory.MakeInformer(config.InvalidationKeyPrefix)
			go instance.watchInvalidationSignals()
			instance.watcherStarted = true
		}

		logger.Infof("Permission cache store initialized with max %d entries, %d MB memory limit",
			config.MaxCacheEntries, config.MaxCacheMemoryBytes/(1024*1024))
	})
	return initErr
}

// GetStore returns the singleton PermissionCacheStore.
// Panics if Init() has not been called.
func GetStore() PermissionCacheStore {
	if instance == nil {
		panic("PermissionCacheStore not initialized - call Init() first")
	}
	return instance
}

// IsInitialized returns true if the cache store has been initialized
func IsInitialized() bool {
	return instance != nil
}

// Get retrieves a permission set for a user with the given checksum.
func (s *permissionCacheStore) Get(userUUID string, checksum uint64) (*PermissionSet, error) {
	// 1. Check in-memory cache first (fast path)
	s.mu.RLock()
	if entry, ok := s.cache[userUUID]; ok {
		if entry.permSet.CombinedChecksum == checksum {
			s.mu.RUnlock()
			if s.config.DebugLevel > 0 {
				logger.Debugf("Cache hit for user %s checksum %016x", userUUID, checksum)
			}
			return entry.permSet, nil
		}
		// Checksum mismatch - entry exists but with different permissions
		if s.config.DebugLevel > 0 {
			logger.Debugf("Cache checksum mismatch for user %s: cached=%016x, requested=%016x",
				userUUID, entry.permSet.CombinedChecksum, checksum)
		}
	}
	s.mu.RUnlock()

	// 2. Cache miss - load from raft storage
	storageKey := s.buildStorageKey(userUUID, checksum)
	data, err := s.storage.Get(storageKey)
	if err != nil {
		return nil, fmt.Errorf("failed to load permission set from storage: %w", err)
	}

	// nil data means key not found
	if data == nil || len(data) == 0 {
		if s.config.DebugLevel > 0 {
			logger.Debugf("Permission set not found in storage for user %s checksum %016x", userUUID, checksum)
		}
		return nil, nil // Not found
	}

	// 3. Deserialize
	stored := &StoredPermissionSet{}
	buf := bytes.NewReader(data)
	dec := gob.NewDecoder(buf)
	if err := dec.Decode(stored); err != nil {
		return nil, fmt.Errorf("failed to deserialize permission set: %w", err)
	}

	// 4. Validate checksum
	if stored.CombinedChecksum != checksum {
		if s.config.DebugLevel > 0 {
			logger.Debugf("Storage checksum mismatch for user %s: stored=%016x, requested=%016x",
				userUUID, stored.CombinedChecksum, checksum)
		}
		return nil, nil // Checksum mismatch
	}

	// 5. Reconstruct PermissionSet
	permSet := &PermissionSet{
		UserUUID:         stored.UserUUID,
		AllowTree:        utils.NewHashTree(),
		DenyTree:         utils.NewHashTree(),
		CombinedChecksum: stored.CombinedChecksum,
		Version:          stored.Version,
		CreatedAt:        stored.CreatedAt,
		SerializedSize:   len(data),
	}

	if err := permSet.AllowTree.Deserialize(stored.AllowData); err != nil {
		return nil, fmt.Errorf("failed to deserialize allow tree: %w", err)
	}
	if err := permSet.DenyTree.Deserialize(stored.DenyData); err != nil {
		return nil, fmt.Errorf("failed to deserialize deny tree: %w", err)
	}

	// Recompute checksums from trees
	permSet.AllowChecksum = permSet.AllowTree.Checksum()
	permSet.DenyChecksum = permSet.DenyTree.Checksum()

	// 6. Add to in-memory cache
	s.addToCache(userUUID, permSet)

	if s.config.DebugLevel > 0 {
		logger.Debugf("Loaded permission set from storage for user %s checksum %016x", userUUID, checksum)
	}

	return permSet, nil
}

// Store stores a permission set and returns its combined checksum.
func (s *permissionCacheStore) Store(userUUID string, allowPerms, denyPerms []string) (uint64, error) {
	// 1. Build HashTrees
	allowTree := utils.NewHashTree()
	for _, p := range allowPerms {
		allowTree.InsertString(p)
	}

	denyTree := utils.NewHashTree()
	for _, p := range denyPerms {
		denyTree.InsertString(p)
	}

	// 2. Compute combined checksum (XOR of allow and deny checksums)
	allowChecksum := allowTree.Checksum()
	denyChecksum := denyTree.Checksum()
	combinedChecksum := allowChecksum ^ denyChecksum

	// 3. Serialize trees
	allowData := allowTree.Serialize()
	denyData := denyTree.Serialize()

	// 4. Create storage entry
	stored := &StoredPermissionSet{
		UserUUID:         userUUID,
		AllowData:        allowData,
		DenyData:         denyData,
		CombinedChecksum: combinedChecksum,
		Version:          time.Now().UnixNano(),
		CreatedAt:        time.Now().Unix(),
	}

	// 5. Serialize for storage
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(stored); err != nil {
		return 0, fmt.Errorf("failed to serialize permission set: %w", err)
	}
	data := buf.Bytes()

	// 6. Delete any existing permission entries for this user (all checksums)
	userPrefix := s.config.StorageKeyPrefix + userUUID + "/"
	if err := s.storage.DeleteAll(userPrefix); err != nil {
		// Log but don't fail - the delete might fail if no entries exist
		if s.config.DebugLevel > 0 {
			logger.Debugf("Delete existing entries for user %s: %v", userUUID, err)
		}
	}

	// 7. Store new entry with key: prefix/userUUID/checksum
	storageKey := s.buildStorageKey(userUUID, combinedChecksum)
	if err := s.storage.Put(storageKey, data); err != nil {
		return 0, fmt.Errorf("failed to store permission set: %w", err)
	}

	// 8. Build PermissionSet for cache
	permSet := &PermissionSet{
		UserUUID:         userUUID,
		AllowTree:        allowTree,
		DenyTree:         denyTree,
		AllowChecksum:    allowChecksum,
		DenyChecksum:     denyChecksum,
		CombinedChecksum: combinedChecksum,
		Version:          stored.Version,
		CreatedAt:        stored.CreatedAt,
		SerializedSize:   len(data),
	}

	// 9. Add to in-memory cache (replaces any existing entry for this user)
	s.addToCache(userUUID, permSet)

	// 10. Trigger cross-node invalidation
	if err := s.TriggerCrossNodeInvalidation(userUUID, "permission_change"); err != nil {
		// Log but don't fail - other nodes will eventually get the new data
		logger.Warnf("Failed to trigger cross-node invalidation for user %s: %v", userUUID, err)
	}

	if s.config.DebugLevel > 0 {
		logger.Debugf("Stored permission set for user %s: checksum=%016x, allowPerms=%d, denyPerms=%d",
			userUUID, combinedChecksum, len(allowPerms), len(denyPerms))
	}

	return combinedChecksum, nil
}

// Invalidate removes a user's permissions from the local in-memory cache.
func (s *permissionCacheStore) Invalidate(userUUID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entry, ok := s.cache[userUUID]; ok {
		s.currentMemoryBytes -= int64(entry.permSet.SerializedSize)
		s.fifoList.Remove(entry.element)
		delete(s.cache, userUUID)
		if s.config.DebugLevel > 0 {
			logger.Debugf("Invalidated cache for user %s", userUUID)
		}
	}
	return nil
}

// InvalidateAll clears the entire local in-memory cache.
func (s *permissionCacheStore) InvalidateAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cache = make(map[string]*cacheEntry)
	s.fifoList = list.New()
	s.currentMemoryBytes = 0

	logger.Infof("Invalidated all cache entries")
	return nil
}

// TriggerCrossNodeInvalidation writes a signal to raft that causes all nodes
// to invalidate their in-memory caches.
func (s *permissionCacheStore) TriggerCrossNodeInvalidation(userUUID string, reason string) error {
	signal := &CacheInvalidationSignal{
		UserUUID:  userUUID,
		Timestamp: time.Now().UnixNano(),
		Reason:    reason,
	}

	data, err := json.Marshal(signal)
	if err != nil {
		return fmt.Errorf("failed to marshal invalidation signal: %w", err)
	}

	// Use timestamp as part of key to ensure uniqueness
	signalKey := fmt.Sprintf("%s%d", s.config.InvalidationKeyPrefix, signal.Timestamp)

	if err := s.storage.Put(signalKey, data); err != nil {
		return fmt.Errorf("failed to write invalidation signal: %w", err)
	}

	if s.config.DebugLevel > 0 {
		logger.Debugf("Wrote cross-node invalidation signal: user=%s, reason=%s", userUUID, reason)
	}

	return nil
}

// GetAllPermissions extracts all permission strings from a PermissionSet.
// Deprecated: Use CheckAccess or HasRequiredPermissions for efficient tree-based matching.
func (s *permissionCacheStore) GetAllPermissions(ps *PermissionSet) ([]string, []string) {
	if ps == nil {
		return nil, nil
	}

	allowPerms := ps.AllowTree.FindByPrefix("")
	denyPerms := ps.DenyTree.FindByPrefix("")

	return allowPerms, denyPerms
}

// CheckAccess checks if the permission set allows the required permission.
// Uses efficient radix tree matching with MatchWithWildcards - O(k) per check
// where k is the permission string length. No regex or string comparisons.
func (s *permissionCacheStore) CheckAccess(ps *PermissionSet, required string) bool {
	if ps == nil {
		return false
	}
	// Check if allowed (matches exact or any wildcard pattern in allow tree)
	if !ps.AllowTree.MatchWithWildcards(required) {
		return false
	}
	// Check if explicitly denied (deny overrides allow)
	if ps.DenyTree.MatchWithWildcards(required) {
		return false
	}
	return true
}

// HasRequiredPermissions checks if permission set satisfies API requirements.
// Uses efficient radix tree matching instead of string/regex comparisons.
// The requireAll parameter controls AND vs OR logic:
//   - requireAll=true: ALL permissions in 'required' must be satisfied (AND)
//   - requireAll=false: ANY permission in 'required' is sufficient (OR)
func (s *permissionCacheStore) HasRequiredPermissions(ps *PermissionSet, required []string, requireAll bool) bool {
	if len(required) == 0 {
		return true // No permissions required
	}
	if requireAll {
		// AND logic: all required permissions must be satisfied
		for _, req := range required {
			if !s.CheckAccess(ps, req) {
				return false
			}
		}
		return true
	}
	// OR logic (default): any required permission is sufficient
	for _, req := range required {
		if s.CheckAccess(ps, req) {
			return true
		}
	}
	return false
}

// CheckAccessWithMatch is like CheckAccess but also returns which permission matched.
// Uses efficient radix tree matching. Returns (allowed, matchedAllow, matchedDeny).
// This is useful for audit/diagnostic APIs that need to report which permission granted/denied access.
func (s *permissionCacheStore) CheckAccessWithMatch(ps *PermissionSet, required string) (allowed bool, matchedAllow, matchedDeny string) {
	if ps == nil {
		return false, "", ""
	}

	// Check if allowed - find which permission matched
	if ps.AllowTree.MatchWithWildcards(required) {
		// Find the actual matching permission for reporting
		// Check progressively broader wildcards to find the match
		matchedAllow = findMatchingPermission(ps.AllowTree, required)
		allowed = true
	}

	if !allowed {
		return false, "", "" // No allow matched → deny by default
	}

	// Check if explicitly denied
	if ps.DenyTree.MatchWithWildcards(required) {
		matchedDeny = findMatchingPermission(ps.DenyTree, required)
		return false, matchedAllow, matchedDeny // Explicit deny → access denied
	}

	return true, matchedAllow, "" // Allow matched and no deny matched
}

// findMatchingPermission finds which permission in the tree matched the required permission.
// It checks progressively broader wildcards to find the actual match.
func findMatchingPermission(tree *utils.HashTree, required string) string {
	// Check exact match first
	if tree.HasPrefix(required) {
		// Verify it's an exact match, not just a prefix
		matches := tree.FindByPrefix(required)
		for _, m := range matches {
			if m == required {
				return required
			}
		}
	}

	// Check progressively broader wildcards: a.b.c.d -> a.b.c.*, a.b.*, a.*, *
	parts := splitPermission(required)
	for i := len(parts) - 1; i >= 0; i-- {
		wildcard := joinPermission(parts[:i]) + ".*"
		if i == 0 {
			wildcard = "*"
		}
		matches := tree.FindByPrefix("")
		for _, m := range matches {
			if m == wildcard {
				return wildcard
			}
		}
	}

	// Fallback - shouldn't happen if MatchWithWildcards returned true
	return ""
}

// splitPermission splits a permission string by dots
func splitPermission(perm string) []string {
	if perm == "" {
		return nil
	}
	result := []string{}
	start := 0
	for i := 0; i < len(perm); i++ {
		if perm[i] == '.' {
			result = append(result, perm[start:i])
			start = i + 1
		}
	}
	result = append(result, perm[start:])
	return result
}

// joinPermission joins permission parts with dots
func joinPermission(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for i := 1; i < len(parts); i++ {
		result += "." + parts[i]
	}
	return result
}

// CheckAccessFromStrings is a standalone function that checks access using string slices.
// It builds temporary HashTrees and uses tree-based matching.
// This is useful for diagnostic/audit APIs that have already resolved permissions to strings.
// For hot-path authorization, use CheckAccess with a PermissionSet instead.
func CheckAccessFromStrings(allowPerms, denyPerms []string, required string) bool {
	// Build temporary HashTrees
	allowTree := utils.NewHashTree()
	for _, p := range allowPerms {
		allowTree.InsertString(p)
	}
	denyTree := utils.NewHashTree()
	for _, p := range denyPerms {
		denyTree.InsertString(p)
	}

	// Check if allowed
	if !allowTree.MatchWithWildcards(required) {
		return false
	}
	// Check if denied
	if denyTree.MatchWithWildcards(required) {
		return false
	}
	return true
}

// CheckAccessFromStringsWithMatch is like CheckAccessFromStrings but returns which permission matched.
// This is useful for diagnostic/audit APIs.
func CheckAccessFromStringsWithMatch(allowPerms, denyPerms []string, required string) (allowed bool, matchedAllow, matchedDeny string) {
	// Build temporary HashTrees
	allowTree := utils.NewHashTree()
	for _, p := range allowPerms {
		allowTree.InsertString(p)
	}
	denyTree := utils.NewHashTree()
	for _, p := range denyPerms {
		denyTree.InsertString(p)
	}

	// Check if allowed - find which permission matched
	if allowTree.MatchWithWildcards(required) {
		matchedAllow = findMatchingPermission(allowTree, required)
		allowed = true
	}

	if !allowed {
		return false, "", "" // No allow matched → deny by default
	}

	// Check if explicitly denied
	if denyTree.MatchWithWildcards(required) {
		matchedDeny = findMatchingPermission(denyTree, required)
		return false, matchedAllow, matchedDeny // Explicit deny → access denied
	}

	return true, matchedAllow, "" // Allow matched and no deny matched
}

// buildStorageKey constructs the storage key for a permission set
func (s *permissionCacheStore) buildStorageKey(userUUID string, checksum uint64) string {
	return fmt.Sprintf("%s%s/%016x", s.config.StorageKeyPrefix, userUUID, checksum)
}

// addToCache adds a permission set to the in-memory cache with FIFO eviction
func (s *permissionCacheStore) addToCache(userUUID string, permSet *PermissionSet) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove existing entry if present
	if existing, ok := s.cache[userUUID]; ok {
		s.currentMemoryBytes -= int64(existing.permSet.SerializedSize)
		s.fifoList.Remove(existing.element)
		delete(s.cache, userUUID)
	}

	// Evict oldest entries if needed (FIFO - remove from back)
	for s.needsEviction(permSet.SerializedSize) && s.fifoList.Len() > 0 {
		oldest := s.fifoList.Back()
		if oldest != nil {
			oldUserUUID := oldest.Value.(string)
			if oldEntry, ok := s.cache[oldUserUUID]; ok {
				s.currentMemoryBytes -= int64(oldEntry.permSet.SerializedSize)
				delete(s.cache, oldUserUUID)
				if s.config.DebugLevel > 0 {
					logger.Debugf("Evicted cache entry for user %s (FIFO)", oldUserUUID)
				}
			}
			s.fifoList.Remove(oldest)
		}
	}

	// Add new entry to front of FIFO list
	entry := &cacheEntry{
		permSet: permSet,
	}
	entry.element = s.fifoList.PushFront(userUUID)
	s.cache[userUUID] = entry
	s.currentMemoryBytes += int64(permSet.SerializedSize)
}

// needsEviction checks if we need to evict entries to make room
func (s *permissionCacheStore) needsEviction(newEntrySize int) bool {
	// Check entry count limit
	if len(s.cache) >= s.config.MaxCacheEntries {
		return true
	}

	// Check memory limit
	if s.config.MaxCacheMemoryBytes > 0 {
		if s.currentMemoryBytes+int64(newEntrySize) > s.config.MaxCacheMemoryBytes {
			return true
		}
	}

	return false
}

// watchInvalidationSignals watches for cross-node invalidation signals
func (s *permissionCacheStore) watchInvalidationSignals() {
	if s.informer == nil {
		logger.Warnf("No informer available for watching invalidation signals")
		return
	}

	updateCh, errCh := s.informer.Watch(s.config.InvalidationKeyPrefix)

	logger.Infof("Started watching for invalidation signals at prefix: %s", s.config.InvalidationKeyPrefix)

	for {
		select {
		case update, ok := <-updateCh:
			if !ok {
				logger.Warnf("Invalidation signal update channel closed")
				return
			}

			var signal CacheInvalidationSignal
			if err := json.Unmarshal(update.Value, &signal); err != nil {
				logger.Warnf("Failed to unmarshal invalidation signal: %v", err)
				continue
			}

			if s.config.DebugLevel > 0 {
				logger.Debugf("Received invalidation signal: user=%s, reason=%s", signal.UserUUID, signal.Reason)
			}

			// Perform invalidation
			if signal.UserUUID == "" {
				s.InvalidateAll()
			} else {
				s.Invalidate(signal.UserUUID)
			}

		case err, ok := <-errCh:
			if !ok {
				logger.Warnf("Invalidation signal error channel closed")
				return
			}
			logger.Errorf("Informer error: %v", err)

		case <-s.stopCh:
			logger.Infof("Stopping invalidation signal watcher")
			return
		}
	}
}

// Stop gracefully shuts down the cache store
func (s *permissionCacheStore) Stop() {
	if s.watcherStarted {
		close(s.stopCh)
		s.watcherStarted = false
	}
}

// Shutdown stops the singleton cache store
func Shutdown() {
	if instance != nil {
		instance.Stop()
	}
}
