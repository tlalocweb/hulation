package badactor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	iradix "github.com/hashicorp/go-immutable-radix/v2"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"gorm.io/gorm"
)

var baLog = log.GetTaggedLogger("badactor", "Bad actor detection and IP blocking")

// BadActorEntry is the value stored in the radix tree per IP.
type BadActorEntry struct {
	Score      int
	DetectedAt time.Time
	ExpiresAt  time.Time
	LastReason string
}

// Store is the central bad actor detection engine.
type Store struct {
	cfg            *config.BadActorConfig
	db             *gorm.DB
	sigs           *CompiledSignatures
	tree           atomic.Pointer[iradix.Tree[BadActorEntry]]
	allowTree      atomic.Pointer[iradix.Tree[struct{}]]
	validPaths     []string // known valid URL path prefixes
	servers        []*config.Server
	ttl            time.Duration
	evictInterval  time.Duration
	blockThreshold int
	cancel         context.CancelFunc
}

var store *Store

// GetStore returns the singleton bad actor store (nil if not enabled).
func GetStore() *Store { return store }

// IsEnabled returns true if the bad actor feature is active.
func IsEnabled() bool {
	return store != nil && !store.cfg.Disable
}

// Init initializes the bad actor detection system.
func Init(cfg *config.BadActorConfig, db *gorm.DB, servers []*config.Server) error {
	if cfg == nil || cfg.Disable {
		return nil
	}

	ttl, err := time.ParseDuration(cfg.TTL)
	if err != nil {
		return err
	}
	evictInterval, err := time.ParseDuration(cfg.EvictionInterval)
	if err != nil {
		return err
	}

	sigs, err := LoadSignatures(cfg.SignaturesFile)
	if err != nil {
		return err
	}
	baLog.Infof("loaded %d signatures (%d url, %d ua, %d qs)",
		len(sigs.All), len(sigs.URL), len(sigs.UserAgent), len(sigs.QueryString))

	// Migrate tables
	if err := AutoMigrateBadActorModels(db); err != nil {
		return fmt.Errorf("badactor: migration error: %w", err)
	}

	s := &Store{
		cfg:            cfg,
		db:             db,
		sigs:           sigs,
		servers:        servers,
		ttl:            ttl,
		evictInterval:  evictInterval,
		blockThreshold: cfg.BlockThreshold,
	}
	s.tree.Store(iradix.New[BadActorEntry]())
	s.allowTree.Store(iradix.New[struct{}]())

	// Build valid paths from server configs
	s.buildValidPaths(servers)

	// Load existing data from ClickHouse
	if !cfg.NoLoadFromDB {
		s.loadFromDB()
	}

	// Start eviction goroutine
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go s.evictionLoop(ctx)

	store = s
	baLog.Infof("initialized (threshold=%d, ttl=%s, dry_run=%v)", s.blockThreshold, ttl, cfg.DryRun)
	return nil
}

// Shutdown stops the bad actor system.
func Shutdown() {
	if store != nil && store.cancel != nil {
		store.cancel()
	}
}

// Reinit tears down the current bad actor system and re-initializes with new config.
func Reinit(cfg *config.BadActorConfig, db *gorm.DB, servers []*config.Server) error {
	Shutdown()
	store = nil
	return Init(cfg, db, servers)
}

func (s *Store) buildValidPaths(servers []*config.Server) {
	// Hula's own routes
	s.validPaths = append(s.validPaths, "/v/", "/scripts/", "/hulastatus", "/api/")

	for _, srv := range servers {
		// Backend virtual paths
		for _, b := range srv.Backends {
			if b.VirtualPath != "" {
				s.validPaths = append(s.validPaths, b.VirtualPath)
			}
		}
		// Static folder prefixes
		for _, f := range srv.NonRootStaticFolders {
			if f.URLPrefix != "" {
				s.validPaths = append(s.validPaths, f.URLPrefix)
			}
		}
	}
	baLog.Debugf("valid path prefixes: %v", s.validPaths)
}

