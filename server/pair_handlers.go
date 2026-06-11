package server

// QR pair endpoints — admin issues a short-lived single-use code in the SPA;
// the marketer's mobile app reads it from a QR, calls /api/v1/pair/redeem with
// its freshly-minted ed25519 public key, and the handler stores the resulting
// DeviceKey in the surrounding deviceKeyStore. Subsequent signed requests from
// that device satisfy the AdminBearerInterceptor's `claimsFromDeviceSignature`
// path.
//
// Wire shape (both endpoints):
//
//   POST /api/v1/pair/issue
//     Auth: admin bearer
//     Request:  {"user_id": "...", "server_id": "...", "label": "..."}
//                  (user_id falls back to the caller's admin username when empty;
//                   server_id is optional)
//     Response: {"code": "HULA-PAIR-XXXX-YYYY-ZZZZ", "expires_at": "<RFC3339>",
//                "user_id": "...", "server_id": "...", "label": "..."}
//
//   POST /api/v1/pair/redeem
//     Auth: none (the code is the proof of possession)
//     Request:  {"code": "HULA-PAIR-...", "device_id": "<uuid>",
//                "signing_public_key_b64": "<32-byte ed25519 pub, base64-std>",
//                "label": "..."}
//     Response: {"user_id": "...", "server_id": "...", "paired_at": "<RFC3339>"}

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/server/authware"
)

// pairCodeStoreSingleton + deviceKeyStoreSingleton hold the boot-time stores
// the handlers consult. Set once during boot via wirePairHandlers(); the
// handler closures capture them. Keeping these as package-level vars keeps
// the HTTP-handler signature plain `http.HandlerFunc` for symmetry with the
// other custom handlers (chat_start, installation_identity).
var (
	pairCodeStoreSingleton authware.PairCodeStore
	deviceKeyStoreSingleton authware.DeviceKeyStore
)

// wirePairHandlers connects the issue + redeem handlers to the runtime
// stores. Called from unified_boot.go after the stores are constructed.
// Idempotent — calling it twice with the same stores is harmless.
func wirePairHandlers(
	codes authware.PairCodeStore,
	devices authware.DeviceKeyStore,
) {
	pairCodeStoreSingleton = codes
	deviceKeyStoreSingleton = devices
}

