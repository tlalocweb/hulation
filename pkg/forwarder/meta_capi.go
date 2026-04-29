package forwarder

// Meta Conversions API adapter.
//
// API:  https://graph.facebook.com/v19.0/<pixel_id>/events
// Auth: Bearer access_token in body or query.
//
// Payload shape (one event):
//
//	{
//	  "data": [{
//	    "event_name": "PageView",
//	    "event_time": <unix>,
//	    "event_source_url": "<url>",
//	    "action_source": "website",
//	    "user_data": {
//	      "client_ip_address": "<ip>",
//	      "client_user_agent": "<ua>",
//	      "em": ["<sha256(lowercased trimmed email)>"],   // optional
//	      "external_id": ["<sha256(visitor_id)>"]
//	    }
//	  }],
//	  "test_event_code": "<optional>"   // when set, events show up
//	                                    // only in the Meta Test Events tool
//	}
//
// Hashing:
//   * em = SHA256(lowercase(trim(email))) — Meta's standard
//   * external_id = SHA256(visitor_id)
//   * IP + UA are sent in the clear (Meta hashes them server-side)
//
// Consent gate: PurposeMarketing.
//
// Deletion: Meta exposes a deletion endpoint
// (https://graph.facebook.com/v19.0/<pixel_id>/conversion_data) but
// it requires posting hashed PII the visitor supplied. Without that
// PII we don't have a primary key to ask Meta to delete. v1 logs a
// debug line for the deletion attempt and returns nil; future work
// can wire in the email→pixel send-history Bolt index if customers
// ask.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tlalocweb/hulation/log"
)

// MetaCAPI is the adapter struct.
type MetaCAPI struct {
	PixelID       string
	AccessToken   string
	TestEventCode string            // optional — empty disables Test Events mode
	EventMap      map[uint64]string // hula EventCode → Meta event_name
	HTTPClient    *http.Client      // optional — defaults to http.DefaultClient with 5s timeout
	APIVersion    string            // optional — defaults to v19.0
}

// NewMetaCAPI constructs an adapter. PixelID + AccessToken are
// required; missing values surface at first Send.
func NewMetaCAPI(pixelID, accessToken, testEventCode string, eventMap map[uint64]string) *MetaCAPI {
	return &MetaCAPI{
		PixelID:       pixelID,
		AccessToken:   accessToken,
		TestEventCode: testEventCode,
		EventMap:      eventMap,
		HTTPClient:    &http.Client{Timeout: 5 * time.Second},
		APIVersion:    "v19.0",
	}
}

// Name implements Adapter.
func (m *MetaCAPI) Name() string { return "meta_capi" }

// Purpose implements Adapter — Meta CAPI is marketing.
func (m *MetaCAPI) Purpose() ConsentPurpose { return PurposeMarketing }

// Send implements Adapter. POSTs a single-event batch.
func (m *MetaCAPI) Send(ctx context.Context, ev Event) error {
	if m.PixelID == "" || m.AccessToken == "" {
		return fmt.Errorf("meta_capi: pixel_id and access_token required")
	}
	eventName, ok := m.EventMap[ev.EventCode]
	if !ok || eventName == "" {
		return ErrSkipped
	}

	user := map[string]interface{}{
		"client_ip_address": ev.IP,
		"client_user_agent": ev.UserAgent,
		"external_id":       []string{sha256Hex(ev.VisitorID)},
	}
	if ev.Email != "" {
		user["em"] = []string{sha256Hex(strings.ToLower(strings.TrimSpace(ev.Email)))}
	}

	when := ev.When
	if when.IsZero() {
		when = time.Now()
	}
	body := map[string]interface{}{
		"data": []map[string]interface{}{{
			"event_name":       eventName,
			"event_time":       when.Unix(),
			"event_source_url": ev.URL,
			"action_source":    "website",
			"user_data":        user,
		}},
	}
	if m.TestEventCode != "" {
		body["test_event_code"] = m.TestEventCode
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("meta_capi: marshal: %w", err)
	}

	url := fmt.Sprintf("https://graph.facebook.com/%s/%s/events?access_token=%s",
		m.APIVersion, m.PixelID, m.AccessToken)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("meta_capi: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := m.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("meta_capi: do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("meta_capi: status %d", resp.StatusCode)
	}
	return nil
}

// Delete implements Adapter. Meta's conversion_data deletion API
// requires hashed PII; we don't index sends by hashed PII in v1, so
// this is a no-op that records intent. Future work: walk
// (server_id, visitor_id) → email association from the Phase-4b
// chat sessions store and post a deletion request per email.
func (m *MetaCAPI) Delete(ctx context.Context, serverID, visitorID string) error {
	log.Debugf("meta_capi: delete request noted (server=%s visitor=%s) — Meta deletion API not implemented", serverID, visitorID)
	return nil
}

func sha256Hex(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
