// Package fcm delivers pushes to Firebase Cloud Messaging via the
// FCM HTTP v1 API.
//
// Auth: OAuth2 service-account token exchange — the service-account
// JSON has a private RSA key we use to sign a JWT assertion, then
// POST to token.googleapis.com to exchange for a short-lived access
// token. Same two-hop dance every Google-Cloud library uses.
//
// The token is cached + refreshed; rotations happen automatically
// via the oauth2 library's reuse-tokensource wrapper.
//
// Dead tokens: FCM returns HTTP 404 with error.status="NOT_FOUND"
// or HTTP 400 with error.status="INVALID_ARGUMENT" referencing the
// registration token when it's been uninstalled / reset. We surface
// notifier.DeadTokenError so the caller marks the device inactive.

package fcm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2/google"

	"github.com/tlalocweb/hulation/pkg/notifier"
)

// Config carries the operator-supplied service-account JSON path +
// the project ID (FCM requires the projectId in the endpoint URL).
type Config struct {
	ProjectID            string
	ServiceAccountJSONPath string
	HTTPClient           *http.Client
}

// Backend implements notifier.Notifier against FCM v1.
type Backend struct {
	cfg      Config
	http     *http.Client
	saBytes  []byte

	tokMu    sync.Mutex
	token    string
	tokenExp time.Time
}

// New reads the service-account JSON. A misconfigured Config is
// tolerated — Deliver reports ErrNotConfigured per recipient rather
// than failing boot.
func New(cfg Config) (*Backend, error) {
	b := &Backend{cfg: cfg}
	b.http = cfg.HTTPClient
	if b.http == nil {
		b.http = &http.Client{Timeout: 15 * time.Second}
	}
	if cfg.ServiceAccountJSONPath == "" {
		return b, nil // unconfigured
	}
	raw, err := os.ReadFile(cfg.ServiceAccountJSONPath)
	if err != nil {
		return nil, fmt.Errorf("fcm: read sa: %w", err)
	}
	b.saBytes = raw
	return b, nil
}

// Channel returns ChannelFCM.
func (b *Backend) Channel() notifier.Channel { return notifier.ChannelFCM }

func (b *Backend) configured() bool {
	return b != nil && len(b.saBytes) > 0 && b.cfg.ProjectID != ""
}

func (b *Backend) accessToken(ctx context.Context) (string, error) {
	b.tokMu.Lock()
	defer b.tokMu.Unlock()
	if b.token != "" && time.Now().Before(b.tokenExp) {
		return b.token, nil
	}
	creds, err := google.CredentialsFromJSON(ctx, b.saBytes, "https://www.googleapis.com/auth/firebase.messaging")
	if err != nil {
		return "", fmt.Errorf("fcm: creds: %w", err)
	}
	tok, err := creds.TokenSource.Token()
	if err != nil {
		return "", fmt.Errorf("fcm: token: %w", err)
	}
	b.token = tok.AccessToken
	b.tokenExp = tok.Expiry.Add(-2 * time.Minute) // refresh slightly early
	return b.token, nil
}

// fcmMessage is the FCM v1 top-level envelope (subset).
type fcmMessage struct {
	Message fcmMessageBody `json:"message"`
}

type fcmMessageBody struct {
	Token        string             `json:"token"`
	Notification fcmMessageNotification `json:"notification,omitempty"`
}

type fcmMessageNotification struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
}

// Deliver iterates recipients with ChannelFCM. Non-FCM addrs skipped.
func (b *Backend) Deliver(ctx context.Context, env notifier.Envelope) ([]notifier.ChannelResult, error) {
	var out []notifier.ChannelResult
	for _, r := range env.Recipients {
		if r.Channel != notifier.ChannelFCM {
			continue
		}
		if !b.configured() {
			out = append(out, notifier.ChannelResult{
				Channel:  notifier.ChannelFCM,
				UserID:   r.UserID,
				DeviceID: r.DeviceID,
				OK:       false,
				Err:      notifier.ErrNotConfigured,
			})
			continue
		}
		err := b.sendOne(ctx, r, env)
		out = append(out, notifier.ChannelResult{
			Channel:  notifier.ChannelFCM,
			UserID:   r.UserID,
			DeviceID: r.DeviceID,
			OK:       err == nil,
			Err:      err,
		})
	}
	return out, nil
}

func (b *Backend) sendOne(ctx context.Context, r notifier.DeviceAddr, env notifier.Envelope) error {
	body, err := json.Marshal(fcmMessage{
		Message: fcmMessageBody{
			Token: r.PushToken,
			Notification: fcmMessageNotification{
				Title: env.Subject,
				Body:  env.ShortText,
			},
		},
	})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("https://fcm.googleapis.com/v1/projects/%s/messages:send", b.cfg.ProjectID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	tok, err := b.accessToken(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "Bearer "+tok)
	req.Header.Set("content-type", "application/json")

	resp, err := b.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	var er struct {
		Error struct {
			Code    int    `json:"code"`
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &er)
	// Dead-token classification. The string matching here is
	// defensive — FCM mixes Status and Message for the same cause
	// depending on when you ask.
	if resp.StatusCode == http.StatusNotFound || er.Error.Status == "NOT_FOUND" ||
		er.Error.Status == "INVALID_ARGUMENT" ||
		strings.Contains(er.Error.Message, "Requested entity was not found") ||
		strings.Contains(er.Error.Message, "registration-token-not-registered") {
		return &notifier.DeadTokenError{DeviceID: r.DeviceID, Wrapped: fmt.Errorf("fcm %d %s", resp.StatusCode, er.Error.Status)}
	}
	return fmt.Errorf("fcm: HTTP %d: %s", resp.StatusCode, string(raw))
}
