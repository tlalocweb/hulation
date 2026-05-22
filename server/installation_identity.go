package server

// Public HTTP handler for GET /api/v1/installation/identity.
//
// Mobile clients fetch this *before* they have credentials so they can pin the
// installation's Noise_IK static public key alongside the paired-server row.
// The mobile core (hula-mobile, common/src/keys.rs::store_noise_server_public_key)
// then uses that pin for every subsequent Noise handshake against this
// installation — a mismatch (server reset, MITM attempt) fails the handshake at
// the IK static-key auth step.
//
// The response is intentionally tiny and stable:
//
//	{
//	  "noise_static_public_key_b64": "<32 raw bytes, base64url, no padding>"
//	}
//
// Status codes:
//
//	200 — key configured; payload returned
//	404 — installation has no Noise key set; Noise mode is unavailable
//	500 — configured key is malformed (operator error; see logs)

import (
	"encoding/base64"
	"encoding/json"
	"net/http"

	"golang.org/x/crypto/curve25519"

	hulaapp "github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/utils"
)

// installationIdentityHandler returns the handler for GET
// /api/v1/installation/identity. The handler reads cfg each request (rather
// than at construction time) so an operator can rotate the key without a
// restart once a /reloadconfig path lands.
func installationIdentityHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeIdentityError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		cfg := hulaapp.GetConfig()
		if cfg == nil || cfg.NoiseStaticKey == "" {
			writeIdentityError(w, http.StatusNotFound, "noise_unavailable",
				"this installation has no Noise static key configured")
			return
		}
		secret, err := utils.DecodeNoiseStaticKey(cfg.NoiseStaticKey)
		if err != nil {
			log.Warnf("installation/identity: decode noise static: %s", err)
			writeIdentityError(w, http.StatusInternalServerError, "noise_misconfigured",
				"configured Noise static key is malformed")
			return
		}
		pub, err := curve25519.X25519(secret, curve25519.Basepoint)
		if err != nil {
			log.Warnf("installation/identity: derive noise public: %s", err)
			writeIdentityError(w, http.StatusInternalServerError, "noise_misconfigured",
				"could not derive Noise public key")
			return
		}

		resp := map[string]string{
			"noise_static_public_key_b64": base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(pub),
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=60")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func writeIdentityError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":    code,
		"message": message,
	})
}
