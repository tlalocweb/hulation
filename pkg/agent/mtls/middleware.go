// Package mtls is the request-time half of the agent auth flow.
// pkg/agent/pki mints CAs and signs leaf certs; pkg/agent/registry
// stores the per-agent record. This package walks the middle:
// after the TLS handshake validates the chain (the unified
// listener's ClientCAs trust pool now includes the Agent CA), the
// middleware here looks up the presented cert by SHA-256
// fingerprint, refuses revoked / expired records up front, and
// attaches the live Record to the request context for downstream
// route handlers (Phase 5) to consult.
//
// Phase 3 wires this layer in. Phase 5 will plug per-route checks
// against record.IsAllowed(site, verb) into the verb endpoints
// listed in HULAAGENT_PLAN.md.
package mtls

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/tlalocweb/hulation/pkg/agent/pki"
	"github.com/tlalocweb/hulation/pkg/agent/registry"
	"github.com/tlalocweb/hulation/pkg/store/storage"
)

// ctxKey scopes the context value so callers can't accidentally
// collide with a string key elsewhere.
type ctxKey int

const recordKey ctxKey = 1

// AgentLookup is the registry lookup the middleware drives. The
// production wiring passes registry.GetByFingerprint bound to the
// global storage; tests pass an in-memory stub. Returning
// registry.ErrNotFound for an unknown fingerprint is treated as
// "this isn't a registered agent" — the request proceeds with no
// record attached, and downstream handlers can decide whether the
// route requires one.
type AgentLookup func(ctx context.Context, fingerprint string) (*registry.Record, error)

// Now returns the current time. Overridable for tests.
type Now func() time.Time

// Middleware returns the http.Handler middleware. Boot wires it
// into the unified server via AttachHTTPMiddleware.
//
// Behaviour per request:
//
//   - No client cert presented → pass through untouched. Plenty of
//     legitimate non-agent traffic doesn't authenticate at the TLS
//     layer (visitors, public site requests, admin browser flows
//     using JWT). Phase 5 route handlers gate on record presence.
//
//   - Client cert presented but NOT signed by the Agent CA → pass
//     through untouched. Could be team-internal traffic (Team CA),
//     izcragent (legacy internal CA), or a misconfigured client.
//     The TLS layer already verified the chain so by the time we
//     get here a non-Agent-CA cert is genuinely a different trust
//     domain — leave it alone.
//
//   - Cert signed by Agent CA, fingerprint not in registry →
//     respond 401. The agent presented a valid Agent-CA leaf but
//     no record exists, which means create-agent never wrote one
//     or the fingerprint index drifted. Either way, refuse —
//     letting the request through and counting on downstream auth
//     would silently flatten an agent's missing registry record
//     into an unauthenticated request.
//
//   - Cert signed by Agent CA, record exists, IsActive() false →
//     respond 401 with reason "revoked" or "expired". Distinct
//     from "not found" so an operator can tell why curl is
//     rejecting their cert.
//
//   - Cert signed by Agent CA, record active → attach record to
//     context and call next. Phase 5 handlers read it via
//     RecordFromContext.
func Middleware(getCA func() *pki.CA, lookup AgentLookup, now Now) func(http.Handler) http.Handler {
	if now == nil {
		now = time.Now
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ca := getCA()
			if ca == nil || ca.Cert == nil {
				next.ServeHTTP(w, r)
				return
			}
			leaf := peerLeaf(r)
			if leaf == nil {
				next.ServeHTTP(w, r)
				return
			}
			if !signedByAgentCA(leaf, ca.Cert) {
				next.ServeHTTP(w, r)
				return
			}

			fp := registry.FingerprintFromCert(leaf)
			rec, err := lookup(r.Context(), fp)
			if err != nil {
				if isNotFound(err) {
					http.Error(w, "agent not registered", http.StatusUnauthorized)
					return
				}
				http.Error(w, "agent registry unavailable", http.StatusServiceUnavailable)
				return
			}
			if !rec.IsActive(now()) {
				reason := "agent expired"
				if rec.Revoked {
					reason = "agent revoked"
				}
				http.Error(w, reason, http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), recordKey, rec)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RecordFromContext returns the active agent record attached by
// the middleware, or nil if the request didn't authenticate as an
// agent. Phase 5 route handlers use this to decide whether to gate
// on registry permissions vs. accept JWT/admin auth.
func RecordFromContext(ctx context.Context) *registry.Record {
	if v := ctx.Value(recordKey); v != nil {
		if r, ok := v.(*registry.Record); ok {
			return r
		}
	}
	return nil
}

// BindRegistryLookup is the production AgentLookup binding —
// closes over the global storage so the middleware doesn't need to
// re-resolve it on every request. When storage isn't online (e.g.
// degraded boot) the lookup returns ErrNotFound, which the
// middleware translates to a 401 — refusing agent traffic during a
// degraded boot is the safe default.
func BindRegistryLookup(getStore func() storage.Storage) AgentLookup {
	return func(ctx context.Context, fingerprint string) (*registry.Record, error) {
		s := getStore()
		if s == nil {
			return nil, registry.ErrNotFound
		}
		return registry.GetByFingerprint(ctx, s, fingerprint)
	}
}

func peerLeaf(r *http.Request) *x509.Certificate {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return nil
	}
	return r.TLS.PeerCertificates[0]
}

// signedByAgentCA reports whether the leaf was actually signed by
// THIS Agent CA's key. The TLS layer already established the chain
// against ClientCAs (which contains the Agent CA + any other
// trusted roots) so we know the leaf is structurally valid; the
// question here is "is this leaf in the agent trust domain
// specifically, vs. some other CA we also trust". A DN-string
// comparison is too weak (two unrelated CAs can share fields);
// verifying the signature with the Agent CA's public key is the
// strict discriminator.
func signedByAgentCA(leaf, caCert *x509.Certificate) bool {
	if leaf == nil || caCert == nil {
		return false
	}
	if !strings.HasPrefix(leaf.Subject.CommonName, pki.AgentSubjectPrefix) {
		return false
	}
	return leaf.CheckSignatureFrom(caCert) == nil
}

func isNotFound(err error) bool {
	return errors.Is(err, registry.ErrNotFound)
}
