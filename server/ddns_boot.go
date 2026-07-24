package server

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/ddns"
)

// ddnsRecordPlan is one resolved (record name × source) target. The per-cycle
// IP is filled in at runtime; everything else is resolved once at boot.
type ddnsRecordPlan struct {
	name      string // FQDN record name
	host      string // owning vhost host / proxy by_domain (for logging)
	apiToken  string
	zoneID    string // "" ⇒ provider auto-resolves
	proxied   bool
	ttl       int
	publishV4 bool
	publishV6 bool
}

// ddnsUpdater owns the DDNS polling loop: detect the public IP(s), then upsert
// only the records whose IP changed since the last successful publish. Every
// operation is non-fatal — failures warn and retry next cycle.
type ddnsUpdater struct {
	provider   ddns.Provider
	plans      []ddnsRecordPlan
	v4Services []string
	v6Services []string
	httpClient *http.Client
	interval   time.Duration
	needV4     bool
	needV6     bool

	mu     sync.Mutex
	lastIP map[string]string // key: name + "|" + type → last published IP
}

// startDDNS builds the updater from cfg and, if any records are configured,
// launches the polling loop plus a SIGHUP → immediate-check bridge. It returns
// immediately; all work happens in background goroutines bound to ctx. No
// existing SIGHUP handler was found in the boot path, so this registers its own
// notify scoped to the DDNS subsystem (SIGINT/SIGTERM shutdown is untouched).
func startDDNS(ctx context.Context, cfg *config.Config) {
	u := buildDDNSUpdater(cfg)
	if u == nil {
		return
	}

	// SIGHUP forces an immediate check. Bridge it to a generic trigger channel
	// so the loop stays signal-agnostic (and unit-testable with a plain chan).
	trigger := make(chan struct{}, 1)
	sighupCh := make(chan os.Signal, 1)
	signal.Notify(sighupCh, syscall.SIGHUP)
	go func() {
		defer signal.Stop(sighupCh)
		for {
			select {
			case <-ctx.Done():
				return
			case <-sighupCh:
				log.Infof("ddns: SIGHUP received — forcing immediate update")
				select {
				case trigger <- struct{}{}:
				default: // a check is already pending; coalesce
				}
			}
		}
	}()

	go u.run(ctx, trigger)
	log.Infof("ddns: updater started (%d record target(s), interval %s)", len(u.plans), u.interval)
}

// buildDDNSUpdater resolves every DDNS-enabled server + proxy into record plans.
// Returns nil when nothing is configured or no target survives credential
// resolution.
func buildDDNSUpdater(cfg *config.Config) *ddnsUpdater {
	if cfg == nil {
		return nil
	}

	// Any DDNS blocks at all?
	hasDDNS := false
	for _, s := range cfg.Servers {
		if s != nil && s.DDNS != nil {
			hasDDNS = true
			break
		}
	}
	if !hasDDNS {
		for _, p := range cfg.Proxies {
			if p != nil && p.DDNS != nil {
				hasDDNS = true
				break
			}
		}
	}
	if !hasDDNS {
		return nil
	}

	provider, err := ddns.NewProvider(cfg.DDNS.ResolveProvider())
	if err != nil {
		// Should not happen (validated at load); stay non-fatal regardless.
		log.Warnf("ddns: %s — dynamic DNS disabled", err)
		return nil
	}

	var plans []ddnsRecordPlan

	for _, s := range cfg.Servers {
		if s == nil || s.DDNS == nil {
			continue
		}
		originToken, originZone := serverOriginCACreds(s)
		token := s.DDNS.ResolveCFAPIToken(s.ID, cfg.DDNS, originToken)
		if token == "" {
			log.Warnf("ddns: server %q (id=%q): no Cloudflare API token resolved — skipping (set servers[].ddns.cf_api_token, env CF_API_TOKEN_%s, ddns.cf_api_token, env CF_API_TOKEN, or ssl.cloudflare_origin_ca)", s.Host, s.ID, config.DDNSEnvID(s.ID))
			continue
		}
		zone := s.DDNS.ResolveCFZoneID(s.ID, cfg.DDNS, originZone)
		proxied := s.DDNS.ResolveCFProxied(cfg.DDNS)
		if !proxied {
			log.Warnf("ddns: WARNING server %q has cf_proxied=false — its A/AAAA records will be DNS-only, exposing the origin IP", s.Host)
		}
		plans = append(plans, expandPlans(s.DDNS.Records, s.Host, token, zone, proxied,
			s.DDNS.ResolveTTL(cfg.DDNS), s.DDNS.ResolveIPv4(cfg.DDNS), s.DDNS.ResolveIPv6(cfg.DDNS))...)
	}

	for _, p := range cfg.Proxies {
		if p == nil || p.DDNS == nil {
			continue
		}
		// Proxies have no id ⇒ no id-suffixed env var and no origin-CA fallback.
		token := p.DDNS.ResolveCFAPIToken("", cfg.DDNS, "")
		if token == "" {
			log.Warnf("ddns: proxy %q: no Cloudflare API token resolved — skipping (set proxies[].ddns.cf_api_token, ddns.cf_api_token, or env CF_API_TOKEN)", p.ByDomain)
			continue
		}
		zone := p.DDNS.ResolveCFZoneID("", cfg.DDNS, "")
		proxied := p.DDNS.ResolveCFProxied(cfg.DDNS)
		if !proxied {
			log.Warnf("ddns: WARNING proxy %q has cf_proxied=false — its A/AAAA records will be DNS-only, exposing the origin IP", p.ByDomain)
		}
		plans = append(plans, expandPlans(p.DDNS.Records, p.ByDomain, token, zone, proxied,
			p.DDNS.ResolveTTL(cfg.DDNS), p.DDNS.ResolveIPv4(cfg.DDNS), p.DDNS.ResolveIPv6(cfg.DDNS))...)
	}

	if len(plans) == 0 {
		return nil
	}

	needV4, needV6 := false, false
	for _, pl := range plans {
		needV4 = needV4 || pl.publishV4
		needV6 = needV6 || pl.publishV6
	}

	var v4Services, v6Services []string
	if cfg.DDNS != nil {
		v4Services = cfg.DDNS.IPv4Services
		v6Services = cfg.DDNS.IPv6Services
	}

	return &ddnsUpdater{
		provider:   provider,
		plans:      plans,
		v4Services: v4Services,
		v6Services: v6Services,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		interval:   cfg.DDNS.ResolveInterval(),
		needV4:     needV4,
		needV6:     needV6,
		lastIP:     make(map[string]string),
	}
}

