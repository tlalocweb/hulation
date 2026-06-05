// Package relayclient is the hulation-side client for the hula-push-relay
// `/v1/push/send` endpoint. It signs each request with the installation's ed25519
// private key — same canonical byte form the relay validates in
// `hula-push-relay/crates/relay/src/wire/signed.rs`:
//
//	METHOD \n PATH_WITH_QUERY \n TIMESTAMP \n NONCE \n BODY_SHA256_HEX
//
// Headers required on the wire:
//
//	Hula-Installation-Id: <issued at relay enrollment>
//	Hula-Timestamp:       <RFC3339, second precision, UTC>
//	Hula-Nonce:           <base64 16+ random bytes, unique per (installation, 24h)>
//	Hula-Body-SHA256:     <hex sha256 of body>
//	Hula-Signature:       <base64 ed25519 over the canonical form above>
//
// The caller supplies the ciphertext already sealed (see
// `github.com/tlalocweb/hulation/pkg/push/preview`) — this package is only the
// transport, not the crypto.
package relayclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Canonical header names. Capitalisation matches the relay's expectations exactly
// (http.Header is case-insensitive on reads, but we set them with the canonical
// casing for clarity in logs and tcpdump.)
const (
	HeaderInstallationID = "Hula-Installation-Id"
	HeaderTimestamp      = "Hula-Timestamp"
	HeaderNonce          = "Hula-Nonce"
	HeaderBodySHA256     = "Hula-Body-SHA256"
	HeaderSignature      = "Hula-Signature"
)

// Outcome mirrors the relay's HTTP-status classification so callers can pick a retry
// strategy without parsing the JSON body.
type Outcome int

const (
	OutcomeUnknown Outcome = iota
	// Dispatched: relay accepted the push for delivery (HTTP 202).
	OutcomeDispatched
	// ChannelRevoked: relay rejected because the channel was revoked, either at
	// registration time or auto-revoked by the relay after an InvalidToken from APNs
	// or FCM. The caller should drop the device record and stop trying.
	OutcomeChannelRevoked
	// BadRequest: the request was malformed (empty ciphertext, missing channel auth,
	// expired channel, unknown channel, bound-elsewhere). Permanent — not retryable.
	OutcomeBadRequest
	// Retryable: transient relay failure (5xx, transport error, retryable dispatcher
	// outcome). Caller backs off and retries.
	OutcomeRetryable
)

// Config holds the per-installation secrets the client needs to sign requests.
type Config struct {
	// BaseURL of the hula-push-relay, e.g. "https://relay.tlaloc.us".
	BaseURL string
	// InstallationID issued by the relay at enrollment time. Travels on every
	// signed request via Hula-Installation-Id.
	InstallationID string
	// SigningKey is the 32-byte ed25519 private seed (raw, not the 64-byte expanded
	// form). The relay holds the matching public.
	SigningKey ed25519.PrivateKey
	// HTTPClient is optional; defaults to a fresh client with sensible timeouts.
	HTTPClient *http.Client
}

// Client is the constructed value the fan-out path holds onto. Safe for concurrent
// use — the underlying http.Client + ed25519 signing key are immutable.
type Client struct {
	baseURL    string
	installID  string
	signingKey ed25519.PrivateKey
	http       *http.Client
}

// New validates the config and returns a ready-to-use client. Fails fast when the
// signing key is the wrong length or the URL is empty.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("relayclient: BaseURL required")
	}
	if cfg.InstallationID == "" {
		return nil, fmt.Errorf("relayclient: InstallationID required")
	}
	if len(cfg.SigningKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf(
			"relayclient: SigningKey must be %d bytes, got %d",
			ed25519.PrivateKeySize, len(cfg.SigningKey),
		)
	}
	httpc := cfg.HTTPClient
	if httpc == nil {
		httpc = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		installID:  cfg.InstallationID,
		signingKey: cfg.SigningKey,
		http:       httpc,
	}, nil
}

// SendPushParams is what the fan-out path builds per recipient device.
type SendPushParams struct {
	// PushChannelID issued by the relay at /v1/channels/register and persisted
	// alongside the device record.
	PushChannelID string
	// ChannelAuth is the per-channel shared secret the relay validates on every
	// send. Stored alongside the channel id; never logged in plaintext.
	ChannelAuth string
	// Ciphertext is the already-sealed visible-notification payload — base64-encoded
	// (URL-safe-no-pad recommended) per the preview wire format.
	Ciphertext string
	// CollapseID, when set, becomes APNs `apns-collapse-id` / FCM `collapse_key` —
	// pushes with the same id collapse on the device, e.g. so multiple new-chat
	// notifications for the same session don't pile up.
	CollapseID string
	// TTLSeconds requests how long the provider should retain the push when the
	// device is offline. Zero leaves it unset (provider default).
	TTLSeconds uint32
}