func (s *Store) loadFromDB() {
	// Load bad actors
	actors, err := LoadRecentBadActors(s.db, s.ttl)
	if err != nil {
		baLog.Errorf("error loading from DB: %s", err)
	} else {
		txn := s.tree.Load().Txn()
		now := time.Now()
		for ip, score := range actors {
			txn.Insert([]byte(ip), BadActorEntry{
				Score:      score,
				DetectedAt: now,
				ExpiresAt:  now.Add(s.ttl),
				LastReason: "loaded from DB",
			})
		}
		s.tree.Store(txn.Commit())
		baLog.Infof("loaded %d IPs from DB", len(actors))
	}

	// Load allowlist
	allowed, err := LoadAllowlist(s.db)
	if err != nil {
		baLog.Errorf("error loading allowlist: %s", err)
	} else {
		txn := s.allowTree.Load().Txn()
		for _, ip := range allowed {
			txn.Insert([]byte(ip), struct{}{})
		}
		s.allowTree.Store(txn.Commit())
		baLog.Infof("loaded %d allowlisted IPs", len(allowed))
	}
}

// CheckAndBlock checks if a request should be blocked.
// Returns (should_block, reason).
func (s *Store) CheckAndBlock(ip, userAgent, method, urlPath, queryString, host string) (bool, string) {
	// 1. Allowlist check
	if _, found := s.allowTree.Load().Get([]byte(ip)); found {
		return false, ""
	}

	// 2. Known bad actor check
	tree := s.tree.Load()
	if entry, found := tree.Get([]byte(ip)); found {
		if time.Now().Before(entry.ExpiresAt) {
			if entry.Score >= s.blockThreshold {
				return !s.cfg.DryRun, entry.LastReason
			}
		} else {
			// Expired — lazy evict
			s.evictIP(ip)
		}
	}

	// 3. Signature matching
	matches := s.sigs.MatchRequest(urlPath, userAgent, queryString, s.isValidPath)
	if len(matches) == 0 {
		return false, ""
	}

	// Accumulate score
	totalScore := 0
	var topReason, topSigName, topCategory string
	for _, m := range matches {
		totalScore += m.Score
		if m.Score > 0 {
			topReason = m.Reason
			topSigName = m.SigName
			topCategory = m.Category
		}
	}

	// Get existing score for this IP
	existingScore := 0
	if entry, found := tree.Get([]byte(ip)); found {
		existingScore = entry.Score
	}
	newScore := existingScore + totalScore

	// Update radix tree
	now := time.Now()
	txn := s.tree.Load().Txn()
	txn.Insert([]byte(ip), BadActorEntry{
		Score:      newScore,
		DetectedAt: now,
		ExpiresAt:  now.Add(s.ttl),
		LastReason: topReason,
	})
	s.tree.Store(txn.Commit())

	// Record to ClickHouse (async)
	go func() {
		for _, m := range matches {
			if err := InsertBadActorRecord(s.db, ip, userAgent, method, urlPath, host, m.Reason, m.SigName, m.Category, m.Score); err != nil {
				baLog.Errorf("error recording to DB: %s", err)
			}
		}
	}()

	if newScore >= s.blockThreshold {
		if s.cfg.DryRun {
			baLog.Warnf("[DRY RUN] would block %s (score=%d, reason=%s, sig=%s)", ip, newScore, topReason, topSigName)
			return false, ""
		}
		baLog.Infof("BLOCKED %s (score=%d, reason=%s, sig=%s, category=%s)", ip, newScore, topReason, topSigName, topCategory)
		return true, topReason
	}

	baLog.Debugf("flagged %s +%d=%d (threshold %d, reason=%s)", ip, totalScore, newScore, s.blockThreshold, topReason)
	return false, ""
}

// CheckKnownOnly checks only the radix tree (no signature matching).
// Used for pre-TLS blocking where we have no HTTP data.
func (s *Store) CheckKnownOnly(ip string) (bool, string) {
	// Allowlist
	if _, found := s.allowTree.Load().Get([]byte(ip)); found {
		return false, ""
	}
	if entry, found := s.tree.Load().Get([]byte(ip)); found {
		if time.Now().Before(entry.ExpiresAt) && entry.Score >= s.blockThreshold {
			return !s.cfg.DryRun, entry.LastReason
		}
	}
	return false, ""
}