// expandPlans turns a source's (records|default host) into per-name plans.
func expandPlans(records []string, defaultName, token, zone string, proxied bool, ttl int, v4, v6 bool) []ddnsRecordPlan {
	names := records
	if len(names) == 0 {
		names = []string{defaultName}
	}
	out := make([]ddnsRecordPlan, 0, len(names))
	for _, n := range names {
		if n == "" {
			continue
		}
		out = append(out, ddnsRecordPlan{
			name:      n,
			host:      defaultName,
			apiToken:  token,
			zoneID:    zone,
			proxied:   proxied,
			ttl:       ttl,
			publishV4: v4,
			publishV6: v6,
		})
	}
	return out
}

// serverOriginCACreds returns the server's already-resolved Cloudflare Origin CA
// token + zone id (populated during LoadConfig), or empties when origin-CA isn't
// configured for that server.
func serverOriginCACreds(s *config.Server) (token, zone string) {
	if s != nil && s.SSL != nil && s.SSL.CloudflareOriginCA != nil {
		return s.SSL.CloudflareOriginCA.APIToken, s.SSL.CloudflareOriginCA.ZoneID
	}
	return "", ""
}

// run does the mandatory boot-time check, then loops on the interval ticker,
// ctx cancellation, and the trigger channel (fed by SIGHUP). Respects ctx for
// clean shutdown.
func (u *ddnsUpdater) run(ctx context.Context, trigger <-chan struct{}) {
	u.runOnce(ctx) // always check on startup, before the first tick
	ticker := time.NewTicker(u.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Infof("ddns: updater stopping (context cancelled)")
			return
		case <-ticker.C:
			u.runOnce(ctx)
		case <-trigger:
			u.runOnce(ctx)
		}
	}
}

// runOnce detects the needed public IP families and upserts every plan whose IP
// changed. All failures are warned and retried next cycle.
func (u *ddnsUpdater) runOnce(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	var v4, v6 string
	if u.needV4 {
		if ip, err := ddns.DetectPublicIPv4(ctx, u.httpClient, u.v4Services); err != nil {
			log.Warnf("ddns: IPv4 detection failed: %s", err)
		} else {
			v4 = ip
		}
	}
	if u.needV6 {
		if ip, err := ddns.DetectPublicIPv6(ctx, u.httpClient, u.v6Services); err != nil {
			log.Warnf("ddns: IPv6 detection failed: %s", err)
		} else {
			v6 = ip
		}
	}

	for _, pl := range u.plans {
		if ctx.Err() != nil {
			return
		}
		if pl.publishV4 && v4 != "" {
			u.ensure(ctx, pl, "A", v4)
		}
		if pl.publishV6 && v6 != "" {
			u.ensure(ctx, pl, "AAAA", v6)
		}
	}
}

// ensure upserts one (record,type) — but only when the detected IP differs from
// the last successfully published value, so a steady IP never hits Cloudflare.
func (u *ddnsUpdater) ensure(ctx context.Context, pl ddnsRecordPlan, typ, ip string) {
	key := pl.name + "|" + typ
	u.mu.Lock()
	last, seen := u.lastIP[key]
	u.mu.Unlock()
	if seen && last == ip {
		return // unchanged since last publish — skip Cloudflare entirely
	}

	err := u.provider.EnsureRecord(ctx, ddns.Record{
		Name:     pl.name,
		Type:     typ,
		Content:  ip,
		Proxied:  pl.proxied,
		TTL:      pl.ttl,
		ZoneID:   pl.zoneID,
		APIToken: pl.apiToken,
	})
	if err != nil {
		log.Warnf("ddns: upsert %s %s → %s failed: %s", typ, pl.name, ip, err)
		return // don't cache; retry next cycle
	}
	u.mu.Lock()
	u.lastIP[key] = ip
	u.mu.Unlock()
	log.Infof("ddns: published %s %s → %s (proxied=%t ttl=%d)", typ, pl.name, ip, pl.proxied, pl.ttl)
}
