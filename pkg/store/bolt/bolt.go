// Package bolt is hula's embedded BoltDB-backed store for
// identity-and-access data that doesn't belong in ClickHouse
// (ACL grants, goal definitions, scheduled reports, report-run
// logs, invites / password-reset tokens when those ship).
//
// Opened once at boot by server.RunUnified; held as a process-global
// handle via Get() so every service that needs to persist can share
// the connection without a giant DI graph.
package bolt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/tlalocweb/hulation/log"
)

// DefaultPath is where the Bolt file lands inside a hula container.
// Operators can override via HULA_BOLT_PATH.
const DefaultPath = "/var/hula/data/hula.bolt"

// Buckets in use. Keep the list small + documented — every addition
// touches the migration story.
const (
	BucketServerAccess = "server_access"  // key = userID|serverID, value = role name
	BucketGoals        = "goals"          // key = goalID, value = JSON Goal proto
	BucketReports      = "reports"        // key = reportID, value = JSON ScheduledReport proto
	BucketReportRuns   = "report_runs"    // key = runID, value = JSON ReportRun proto
	BucketAlerts       = "alerts"         // key = alertID, value = JSON Alert proto
	BucketAlertEvents  = "alert_events"   // key = eventID, value = JSON AlertEvent proto
	BucketAuditForget  = "audit_forget"   // key = visitorID|ts, value = JSON ForgetAuditRow
	BucketMobileDevices     = "mobile_devices"      // key = deviceID, value = StoredDevice
	BucketNotificationSends = "notification_sends"  // key = sendID,   value = StoredNotificationSend
	BucketNotificationPrefs = "notification_prefs"  // key = userID,   value = StoredNotificationPrefs
	BucketOpaqueRecords     = "opaque_records"      // key = "provider|username", value = StoredOpaqueRecord
	BucketChatACL           = "chat_acl"            // key = server_id, value = JSON StoredChatRoster
)

var (
	mu      sync.RWMutex
	handle  *bolt.DB
	pathStr string
)

// Open opens the Bolt database at the given path. Directory is
// created if missing. Safe to call from multiple goroutines; only
// the first call actually opens. Returns the process-global handle.
func Open(path string) (*bolt.DB, error) {
	if path == "" {
		path = os.Getenv("HULA_BOLT_PATH")
	}
	if path == "" {
		path = DefaultPath
	}

	mu.Lock()
	defer mu.Unlock()
	if handle != nil {
		return handle, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("bolt: mkdir %s: %w", filepath.Dir(path), err)
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("bolt: open %s: %w", path, err)
	}

	// Ensure all buckets exist so readers don't have to bucket.CreateIfMissing.
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, b := range []string{
			BucketServerAccess,
			BucketGoals,
			BucketReports,
			BucketReportRuns,
			BucketAlerts,
			BucketAlertEvents,
			BucketAuditForget,
			BucketMobileDevices,
			BucketNotificationSends,
			BucketNotificationPrefs,
			BucketOpaqueRecords,
			BucketChatACL,
		} {
			if _, e := tx.CreateBucketIfNotExists([]byte(b)); e != nil {
				return fmt.Errorf("bucket %s: %w", b, e)
			}
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, err
	}

	handle = db
	pathStr = path
	log.Infof("bolt: opened %s", path)
	return db, nil
}

// Get returns the process-global *bolt.DB. Returns nil when Open
// hasn't been called — callers should treat that as "Bolt unavailable"
// and degrade gracefully.
func Get() *bolt.DB {
	mu.RLock()
	defer mu.RUnlock()
	return handle
}

// Close closes the process-global handle. Safe to call multiple
// times; subsequent Opens re-initialise. Typically called from the
// signal-handling shutdown path.
func Close() error {
	mu.Lock()
	defer mu.Unlock()
	if handle == nil {
		return nil
	}
	err := handle.Close()
	handle = nil
	pathStr = ""
	return err
}

// Path reports the file path of the currently-open store, or empty
// when closed. Handy for diagnostics.
func Path() string {
	mu.RLock()
	defer mu.RUnlock()
	return pathStr
}

// ErrNotOpen is returned by convenience wrappers when Get() is nil.
var ErrNotOpen = errors.New("bolt: store not open")