// pairIssueHandler returns the handler for POST /api/v1/pair/issue.
// Requires an admin bearer on the request (planted by
// AdminBearerHTTPMiddleware); rejects non-admin or unauthenticated callers
// with 401.
func pairIssueHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writePairError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		if pairCodeStoreSingleton == nil {
			writePairError(w, http.StatusServiceUnavailable, "not_ready", "pair store not wired")
			return
		}
		claims, _ := r.Context().Value(authware.ClaimsKey).(*authware.Claims)
		if claims == nil || claims.Username == "" {
			writePairError(w, http.StatusUnauthorized, "unauthorized",
				"missing or invalid bearer token")
			return
		}
		if !hasAdminRole(claims) {
			writePairError(w, http.StatusForbidden, "forbidden",
				"only admin users may issue pair codes")
			return
		}

		var body struct {
			UserID   string `json:"user_id"`
			ServerID string `json:"server_id"`
			Label    string `json:"label"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024)).Decode(&body); err != nil {
			// Empty body is allowed — defaults below cover it.
			body.UserID = ""
		}
		userID := strings.TrimSpace(body.UserID)
		if userID == "" {
			userID = claims.Username
		}
		serverID := strings.TrimSpace(body.ServerID)
		label := strings.TrimSpace(body.Label)
		if len(label) > 128 {
			label = label[:128]
		}

		code, err := authware.GeneratePairCode(nil)
		if err != nil {
			log.Warnf("pair issue: generate code: %s", err)
			writePairError(w, http.StatusInternalServerError, "internal",
				"could not generate code")
			return
		}
		now := time.Now().UTC()
		entry := authware.PairCode{
			Code:      code,
			UserID:    userID,
			ServerID:  serverID,
			Label:     label,
			CreatedAt: now,
			ExpiresAt: now.Add(authware.DefaultPairCodeTTL),
		}
		pairCodeStoreSingleton.Put(entry)

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":       entry.Code,
			"user_id":    entry.UserID,
			"server_id":  entry.ServerID,
			"label":      entry.Label,
			"expires_at": entry.ExpiresAt.Format(time.RFC3339),
		})
	}
}

// pairRedeemHandler returns the handler for POST /api/v1/pair/redeem.
// Intentionally unauthenticated — the code itself is the proof of possession.
// Validates the code, parses the device public key, stores a DeviceKey, and
// returns the resolved (user_id, server_id) the redeemed device is paired
// for.
func pairRedeemHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writePairError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		if pairCodeStoreSingleton == nil || deviceKeyStoreSingleton == nil {
			writePairError(w, http.StatusServiceUnavailable, "not_ready", "pair stores not wired")
			return
		}
		var body struct {
			Code                string `json:"code"`
			DeviceID            string `json:"device_id"`
			SigningPublicKeyB64 string `json:"signing_public_key_b64"`
			Label               string `json:"label"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024)).Decode(&body); err != nil {
			writePairError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
			return
		}
		if strings.TrimSpace(body.Code) == "" ||
			strings.TrimSpace(body.DeviceID) == "" ||
			strings.TrimSpace(body.SigningPublicKeyB64) == "" {
			writePairError(w, http.StatusBadRequest, "bad_request",
				"code, device_id, and signing_public_key_b64 are required")
			return
		}
		// Canonicalise + checksum-verify before touching the store so a typo
		// surfaces as a 400 rather than walking the map.
		if _, err := authware.ParsePairCode(body.Code); err != nil {
			writePairError(w, http.StatusBadRequest, "bad_code",
				"code failed checksum — likely a typo")
			return
		}
		pubBytes, err := base64.StdEncoding.DecodeString(body.SigningPublicKeyB64)
		if err != nil {
			writePairError(w, http.StatusBadRequest, "bad_signing_key",
				"signing_public_key_b64 is not valid base64")
			return
		}
		if len(pubBytes) != 32 {
			writePairError(w, http.StatusBadRequest, "bad_signing_key",
				"signing_public_key_b64 must decode to 32 bytes")
			return
		}

		now := time.Now().UTC()
		entry, err := pairCodeStoreSingleton.Consume(body.Code, now)
		if err != nil {
			writePairError(w, http.StatusGone, "code_invalid",
				"code is unknown, expired, or already redeemed")
			return
		}

		// Both InMemoryDeviceKeyStore (tests + dev) and the bolt-backed
		// pkg/store/bolt.DeviceKeyStore (production) satisfy the writer
		// interface — no concrete type assertion needed.
		writer, ok := deviceKeyStoreSingleton.(authware.DeviceKeyWriter)
		if !ok {
			writePairError(w, http.StatusInternalServerError, "internal",
				"device-key store is read-only in this build")
			return
		}
		if err := writer.PutDeviceKey(authware.DeviceKey{
			DeviceID:  body.DeviceID,
			UserID:    entry.UserID,
			ServerID:  entry.ServerID,
			PublicKey: pubBytes,
			CreatedAt: now,
		}); err != nil {
			log.Warnf("pair redeem: put device key: %s", err)
			writePairError(w, http.StatusInternalServerError, "internal",
				"could not persist device key")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"user_id":   entry.UserID,
			"server_id": entry.ServerID,
			"paired_at": now.Format(time.RFC3339),
		})
	}
}

// pairListDevicesHandler returns the handler for GET /api/v1/pair/devices.
// Admin callers may list any user's devices via `?user_id=<id>`; non-admin
// callers can only list their own (the `user_id` query is ignored). Used by
// the SPA "paired devices" admin screen + the mobile self-service "manage
// my devices" surface.
func pairListDevicesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writePairError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		claims, _ := r.Context().Value(authware.ClaimsKey).(*authware.Claims)
		if claims == nil || claims.Username == "" {
			writePairError(w, http.StatusUnauthorized, "unauthorized",
				"missing or invalid bearer token")
			return
		}
		// Admin "all devices" view (?all=true) — the SPA paired-devices screen.
		// Admin-only; lists every device across users via the optional
		// DeviceKeyAllLister capability.
		if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("all")), "true") {
			if !hasAdminRole(claims) {
				writePairError(w, http.StatusForbidden, "forbidden",
					"only admin users may list all devices")
				return
			}
			allLister, ok := deviceKeyStoreSingleton.(authware.DeviceKeyAllLister)
			if !ok {
				writePairError(w, http.StatusServiceUnavailable, "not_ready",
					"device-key store does not support listing all devices")
				return
			}
			devices, err := allLister.ListAll(r.Context())
			if err != nil {
				log.Warnf("pair: list all devices: %s", err)
				writePairError(w, http.StatusInternalServerError, "internal",
					"could not list devices")
				return
			}
			writeDeviceList(w, devices)
			return
		}

		lister, ok := deviceKeyStoreSingleton.(authware.DeviceKeyLister)
		if !ok {
			writePairError(w, http.StatusServiceUnavailable, "not_ready",
				"device-key store does not support listing")
			return
		}
		targetUser := strings.TrimSpace(r.URL.Query().Get("user_id"))
		if targetUser == "" {
			targetUser = claims.Username
		} else if targetUser != claims.Username && !hasAdminRole(claims) {
			writePairError(w, http.StatusForbidden, "forbidden",
				"only admin users may list another user's devices")
			return
		}
		devices, err := lister.ListForUser(r.Context(), targetUser)
		if err != nil {
			log.Warnf("pair: list devices for %s: %s", targetUser, err)
			writePairError(w, http.StatusInternalServerError, "internal",
				"could not list devices")
			return
		}
		writeDeviceList(w, devices)
	}
}

