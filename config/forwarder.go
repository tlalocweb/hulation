package config

// ForwarderConfig defines one server-side forwarder. Phase 4c.2.
//
// Two adapter kinds are supported in v1: "meta_capi" and "ga4_mp".
// Each has its own credentials block; only one of {MetaCAPI, GA4MP}
// is read per entry (the kind selector picks).
//
//	forwarders:
//	  - kind: meta_capi
//	    pixel_id:     "{{env:META_PIXEL_ID}}"
//	    access_token: "{{env:META_CAPI_TOKEN}}"
//	    test_event_code: "{{env:META_TEST_CODE,optional}}"
//	    event_map:
//	      page_view: "PageView"
//	      form_submit: "Lead"
//	  - kind: ga4_mp
//	    measurement_id: "{{env:GA4_MEASUREMENT_ID}}"
//	    api_secret:     "{{env:GA4_API_SECRET}}"
//	    event_map:
//	      page_view: "page_view"
//	      form_submit: "generate_lead"
//
// Event map keys are the symbolic event names the rest of hula uses
// (page_view, click, form_start, form_submit, lander_hit, scrolled,
// referred). Unmapped events are silently skipped at the adapter.
type ForwarderConfig struct {
	// Kind selects the adapter. One of: "meta_capi", "ga4_mp".
	Kind string `yaml:"kind"`

	// QueueSize overrides the default per-server bounded queue
	// (1024). Leave 0 to use the default.
	QueueSize int `yaml:"queue_size,omitempty"`

	// EventMap maps hula event-code symbols to the adapter's event
	// names. Symbols recognised by the boot wiring:
	//   "page_view", "click", "form_start", "form_submit",
	//   "lander_hit", "scrolled", "referred"
	EventMap map[string]string `yaml:"event_map,omitempty"`

	// MetaCAPI fields — used when Kind == "meta_capi".
	PixelID       string `yaml:"pixel_id,omitempty"`
	AccessToken   string `yaml:"access_token,omitempty"`
	TestEventCode string `yaml:"test_event_code,omitempty"`

	// GA4 MP fields — used when Kind == "ga4_mp".
	MeasurementID string `yaml:"measurement_id,omitempty"`
	APISecret     string `yaml:"api_secret,omitempty"`

	// Endpoint override — used by tests to point at an
	// http-recorder sidecar instead of the real ad platform. Not
	// expected to be set in production configs.
	Endpoint string `yaml:"endpoint,omitempty"`
}