// isValidPath checks if a URL-type signature match should be skipped
// because the path is actually served by the server.
func (s *Store) isValidPath(sig *CompiledSignature, path string) bool {
	// Check cache first
	if cached, ok := sig.validatedPaths.Load(path); ok {
		return cached.(bool)
	}

	// Check against known valid prefixes
	for _, prefix := range s.validPaths {
		if strings.HasPrefix(path, prefix) {
			sig.validatedPaths.Store(path, true)
			return true
		}
	}

	// Check if static file exists on disk (for servers with Root configured)
	// This is a one-time stat per unique path
	for _, srv := range s.getServers() {
		if srv.Root != "" {
			fullPath := filepath.Join(srv.Root, path)
			if _, err := os.Stat(fullPath); err == nil {
				sig.validatedPaths.Store(path, true)
				return true
			}
		}
	}

	sig.validatedPaths.Store(path, false)
	return false
}

func (s *Store) getServers() []*config.Server {
	return s.servers
}

func (s *Store) evictIP(ip string) {
	txn := s.tree.Load().Txn()
	txn.Delete([]byte(ip))
	s.tree.Store(txn.Commit())
}

func (s *Store) evictionLoop(ctx context.Context) {
	ticker := time.NewTicker(s.evictInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepExpired()
		}
	}
}

func (s *Store) sweepExpired() {
	tree := s.tree.Load()
	now := time.Now()
	var expired []string
	iter := tree.Root().Iterator()
	for key, entry, ok := iter.Next(); ok; key, entry, ok = iter.Next() {
		if now.After(entry.ExpiresAt) {
			expired = append(expired, string(key))
		}
	}
	if len(expired) > 0 {
		txn := tree.Txn()
		for _, ip := range expired {
			txn.Delete([]byte(ip))
		}
		s.tree.Store(txn.Commit())
		baLog.Debugf("evicted %d expired entries", len(expired))
	}
}

// --- Methods used by admin API ---

// EvictIPManual removes an IP from the bad actor list.
func (s *Store) EvictIPManual(ip string) {
	s.evictIP(ip)
}

// AddToAllowlist adds an IP to the in-memory allowlist.
func (s *Store) AddToAllowlist(ip string) {
	txn := s.allowTree.Load().Txn()
	txn.Insert([]byte(ip), struct{}{})
	s.allowTree.Store(txn.Commit())
}

// RemoveFromAllowlist removes an IP from the in-memory allowlist.
func (s *Store) RemoveFromAllowlist(ip string) {
	txn := s.allowTree.Load().Txn()
	txn.Delete([]byte(ip))
	s.allowTree.Store(txn.Commit())
}

// GetBlockedCount returns the number of IPs currently in the bad actor tree.
func (s *Store) GetBlockedCount() int {
	return s.tree.Load().Len()
}

// GetAllowlistCount returns the number of IPs in the allowlist.
func (s *Store) GetAllowlistCount() int {
	return s.allowTree.Load().Len()
}

// GetSignatures returns the compiled signatures (for admin listing).
func (s *Store) GetSignatures() *CompiledSignatures {
	return s.sigs
}

// GetConfig returns the bad actor config.
func (s *Store) GetConfig() *config.BadActorConfig {
	return s.cfg
}

// ListBlockedIPsWithDetail returns IPs with their entries including IP and blocked status.
func (s *Store) ListBlockedIPsWithDetail(limit, offset int) []BadActorListEntry {
	var entries []BadActorListEntry
	tree := s.tree.Load()
	iter := tree.Root().Iterator()
	i := 0
	for key, entry, ok := iter.Next(); ok; key, entry, ok = iter.Next() {
		if i >= offset {
			entries = append(entries, BadActorListEntry{
				IP:         string(key),
				Score:      entry.Score,
				DetectedAt: entry.DetectedAt,
				ExpiresAt:  entry.ExpiresAt,
				LastReason: entry.LastReason,
				Blocked:    entry.Score >= s.blockThreshold,
			})
			if limit > 0 && len(entries) >= limit {
				break
			}
		}
		i++
	}
	return entries
}
