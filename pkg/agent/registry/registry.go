// Package registry stores the per-agent record (id, permissions,
// expiry, cert fingerprint, revocation state) in hula's storage
// layer so all team members agree on which agents are valid.
//
// FSM key layout:
//
//	_agents/by-id/<id>            → JSON-encoded Record
//	_agents/by-fingerprint/<hex>  → <id>
//
// The fingerprint index is the fast path for the mTLS verification
// middleware: at handshake time we hash the presented client cert,
// look up the agent ID, then load the full record. The by-id index
// is the canonical store; the by-fingerprint index is rebuilt from
// it if it ever drifts.
package registry

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tlalocweb/hulation/pkg/store/storage"
)

// PrefixByID is the FSM prefix where the canonical agent records live.
const PrefixByID = "_agents/by-id/"

// PrefixByFingerprint is the FSM prefix mapping cert SHA-256 (hex,
// lowercase, no separators) to agent ID. Built from PrefixByID.
const PrefixByFingerprint = "_agents/by-fingerprint/"

// ErrNotFound is returned when no record exists for the given id /
// fingerprint. Distinct from storage.ErrNotFound so the caller can
// reason in agent-domain terms.
var ErrNotFound = errors.New("agent registry: not found")

// Record is the canonical per-agent record.
//
// Permissions: site → verb → option-string. Empty option string
// means "verb allowed with no flags." See HULAAGENT_PLAN.md for the
// allow-list verbs and matching semantics.
type Record struct {
	ID          string                       `json:"id"`
	Permissions map[string]map[string]string `json:"permissions"`
	CertSHA256  string                       `json:"cert_sha256"`
	CreatedAt   time.Time                    `json:"created_at"`
	ExpiresAt   time.Time                    `json:"expires_at"`
	Revoked     bool                         `json:"revoked,omitempty"`
}

// IsActive returns true iff the record is not revoked and the cert
// hasn't expired. Callers checking auth at handshake/RPC time should
// always use this rather than poking at the fields directly so the
// "revoked" and "expired" cases share a code path.
func (r *Record) IsActive(now time.Time) bool {
	if r.Revoked {
		return false
	}
	if !r.ExpiresAt.IsZero() && now.After(r.ExpiresAt) {
		return false
	}
	return true
}

// IsAllowed returns the registered option-string for (site, verb)
// when the agent is authorized; the second return is false when the
// agent has no entry for that verb on that site. The option-string
// is what hula will pass through to the underlying handler — agents
// don't get to override it at call time (Phase 5 will enforce this
// in the route-level checks).
func (r *Record) IsAllowed(site, verb string) (string, bool) {
	if r == nil {
		return "", false
	}
	verbs, ok := r.Permissions[site]
	if !ok {
		return "", false
	}
	opts, ok := verbs[verb]
	return opts, ok
}

// Put writes the record under PrefixByID and refreshes the
// fingerprint index. Both writes happen in a single Batch so a crash
// can't leave the indexes inconsistent.
//
// Caller is responsible for setting CertSHA256 before calling Put;
// FingerprintFromCert is the helper for that.
func Put(ctx context.Context, s storage.Storage, r *Record) error {
	if s == nil {
		return errors.New("agent registry: nil storage")
	}
	if r == nil || r.ID == "" {
		return errors.New("agent registry: id is required")
	}
	if r.CertSHA256 == "" {
		return errors.New("agent registry: cert_sha256 is required")
	}

	body, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal record: %w", err)
	}
	ops := []storage.BatchOp{
		{Op: storage.OpPut, Key: PrefixByID + r.ID, Value: body},
		{Op: storage.OpPut, Key: PrefixByFingerprint + r.CertSHA256, Value: []byte(r.ID)},
	}
	return s.Batch(ctx, ops)
}

// GetByID loads the canonical record. Returns ErrNotFound when the
// agent isn't registered.
func GetByID(ctx context.Context, s storage.Storage, id string) (*Record, error) {
	if s == nil {
		return nil, errors.New("agent registry: nil storage")
	}
	if id == "" {
		return nil, errors.New("agent registry: id is required")
	}
	body, err := s.Get(ctx, PrefixByID+id)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("unmarshal record %s: %w", id, err)
	}
	return &r, nil
}

// GetByFingerprint resolves the cert fingerprint to an agent ID, then
// loads the full record. Used by the mTLS verification middleware
// (Phase 3): given the presented client cert's SHA-256, walk the
// index to the canonical record in two cheap key-value reads.
//
// If the fingerprint index is present but the canonical record is
// gone (data corruption, partial revoke), returns ErrNotFound — the
// fingerprint shouldn't be trusted in that case.
func GetByFingerprint(ctx context.Context, s storage.Storage, fingerprint string) (*Record, error) {
	if s == nil {
		return nil, errors.New("agent registry: nil storage")
	}
	if fingerprint == "" {
		return nil, errors.New("agent registry: fingerprint is required")
	}
	idBytes, err := s.Get(ctx, PrefixByFingerprint+strings.ToLower(fingerprint))
	if errors.Is(err, storage.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return GetByID(ctx, s, string(idBytes))
}

// SetRevoked flips the Revoked flag on the named agent. Returns
// ErrNotFound when no such agent. Idempotent — calling twice with
// the same value is a no-op.
func SetRevoked(ctx context.Context, s storage.Storage, id string, revoked bool) error {
	r, err := GetByID(ctx, s, id)
	if err != nil {
		return err
	}
	if r.Revoked == revoked {
		return nil
	}
	r.Revoked = revoked
	body, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return s.Put(ctx, PrefixByID+r.ID, body)
}

// List walks every record under PrefixByID. Used by `hulactl
// list-agents` (Phase 6). Returns records sorted by ID for stable
// output.
func List(ctx context.Context, s storage.Storage) ([]*Record, error) {
	if s == nil {
		return nil, errors.New("agent registry: nil storage")
	}
	all, err := s.List(ctx, PrefixByID)
	if err != nil {
		return nil, err
	}
	out := make([]*Record, 0, len(all))
	for _, body := range all {
		var r Record
		if err := json.Unmarshal(body, &r); err != nil {
			// Skip records that can't decode — better to surface a
			// partial list than 500 the entire endpoint. Phase 6
			// adds an explicit "corrupt" warning when this happens.
			continue
		}
		out = append(out, &r)
	}
	return out, nil
}

// FingerprintFromCert returns the lowercase-hex SHA-256 of cert.Raw,
// matching what the by-fingerprint index keys on. cert.Raw is the
// DER bytes of the leaf certificate — the same bytes the mTLS layer
// inspects at handshake time, so a fingerprint computed here matches
// what GetByFingerprint resolves at request time.
func FingerprintFromCert(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}
