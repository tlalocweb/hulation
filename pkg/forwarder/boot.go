package forwarder

// Boot helpers — build per-server Composites and Workers from
// config.ForwarderConfig entries, wire them into the per-server
// registry, and start the drain goroutines.
//
// Called once at startup from server.RunUnified. Subsequent
// per-server config reloads can re-call BuildAndRegisterForServer
// to swap adapters at runtime — old worker is stopped, new one
// takes over.

import (
	"fmt"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
)

// eventCodeFromSymbol maps the YAML event-name symbols ops type to
// the model.EventCode* constants used internally.
var eventCodeFromSymbol = map[string]uint64{
	"page_view":     model.EventCodePageView,
	"click":         model.EventCodeClick,
	"form_start":    model.EventCodeStartForm,
	"form_submit":   model.EventCodeFormSubmission,
	"referred":      model.EventCodeReferredFromKnown,
	"scrolled":      model.EventCodeScrolledIntoView,
	"lander_hit":    model.EventCodeLanderHit,
}

// translateEventMap converts the YAML map (symbol→adapter-name) into
// the (eventCode → adapter-name) map the adapters expect. Symbols
// not in the table are ignored with a warn log.
func translateEventMap(serverID, kind string, m map[string]string) map[uint64]string {
	out := make(map[uint64]string, len(m))
	for sym, name := range m {
		code, ok := eventCodeFromSymbol[sym]
		if !ok {
			log.Warnf("forwarder %s/%s: unknown event symbol %q (skipping)", serverID, kind, sym)
			continue
		}
		out[code] = name
	}
	return out
}

// BuildAdapter constructs a single Adapter from a ForwarderConfig.
// Returns an error when the config is malformed.
func BuildAdapter(serverID string, fc *config.ForwarderConfig) (Adapter, error) {
	if fc == nil {
		return nil, fmt.Errorf("nil ForwarderConfig")
	}
	switch fc.Kind {
	case "meta_capi":
		if fc.PixelID == "" || fc.AccessToken == "" {
			return nil, fmt.Errorf("meta_capi: pixel_id and access_token required")
		}
		em := translateEventMap(serverID, fc.Kind, fc.EventMap)
		return NewMetaCAPI(fc.PixelID, fc.AccessToken, fc.TestEventCode, em), nil
	case "ga4_mp":
		if fc.MeasurementID == "" || fc.APISecret == "" {
			return nil, fmt.Errorf("ga4_mp: measurement_id and api_secret required")
		}
		em := translateEventMap(serverID, fc.Kind, fc.EventMap)
		ad := NewGA4MP(fc.MeasurementID, fc.APISecret, em)
		if fc.Endpoint != "" {
			ad.Endpoint = fc.Endpoint
		}
		return ad, nil
	default:
		return nil, fmt.Errorf("unknown forwarder kind %q", fc.Kind)
	}
}

// BuildAndRegisterForServer assembles a Composite + Worker for the
// given server config and installs them in the per-server registry.
// Returns false if the server has no forwarders configured.
func BuildAndRegisterForServer(srv *config.Server) bool {
	if srv == nil || len(srv.Forwarders) == 0 {
		return false
	}
	composite := NewComposite()
	queueSize := DefaultQueueSize
	for _, fc := range srv.Forwarders {
		ad, err := BuildAdapter(srv.ID, fc)
		if err != nil {
			log.Errorf("forwarder %s/%s: build failed: %s (skipping)", srv.ID, fc.Kind, err.Error())
			continue
		}
		composite.Add(ad)
		if fc.QueueSize > queueSize {
			queueSize = fc.QueueSize
		}
		log.Infof("forwarder registered: server=%s kind=%s purpose=%s", srv.ID, ad.Name(), ad.Purpose())
	}
	if len(composite.Adapters()) == 0 {
		return false
	}
	SetForServer(srv.ID, composite)
	w := NewWorker(composite, queueSize)
	SetWorkerForServer(srv.ID, w)
	return true
}

// BuildAndRegisterAll wires forwarders for every server with a
// non-empty Forwarders list. Returns the count registered.
func BuildAndRegisterAll(servers []*config.Server) int {
	n := 0
	for _, s := range servers {
		if BuildAndRegisterForServer(s) {
			n++
		}
	}
	return n
}
