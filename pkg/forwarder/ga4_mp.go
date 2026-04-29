package forwarder

// GA4 Measurement Protocol adapter.
//
// API:  https://www.google-analytics.com/mp/collect
//        ?measurement_id=<id>&api_secret=<secret>
//
// Payload shape (one batch):
//
//	{
//	  "client_id": "<visitor_id>",
//	  "events": [{
//	    "name": "page_view",
//	    "params": {
//	      "page_location": "<url>",
//	      "page_referrer": "<referer>"
//	    }
//	  }]
//	}
//
// client_id should look UUID-shaped per GA4 expectations; hula's
// visitor IDs are uuid.NewV7 strings, so they pass.
//
// Consent gate: PurposeAnalytics.
//
// Deletion: GA4 has a User Deletion API
// (https://developers.google.com/analytics/devguides/config/userdeletion)
// but it requires a service-account creds + a different auth flow.
// v1 logs a debug line; future work can wire in the OAuth2 service-
// account exchange + post a deletion request when customers ask.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/tlalocweb/hulation/log"
)

// GA4MP is the adapter struct.
type GA4MP struct {
	MeasurementID string
	APISecret     string
	EventMap      map[uint64]string // hula EventCode → GA4 event_name
	HTTPClient    *http.Client      // optional — defaults to 5s timeout
	Endpoint      string            // optional — overridable for tests
}

// NewGA4MP constructs an adapter.
func NewGA4MP(measurementID, apiSecret string, eventMap map[uint64]string) *GA4MP {
	return &GA4MP{
		MeasurementID: measurementID,
		APISecret:     apiSecret,
		EventMap:      eventMap,
		HTTPClient:    &http.Client{Timeout: 5 * time.Second},
		Endpoint:      "https://www.google-analytics.com/mp/collect",
	}
}

// Name implements Adapter.
func (g *GA4MP) Name() string { return "ga4_mp" }

// Purpose implements Adapter — GA4 is analytics.
func (g *GA4MP) Purpose() ConsentPurpose { return PurposeAnalytics }

// Send implements Adapter.
func (g *GA4MP) Send(ctx context.Context, ev Event) error {
	if g.MeasurementID == "" || g.APISecret == "" {
		return fmt.Errorf("ga4_mp: measurement_id and api_secret required")
	}
	eventName, ok := g.EventMap[ev.EventCode]
	if !ok || eventName == "" {
		return ErrSkipped
	}

	params := map[string]interface{}{
		"page_location": ev.URL,
		"page_referrer": ev.Referer,
	}
	body := map[string]interface{}{
		"client_id": ev.VisitorID,
		"events": []map[string]interface{}{{
			"name":   eventName,
			"params": params,
		}},
	}
	if !ev.When.IsZero() {
		// GA4 expects timestamp_micros at the top level for past events.
		body["timestamp_micros"] = ev.When.UnixMicro()
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("ga4_mp: marshal: %w", err)
	}

	url := fmt.Sprintf("%s?measurement_id=%s&api_secret=%s",
		g.Endpoint, g.MeasurementID, g.APISecret)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("ga4_mp: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := g.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ga4_mp: do: %w", err)
	}
	defer resp.Body.Close()
	// GA4 MP returns 204 No Content on success; some versions return 200.
	if resp.StatusCode != 204 && resp.StatusCode != 200 {
		return fmt.Errorf("ga4_mp: status %d", resp.StatusCode)
	}
	return nil
}

// Delete implements Adapter — User Deletion API not wired in v1.
func (g *GA4MP) Delete(ctx context.Context, serverID, visitorID string) error {
	log.Debugf("ga4_mp: delete request noted (server=%s visitor=%s) — GA4 User Deletion API not implemented", serverID, visitorID)
	return nil
}
