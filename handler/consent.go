package handler

// Phase 4c.1 — visitor-side consent resolution.
//
// Given the per-server consent_mode, the body's optional `consent`
// object, and the request's Sec-GPC header, decide:
//
//   1. The effective consent state to tag onto the event.
//   2. Whether the event should be written at all (opt_in mode
//      requires affirmative consent before any row lands).
//   3. The source label that goes onto the consent_log audit row.
//
// Precedence (highest → lowest):
//   client-supplied CMP payload  >  Sec-GPC: 1 (marketing only)
//                                >  per-server default

import (
	"strings"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"
)

const (
	// HeaderSecGPC is the W3C Global Privacy Control opt-out signal.
	// Browsers that respect GPC send "1"; we treat it as a binding
	// marketing opt-out per the emerging US state-law consensus.
	HeaderSecGPC = "Sec-GPC"

	// HeaderHulaConsentRequired is the response hint we set when
	// opt_in mode rejected an event for missing consent. CMPs can
	// listen for this to surface their banner sooner.
	HeaderHulaConsentRequired = "Hula-Consent-Required"

	consentModeOff    = "off"
	consentModeOptIn  = "opt_in"
	consentModeOptOut = "opt_out"
)

// ConsentDecision is the resolver output.
type ConsentDecision struct {
	State  ConsentState
	Block  bool // true means "do not write event row" (opt_in + no consent)
}

// resolveConsent computes the effective consent state for this
// request. `bodyConsent` is the client-supplied object on /v/hello
// (nil when absent). `gpcHeader` is the value of the Sec-GPC HTTP
// header.
func resolveConsent(hostconf *config.Server, bodyConsent *ConsentState, gpcHeader string) ConsentDecision {
	mode := strings.ToLower(strings.TrimSpace(hostconf.ConsentMode))
	if mode == "" {
		mode = consentModeOff
	}

	gpcOptOut := strings.TrimSpace(gpcHeader) == "1"

	switch {
	case bodyConsent != nil:
		// Client CMP has spoken — authoritative for both purposes.
		// Even in opt_in mode, an explicit `{analytics:false,
		// marketing:false}` payload is honored: we still write a
		// consent_log row recording the decline, but we do not
		// write an event row.
		st := *bodyConsent
		st.Source = "cmp_payload"
		block := mode == consentModeOptIn && !st.Analytics
		return ConsentDecision{State: st, Block: block}

	case gpcOptOut:
		// GPC binds marketing only; analytics still proceeds under
		// the per-server default. In opt_in mode, GPC alone isn't
		// a positive analytics signal, so we still gate the event.
		st := ConsentState{
			Analytics: mode == consentModeOptOut, // off + opt_in → no analytics consent yet
			Marketing: false,                     // GPC = no marketing
			Source:    "gpc_header",
		}
		// In off mode we DO process analytics under
		// legitimate-interest even when GPC=1.
		if mode == consentModeOff {
			st.Analytics = true
		}
		block := mode == consentModeOptIn
		return ConsentDecision{State: st, Block: block}

	default:
		// No client signal, no GPC. Apply per-server default.
		switch mode {
		case consentModeOptIn:
			return ConsentDecision{
				State: ConsentState{Source: "default_optin"},
				Block: true,
			}
		case consentModeOptOut:
			return ConsentDecision{
				State: ConsentState{
					Analytics: true,
					Marketing: true,
					Source:    "default_optout",
				},
				Block: false,
			}
		default: // consentModeOff
			return ConsentDecision{
				State: ConsentState{
					Analytics: true,
					Marketing: true,
					Source:    "default_off",
				},
				Block: false,
			}
		}
	}
}

// recordConsent writes an audit row to the consent_log Bolt bucket.
// Failures are logged at warn but never block the request — the
// event row is the GDPR-relevant primary record; the consent log is
// supplementary.
func recordConsent(serverID, visitorID string, st ConsentState) {
	err := hulabolt.PutConsent(hulabolt.StoredConsent{
		ServerID:  serverID,
		VisitorID: visitorID,
		Analytics: st.Analytics,
		Marketing: st.Marketing,
		Source:    st.Source,
	})
	if err != nil {
		log.Debugf("consent_log write failed (non-fatal): %s", err.Error())
	}
}
