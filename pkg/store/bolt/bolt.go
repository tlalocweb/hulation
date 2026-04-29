// Package bolt holds the schema + key-layout owners for hula's
// persistent state. After HA Plan 1 this package no longer owns
// the bbolt handle itself; it just defines:
//
//   - StoredX types (StoredAlert, StoredGoal, StoredOpaqueRecord, ...)
//   - the bucket-name constants
//   - thin accessor functions that take a storage.Storage argument
//
// The actual bbolt I/O lives in pkg/store/storage/local
// (LocalStorage) and, in production after HA Plan 2,
// pkg/store/storage/raft (RaftStorage).
package bolt

import "errors"

// Bucket names for the LocalStorage bucket router. Kept here as
// package constants because every accessor's key format starts with
// one of these names + "/". Adding a new bucket means appending it
// to local.Buckets in pkg/store/storage/local/bucket_router.go AND
// adding a constant here for ergonomic reference.
const (
	BucketServerAccess      = "server_access"
	BucketGoals             = "goals"
	BucketReports           = "reports"
	BucketReportRuns        = "report_runs"
	BucketAlerts            = "alerts"
	BucketAlertEvents       = "alert_events"
	BucketAuditForget       = "audit_forget"
	BucketMobileDevices     = "mobile_devices"
	BucketNotificationSends = "notification_sends"
	BucketNotificationPrefs = "notification_prefs"
	BucketOpaqueRecords     = "opaque_records"
	BucketChatACL           = "chat_acl"
	BucketConsentLog        = "consent_log"
	BucketCookielessSalts   = "cookieless_salts"
)

// ErrNotOpen is returned by accessors that fail because the
// underlying Storage hasn't been installed. Most production paths
// won't hit this — server boot installs storage.Global() before
// any handler runs — but tests + offline tools sometimes do, and
// the sentinel makes graceful-degrade easy.
var ErrNotOpen = errors.New("bolt: storage not installed")