// SendPushResult is the parsed shape from a successful relay response (HTTP 202).
type SendPushResult struct {
	DeliveryID string
	Status     string
}

// SendPush issues a signed POST to /v1/push/send. Returns the outcome
// classification + parsed result when available + the underlying error (or nil on
// `OutcomeDispatched`). The caller looks at `outcome` to decide retry/drop/move-on.
func (c *Client) SendPush(ctx context.Context, p SendPushParams) (Outcome, SendPushResult, error) {
	if p.PushChannelID == "" || p.ChannelAuth == "" || p.Ciphertext == "" {
		return OutcomeBadRequest, SendPushResult{}, fmt.Errorf(
			"relayclient: push_channel_id, channel_auth, ciphertext are all required",
		)
	}
	body := map[string]any{
		"push_channel_id": p.PushChannelID,
		"channel_auth":    p.ChannelAuth,
		"ciphertext":      p.Ciphertext,
	}
	if p.CollapseID != "" {
		body["collapse_id"] = p.CollapseID
	}
	if p.TTLSeconds != 0 {
		body["ttl_seconds"] = p.TTLSeconds
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return OutcomeBadRequest, SendPushResult{}, fmt.Errorf("relayclient: marshal: %w", err)
	}

	path := "/v1/push/send"
	timestamp := time.Now().UTC().Format(time.RFC3339)
	nonce, err := newNonce()
	if err != nil {
		return OutcomeRetryable, SendPushResult{}, fmt.Errorf("relayclient: nonce: %w", err)
	}
	bodyHash := sha256.Sum256(bodyBytes)
	bodySha256Hex := hex.EncodeToString(bodyHash[:])
	canonical := canonicalSigningBytes("POST", path, timestamp, nonce, bodySha256Hex)
	signature := ed25519.Sign(c.signingKey, canonical)
	sigB64 := base64.StdEncoding.EncodeToString(signature)

	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return OutcomeRetryable, SendPushResult{}, fmt.Errorf("relayclient: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderInstallationID, c.installID)
	req.Header.Set(HeaderTimestamp, timestamp)
	req.Header.Set(HeaderNonce, nonce)
	req.Header.Set(HeaderBodySHA256, bodySha256Hex)
	req.Header.Set(HeaderSignature, sigB64)

	resp, err := c.http.Do(req)
	if err != nil {
		return OutcomeRetryable, SendPushResult{}, fmt.Errorf("relayclient: do: %w", err)
	}
	defer resp.Body.Close()

	rawResp, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	switch resp.StatusCode {
	case http.StatusAccepted:
		var out SendPushResult
		var parsed struct {
			DeliveryID string `json:"delivery_id"`
			Status     string `json:"status"`
		}
		if jerr := json.Unmarshal(rawResp, &parsed); jerr == nil {
			out.DeliveryID = parsed.DeliveryID
			out.Status = parsed.Status
		}
		return OutcomeDispatched, out, nil
	case http.StatusForbidden:
		// 403 = channel_revoked, channel_expired, channel_bound_elsewhere. The
		// first two mean the device should be dropped; bound-elsewhere is a
		// hulation-side bug (two installations sharing a device record). Surfaced
		// as "channel revoked" because that's the action the caller takes.
		return OutcomeChannelRevoked, SendPushResult{}, fmt.Errorf(
			"relayclient: %d %s", resp.StatusCode, truncate(rawResp, 256),
		)
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnauthorized:
		return OutcomeBadRequest, SendPushResult{}, fmt.Errorf(
			"relayclient: %d %s", resp.StatusCode, truncate(rawResp, 256),
		)
	default:
		if resp.StatusCode >= 500 {
			return OutcomeRetryable, SendPushResult{}, fmt.Errorf(
				"relayclient: %d %s", resp.StatusCode, truncate(rawResp, 256),
			)
		}
		return OutcomeBadRequest, SendPushResult{}, fmt.Errorf(
			"relayclient: %d %s", resp.StatusCode, truncate(rawResp, 256),
		)
	}
}

// canonicalSigningBytes mirrors the relay's signed::canonical_signing_bytes.
// Changing either side without the other will break every signed call.
func canonicalSigningBytes(method, pathWithQuery, timestamp, nonce, bodySha256Hex string) []byte {
	var buf bytes.Buffer
	buf.Grow(len(method) + len(pathWithQuery) + len(timestamp) + len(nonce) + len(bodySha256Hex) + 4)
	buf.WriteString(method)
	buf.WriteByte('\n')
	buf.WriteString(pathWithQuery)
	buf.WriteByte('\n')
	buf.WriteString(timestamp)
	buf.WriteByte('\n')
	buf.WriteString(nonce)
	buf.WriteByte('\n')
	buf.WriteString(bodySha256Hex)
	return buf.Bytes()
}

// newNonce returns 16 random bytes, base64-encoded. The relay validates that the
// (installation_id, nonce) pair has not been used inside the 24-hour replay window.
func newNonce() (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw[:]), nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
