// Package apns talks to Apple Push Notification service directly
// over HTTP/2. No vendor SDK — the on-wire format is small enough
// that auditability + single-hop dependency graph outweighs the
// handful of convenience methods a library would offer.
//
// Auth: provider JWT signed with ES256 using the p8 auth key. Token
// is cached + rotated every ~40m (Apple allows up to 60m; 40 is the
// comfortable margin).
//
// Dead-token handling: APNs returns HTTP 410 with
// reason="BadDeviceToken" or similar permanents → wrap the per-
// recipient error in notifier.DeadTokenError so the caller marks
// the device inactive.

package apns

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/tlalocweb/hulation/pkg/notifier"
)

// EndpointProduction is the live APNs HTTP/2 endpoint. Tests can
// override with EndpointSandbox.
const (
	EndpointProduction = "https://api.push.apple.com"
	EndpointSandbox    = "https://api.sandbox.push.apple.com"
)

// Config is what operators supply. All fields are required for the
// backend to deliver; any empty field → backend is "not configured"
// and Deliver returns ErrNotConfigured per recipient.
type Config struct {
	TeamID      string // Apple Developer team (10 chars)
	KeyID       string // p8 key ID
	KeyPEMPath  string // path to the p8 PEM
	BundleID    string // app's bundle ID — carried in apns-topic header
	Endpoint    string // EndpointProduction or EndpointSandbox; default production
	HTTPClient  *http.Client
}

// Backend implements notifier.Notifier against APNs.
type Backend struct {
	cfg     Config
	key     *ecdsa.PrivateKey
	http    *http.Client

	jwtMu   sync.Mutex
	jwt     string
	jwtExp  time.Time
}

// New parses the p8 key and returns a ready backend. A misconfigured
// Config is tolerated at construction — Deliver reports
// ErrNotConfigured per recipient rather than failing boot.
func New(cfg Config) (*Backend, error) {
	b := &Backend{cfg: cfg}
	b.http = cfg.HTTPClient
	if b.http == nil {
		// Stdlib default client enables HTTP/2 since go 1.x; APNs
		// requires h2.
		b.http = &http.Client{Timeout: 15 * time.Second}
	}
	if b.cfg.Endpoint == "" {
		b.cfg.Endpoint = EndpointProduction
	}
	if cfg.KeyPEMPath == "" {
		return b, nil // unconfigured — Deliver will short-circuit
	}
	pemBytes, err := os.ReadFile(cfg.KeyPEMPath)
	if err != nil {
		return nil, fmt.Errorf("apns: read key: %w", err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("apns: key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("apns: parse key: %w", err)
	}
	ec, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("apns: key is not ECDSA")
	}
	b.key = ec
	return b, nil
}

// Channel returns ChannelAPNS.
func (b *Backend) Channel() notifier.Channel { return notifier.ChannelAPNS }

func (b *Backend) configured() bool {
	return b != nil && b.key != nil && b.cfg.TeamID != "" && b.cfg.KeyID != "" && b.cfg.BundleID != ""
}

// providerToken returns a cached JWT, refreshing when close to
// expiry. The key is loaded once in New; here we just sign a fresh
// JWS token with the current timestamp.
func (b *Backend) providerToken() (string, error) {
	b.jwtMu.Lock()
	defer b.jwtMu.Unlock()
	if b.jwt != "" && time.Now().Before(b.jwtExp) {
		return b.jwt, nil
	}
	claims := jwt.RegisteredClaims{
		Issuer:   b.cfg.TeamID,
		IssuedAt: jwt.NewNumericDate(time.Now()),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tok.Header["kid"] = b.cfg.KeyID
	signed, err := tok.SignedString(b.key)
	if err != nil {
		return "", fmt.Errorf("apns: sign jwt: %w", err)
	}
	b.jwt = signed
	b.jwtExp = time.Now().Add(40 * time.Minute)
	return signed, nil
}

// apnsPayload is the minimum aps-wrapped JSON APNs accepts.
type apnsPayload struct {
	APS apnsPayloadAPS `json:"aps"`
	// Custom app-level fields attach here via an embedded JSON
	// merging step if we ever need them.
}

type apnsPayloadAPS struct {
	Alert apnsPayloadAlert `json:"alert"`
	Sound string           `json:"sound,omitempty"`
}

type apnsPayloadAlert struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
}

// Deliver iterates recipients with ChannelAPNS, POSTs each. Skips
// non-APNs addrs silently.
func (b *Backend) Deliver(ctx context.Context, env notifier.Envelope) ([]notifier.ChannelResult, error) {
	var out []notifier.ChannelResult
	for _, r := range env.Recipients {
		if r.Channel != notifier.ChannelAPNS {
			continue
		}
		if !b.configured() {
			out = append(out, notifier.ChannelResult{
				Channel:  notifier.ChannelAPNS,
				UserID:   r.UserID,
				DeviceID: r.DeviceID,
				OK:       false,
				Err:      notifier.ErrNotConfigured,
			})
			continue
		}
		err := b.sendOne(ctx, r, env)
		out = append(out, notifier.ChannelResult{
			Channel:  notifier.ChannelAPNS,
			UserID:   r.UserID,
			DeviceID: r.DeviceID,
			OK:       err == nil,
			Err:      err,
		})
	}
	return out, nil
}

func (b *Backend) sendOne(ctx context.Context, r notifier.DeviceAddr, env notifier.Envelope) error {
	body, err := json.Marshal(apnsPayload{
		APS: apnsPayloadAPS{
			Alert: apnsPayloadAlert{
				Title: env.Subject,
				Body:  env.ShortText,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("apns: marshal payload: %w", err)
	}
	url := b.cfg.Endpoint + "/3/device/" + r.PushToken
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	tok, err := b.providerToken()
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "bearer "+tok)
	req.Header.Set("apns-topic", b.cfg.BundleID)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("content-type", "application/json")

	resp, err := b.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	// Permanents: 410 Gone, or 400 with reason="BadDeviceToken" etc.
	if resp.StatusCode == http.StatusGone {
		return &notifier.DeadTokenError{DeviceID: r.DeviceID, Wrapped: errors.New("apns 410")}
	}
	// Peek the reason for 400-range errors to classify badtoken reasons.
	raw, _ := io.ReadAll(resp.Body)
	var er struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(raw, &er)
	switch er.Reason {
	case "BadDeviceToken", "Unregistered", "DeviceTokenNotForTopic", "MissingDeviceToken":
		return &notifier.DeadTokenError{DeviceID: r.DeviceID, Wrapped: fmt.Errorf("apns %d %s", resp.StatusCode, er.Reason)}
	}
	return fmt.Errorf("apns: HTTP %d: %s", resp.StatusCode, string(raw))
}
