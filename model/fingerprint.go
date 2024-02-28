package model

const (
	sqlCreateVisitorFinergrpints = `
	CREATE TABLE IF NOT EXISTS visitor_fingerprints
	(
		^id^ String,
		^created_at^ DateTime64(3),
		^fingerprint^ String,
		^ip^ String,
		^visitor_id^ String,
		^ss_cookie^ String
	)`
)

// Fingerprints are (supposedly) unique to the visitor
// We only just record each fingerprint once and we never alter the entry afterwards
type VisitorFingerprint struct {
	HModelRO
	Type        string // indicates the fingerprint type (e.g. "creepjs", "thumbmarkjs" "server" etc.)
	Fingerprint string // unique fingerprint reported via /hello API
	Details     string // optional additional data which can be used for anayltics
	IP          string // IP address of the visitor at the time of the fingerprint
	VisitorID   string // A visitor ID if we had at the time we go this fingerprint
	SSCookie    string // the SSCookie if one existed at the time
}
