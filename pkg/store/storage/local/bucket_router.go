package local

// Bucket routing for the LocalStorage implementation.
//
// The storage.Storage interface has no notion of buckets — keys
// are flat slash-delimited strings. LocalStorage maps the first
// slash-delimited segment of every key to a bbolt bucket name.
// This keeps the bbolt file's bucket layout sensibly partitioned
// (each domain's data in its own bucket, which makes ad-hoc
// `bolt-cli` introspection easier) without making bucket
// management a Storage-interface concern.

// Buckets is the canonical set of bbolt buckets that LocalStorage
// pre-creates at Open time. Adding a new bucket means: append it
// here, write key/value pairs whose first segment matches the
// bucket name, and `Storage.Get/Put` will route correctly.
//
// Anything written under a key whose first segment doesn't match
// any entry here lands in the BucketUnrouted fallback (and is
// logged at warn level by LocalStorage so typos surface).
//
// The names match the historic constants in pkg/store/bolt/bolt.go
// — by intent, not coincidence. We keep the same on-disk shape so
// `bolt-cli` dumps remain interpretable.
var Buckets = []string{
	"server_access",
	"goals",
	"reports",
	"report_runs",
	"alerts",
	"alert_events",
	"audit_forget",
	"mobile_devices",
	"notification_sends",
	"notification_prefs",
	"opaque_records",
	"chat_acl",
	"consent_log",
	"cookieless_salts",
}

// BucketUnrouted is where keys whose first segment doesn't match
// a known bucket end up. Useful for tests that write throwaway
// keys; surfaces operator typos via the warn log emitted by
// LocalStorage when a key routes here.
const BucketUnrouted = "storage_unrouted"

// bucketSet is the lookup table built once at package load.
var bucketSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(Buckets))
	for _, b := range Buckets {
		m[b] = struct{}{}
	}
	return m
}()

// routeKey returns (bucket, subKey) for a Storage key.
//
//	"consent_log/abc"     → "consent_log",   "abc"
//	"consent_log/a/b/c"   → "consent_log",   "a/b/c"   (only first '/' splits)
//	"unknown/foo"         → BucketUnrouted,  "unknown/foo"
//	"flat-key"            → BucketUnrouted,  "flat-key"
//	""                    → BucketUnrouted,  ""
//
// Note: when the bucket is unknown we keep the FULL original key
// as the sub-key so callers can still round-trip. The fallback
// bucket is a debugging aid, not a feature; anything that ends up
// there is operator error and the warn log surfaces it.
func routeKey(key string) (bucket, subKey string) {
	for i := 0; i < len(key); i++ {
		if key[i] == '/' {
			head := key[:i]
			if _, ok := bucketSet[head]; ok {
				return head, key[i+1:]
			}
			return BucketUnrouted, key
		}
	}
	return BucketUnrouted, key
}

// recompose builds the full Storage key from a (bucket, subKey)
// pair returned by an iterator. Inverse of routeKey for known
// buckets; for the fallback bucket the subKey is already the
// full original key, so we return it unchanged.
func recompose(bucket, subKey string) string {
	if bucket == BucketUnrouted {
		return subKey
	}
	return bucket + "/" + subKey
}