// writeDeviceList encodes a device-key slice as the public list response shape:
// {"devices":[{device_id,user_id,server_id,public_key_b64,created_at}]}.
func writeDeviceList(w http.ResponseWriter, devices []authware.DeviceKey) {
	out := make([]map[string]any, 0, len(devices))
	for _, d := range devices {
		out = append(out, map[string]any{
			"device_id":      d.DeviceID,
			"user_id":        d.UserID,
			"server_id":      d.ServerID,
			"public_key_b64": base64.StdEncoding.EncodeToString(d.PublicKey),
			"created_at":     d.CreatedAt.Format(time.RFC3339),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"devices": out})
}

// pairRevokeDeviceHandler returns the handler for POST /api/v1/pair/devices/revoke.
// Either the device's owner OR an admin can revoke. Idempotent — revoking
// an unknown device returns 200 with `revoked: false` rather than 404, so
// retry-after-network-blip is cheap.
//
// Wire shape:
//
//	Request:  {"device_id": "<uuid>"}
//	Response: {"revoked": true|false, "device_id": "..."}
func pairRevokeDeviceHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writePairError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		claims, _ := r.Context().Value(authware.ClaimsKey).(*authware.Claims)
		if claims == nil || claims.Username == "" {
			writePairError(w, http.StatusUnauthorized, "unauthorized",
				"missing or invalid bearer token")
			return
		}
		var body struct {
			DeviceID string `json:"device_id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024)).Decode(&body); err != nil {
			writePairError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
			return
		}
		body.DeviceID = strings.TrimSpace(body.DeviceID)
		if body.DeviceID == "" {
			writePairError(w, http.StatusBadRequest, "bad_request", "device_id is required")
			return
		}
		if deviceKeyStoreSingleton == nil {
			writePairError(w, http.StatusServiceUnavailable, "not_ready", "device-key store not wired")
			return
		}

		// Ownership / admin gate. We look up the device to see who it
		// belongs to BEFORE deleting; non-admin callers revoking
		// someone else's device get a 403 (not a 404), so they can't
		// probe for the existence of arbitrary device ids.
		existing, ok := deviceKeyStoreSingleton.LookupDeviceKey(r.Context(), body.DeviceID)
		if !ok {
			// Idempotent: nothing to revoke.
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"revoked":   false,
				"device_id": body.DeviceID,
			})
			return
		}
		if existing.UserID != claims.Username && !hasAdminRole(claims) {
			writePairError(w, http.StatusForbidden, "forbidden",
				"can only revoke your own devices unless you are admin")
			return
		}

		revoker, ok := deviceKeyStoreSingleton.(authware.DeviceKeyRevoker)
		if !ok {
			writePairError(w, http.StatusServiceUnavailable, "not_ready",
				"device-key store does not support revocation")
			return
		}
		if err := revoker.Delete(body.DeviceID); err != nil {
			log.Warnf("pair: revoke device %s: %s", body.DeviceID, err)
			writePairError(w, http.StatusInternalServerError, "internal",
				"could not revoke device")
			return
		}
		log.Infof(
			"pair: revoked device %s (owner=%s, by=%s)",
			body.DeviceID, existing.UserID, claims.Username,
		)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"revoked":   true,
			"device_id": body.DeviceID,
		})
	}
}

func hasAdminRole(c *authware.Claims) bool {
	if c == nil {
		return false
	}
	for _, role := range c.Roles {
		if role == "admin" {
			return true
		}
	}
	return false
}

func writePairError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":    code,
		"message": message,
	})
}
