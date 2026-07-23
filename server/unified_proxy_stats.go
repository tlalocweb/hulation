package server

// Server-side analytics for `proxy_only` virtual hosts (reverse-proxy parity
// PR-3). A proxy_only host forwards every request to an external upstream that
// does NOT carry hula's client beacon (`/v/*`), so those hosts would otherwise
// record zero analytics. This file derives a pageview straight from the raw
// HTTP request at the proxyDispatch seam and records it, with NO client JS.
//
// Design constraints (all enforced here):
//
//   - Never blocks or slows the proxied response. The page-nav FILTER
//     (evalPageView) runs inline and is cheap; only navigations that pass it
//     are handed to a bounded async pipeline whose worker goroutines do the DB
//     write. A full queue DROPS (and counts) rather than blocking — a slow DB
//     must never back up the reverse proxy.
//   - No side effect on the forwarded response. recordProxyPageview reads a
//     PRISTINE *http.Request (httputil.ReverseProxy clones before its Director
//     mutates, so `r` is untouched at the seam) and never writes to `w`, sets
//     no cookies, and does not consume the body.
//   - Cookieless identity ONLY. A proxied external app can't reliably
//     round-trip hula's analytics cookie, so the visitor id is derived from the
//     request alone via the daily-rotating cookieless HMAC — regardless of the
//     site's configured tracking_mode. This is a deliberate, documented
//     limitation: cross-day stitching is impossible and there is no
//     bounce/engagement signal (see the honest-degradation notes below).
//   - Tagged distinctly. The event method is "serverside" so these rows can
//     never be confused with, or double-counted against, beacon hits
//     ("hellonoscript" / client JS).
//
// Honest degradation vs a JS beacon hit:
//   - Visitor id is cookieless + daily-rotating (no cross-day identity).
//   - Only the INITIAL navigation URL is seen (no SPA route changes, no
//     scroll/engagement/bounce, no click or form events).
//   - Geo is limited to CF-IPCountry (when the edge supplies it and RemoteAddr
//     is a verified Cloudflare IP); region/city/ASN/ISP/Org are left empty
//     (no IPInfo hook is wired on this path).

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"
	"github.com/tlalocweb/hulation/pkg/store/storage"
	"github.com/tlalocweb/hulation/pkg/visitorid"
)

const (
	// serverSideStatsMethod tags the Event.Method for a pageview derived from
	// the raw proxied request. Distinct from the beacon methods
	// ("hellonoscript" / client JS) so server-side rows are never mistaken for,
	// or double-counted against, beacon hits.
	serverSideStatsMethod = "serverside"

	// statsQueueSize is the bounded async buffer. At full it drops (never
	// blocks) — the reverse proxy must not stall on a slow DB write.
	statsQueueSize = 1024

	// statsWorkers drains the queue with a small fixed pool.
	statsWorkers = 2
)

// assetExts are path extensions we treat as non-navigations and skip cheaply.
// Keyed for O(1) lookup.
var assetExts = map[string]struct{}{
	".js": {}, ".mjs": {}, ".css": {}, ".map": {},
	".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {}, ".svg": {}, ".ico": {},
	".webp": {}, ".avif": {}, ".bmp": {},
	".woff": {}, ".woff2": {}, ".ttf": {}, ".eot": {}, ".otf": {},
	".json": {}, ".txt": {}, ".xml": {}, ".pdf": {},
	".mp4": {}, ".webm": {}, ".mp3": {}, ".wav": {}, ".ogg": {},
	".wasm": {}, ".zip": {}, ".gz": {},
}

// statsPageView is the request-derived pageview carried through the async
// pipeline. It holds only cheap request-scoped values; the expensive work
// (salt fetch, visitor-id derivation, DB write) happens in the worker.
type statsPageView struct {
	serverID string // resolved hula server_id (from host config)
	ownHost  string // hconf.Host — lets enrichment classify self-referrals as Direct
	url      string // reconstructed absolute URL (scheme://host/path?query)
	urlPath  string // just the path
	host     string // port-stripped, lowercased Host
	referer  string // Referer header (usually empty for direct navigations)
	ua       string // User-Agent
	ip       string // client IP (CF-aware, via extractIPFromRequest)
	country  string // 2-letter ISO from CF-IPCountry when trusted; else ""
	method   string // always serverSideStatsMethod
}

// --- Page-navigation filter (PURE) -----------------------------------------

// evalPageView decides whether r is a page navigation worth recording and, if
// so, extracts the request-only event fields. PURE: reads r only (no config,
// storage, DB, or globals) so it is trivially unit-testable in-memory. The
// non-request-scoped fields (ip, country, serverID, ownHost) are filled by
// recordProxyPageview.
//
// A request is recorded only when ALL hold:
//   - method is GET (a navigation, not an API mutation);
//   - it is NOT a WebSocket upgrade (Connection: Upgrade / Upgrade: websocket);
//   - the path is not an obvious asset (.js/.css/.png/...); and
//   - the Accept header contains text/html, OR Accept is empty on a non-asset
//     path (a bare `curl`/prefetch of a page still counts; an XHR that sends
//     `Accept: application/json` does not).
func evalPageView(r *http.Request) (statsPageView, bool) {
	if r.Method != http.MethodGet {
		return statsPageView{}, false
	}
	if isWebSocketUpgrade(r) {
		return statsPageView{}, false
	}
	if hasAssetExtension(r.URL.Path) {
		return statsPageView{}, false
	}
	if !acceptsHTML(r.Header.Get("Accept")) {
		return statsPageView{}, false
	}
	return statsPageView{
		host:    hostOnly(r.Host),
		url:     requestScheme(r) + "://" + r.Host + r.URL.RequestURI(),
		urlPath: r.URL.Path,
		referer: r.Header.Get("Referer"),
		ua:      r.Header.Get("User-Agent"),
		method:  serverSideStatsMethod,
	}, true
}

// isWebSocketUpgrade reports whether r is a protocol-upgrade handshake (most
// commonly a WebSocket), which is a long-lived connection, not a pageview.
func isWebSocketUpgrade(r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		return true
	}
	// Connection may be a comma-separated token list (e.g. "keep-alive, Upgrade").
	for _, tok := range strings.Split(r.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
			return true
		}
	}
	return false
}

// hasAssetExtension reports whether the last path segment ends in a known
// static-asset extension.
func hasAssetExtension(path string) bool {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		path = path[i+1:]
	}
	dot := strings.LastIndexByte(path, '.')
	if dot < 0 {
		return false
	}
	_, ok := assetExts[strings.ToLower(path[dot:])]
	return ok
}

// acceptsHTML implements the Accept rule: an explicit text/html, or an empty
// Accept (the caller has already excluded asset paths).
func acceptsHTML(accept string) bool {
	if accept == "" {
		return true
	}
	return strings.Contains(accept, "text/html")
}

// requestScheme derives the client-facing scheme, honouring an upstream
// X-Forwarded-Proto (hula may sit behind a CDN/front proxy) then falling back
// to the connection's TLS state.
func requestScheme(r *http.Request) string {
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		if i := strings.IndexByte(proto, ','); i >= 0 {
			proto = proto[:i]
		}
		return strings.TrimSpace(proto)
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// extractCFCountry returns the CF-IPCountry code only when it can be trusted —
// i.e. RemoteAddr is a verified Cloudflare edge IP — mirroring
// handler.NetHTTPCtx.Country so a direct client can't spoof its country.
func extractCFCountry(r *http.Request) string {
	cc := strings.TrimSpace(r.Header.Get("CF-IPCountry"))
	if cc == "" {
		return ""
	}
	cfRanges := app.GetConfig().GetCloudflareIPs()
	if cfRanges == nil || !cfRanges.ContainsString(r.RemoteAddr) {
		return ""
	}
	return cc
}

// --- Site resolution -------------------------------------------------------

// siteResolver maps a request host to the owning hula server. Injected so the
// recorder is testable without a fully-loaded config.
type siteResolver func(host string) (serverID, ownHost string, ok bool)

// configSiteResolver is the production resolver: a single O(1) alias-map
// lookup on the global config.
func configSiteResolver(host string) (serverID, ownHost string, ok bool) {
	cfg := config.GetConfig()
	if cfg == nil {
		return "", "", false
	}
	hconf := cfg.GetServerByAnyAlias(host)
	if hconf == nil {
		return "", "", false
	}
	return hconf.ID, hconf.Host, true
}

// --- Async pipeline --------------------------------------------------------

// statsSink consumes a recorded pageview. Abstracted so tests can capture
// pageviews in-memory without ClickHouse. Production uses dbStatsSink.
type statsSink interface {
	write(pv statsPageView)
}

// statsPipeline is a bounded, non-blocking async queue drained by a fixed
// worker pool. Enqueue is O(1) and never blocks; a full queue drops + counts.
type statsPipeline struct {
	queue  chan statsPageView
	sink   statsSink
	stopCh chan struct{}
	wg     sync.WaitGroup

	enqueued atomic.Uint64
	dropped  atomic.Uint64
}

func newStatsPipeline(sink statsSink, queueSize int) *statsPipeline {
	if queueSize <= 0 {
		queueSize = statsQueueSize
	}
	return &statsPipeline{
		queue:  make(chan statsPageView, queueSize),
		sink:   sink,
		stopCh: make(chan struct{}),
	}
}

// start launches the drain workers. workers<1 is clamped to 1.
func (p *statsPipeline) start(workers int) {
	if workers < 1 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go p.run()
	}
}

func (p *statsPipeline) run() {
	defer p.wg.Done()
	for {
		select {
		case <-p.stopCh:
			return
		case pv := <-p.queue:
			p.sink.write(pv)
		}
	}
}

// enqueue offers a pageview to the queue. Non-blocking: returns true on accept,
// false on drop (queue full). A slow sink must never stall the reverse proxy.
func (p *statsPipeline) enqueue(pv statsPageView) bool {
	select {
	case p.queue <- pv:
		p.enqueued.Add(1)
		return true
	default:
		n := p.dropped.Add(1)
		// Log the first drop and then sparsely, so a sustained overload can't
		// itself become a log flood.
		if n == 1 || n%1024 == 0 {
			log.Warnf("serverside stats: queue full, dropped %d pageview(s) (buffer=%d)", n, cap(p.queue))
		}
		return false
	}
}

// stop signals the workers to exit and waits for them. Pending queued events
// are discarded. Provided mainly for test hygiene — in production the pipeline
// is a process-lifetime daemon (the unified server has no per-subsystem
// shutdown hook today, matching the forwarder workers).
func (p *statsPipeline) stop() {
	close(p.stopCh)
	p.wg.Wait()
}

// --- Production sink: cookieless derive + DB write -------------------------

// dbStatsSink writes a pageview to the events store. Nil-safe: a no-op when the
// DB isn't wired (e.g. infra-free tests). All of its work — salt fetch,
// visitor-id derivation, enrichment, the CommitTo insert — runs on the worker
// goroutine, off the reverse-proxy hot path.
type dbStatsSink struct{}

func (dbStatsSink) write(pv statsPageView) {
	if model.GetDB() == nil || model.GetSQLDB() == nil {
		return // DB not wired — no-op
	}
	salt, ok := cookielessSaltForServer(pv.serverID)
	if !ok {
		return
	}
	vid, ok := deriveCookielessVisitorID(salt, pv.ip, pv.ua)
	if !ok {
		return
	}
	ev := model.NewEvent(model.EventCodePageView)
	ev.SetURL(pv.url)
	ev.SetUrlPath(pv.urlPath)
	ev.SetHost(pv.host)
	ev.SetMethod(pv.method)
	ev.SetBrowserUA(pv.ua)
	ev.SetFromIP(pv.ip)
	// UTM/channel derive from URL + referer exactly as the beacon path does.
	// region/city/asn/isp/org are unavailable on this path (no IPInfo hook).
	ev.ApplyEnrichment(vid, pv.ownHost, pv.serverID, pv.referer, pv.ua, pv.country, "", "", "", "", "")

	v := model.NewVisitor()
	v.ID = vid
	if err := ev.CommitTo(model.GetDB(), v); err != nil {
		log.Debugf("serverside stats: commit failed (server=%s host=%s): %v", pv.serverID, pv.host, err)
	}
}

// cookielessSaltForServer fetches (lazily creating) the per-server 32-byte
// cookieless salt from the global storage. Returns ok=false when storage is
// unavailable or the fetch fails.
func cookielessSaltForServer(serverID string) ([]byte, bool) {
	s := storage.Global()
	if s == nil {
		return nil, false
	}
	salt, err := hulabolt.GetOrCreateCookielessSalt(context.Background(), s, serverID)
	if err != nil {
		log.Debugf("serverside stats: salt fetch failed (server=%s): %v", serverID, err)
		return nil, false
	}
	return salt, true
}

// deriveCookielessVisitorID derives the daily-rotating, request-only visitor id
// — HMAC(salt, dayUTC‖IP‖UA) — the SAME identity handler.MintVisitor's
// cookieless path produces. Same (IP, UA, salt) within a UTC day → same id;
// next day → different id (the privacy property). No cookie is read or set.
func deriveCookielessVisitorID(salt []byte, ip, ua string) (string, bool) {
	id, err := visitorid.DeriveNow(salt, ip, ua)
	if err != nil {
		return "", false
	}
	return id, true
}

// --- Recorder wiring -------------------------------------------------------

// recordProxyPageview is the inline hook invoked at the proxyDispatch seam,
// BEFORE the request is forwarded. It runs the cheap page-nav filter, resolves
// the owning site, fills the request-scoped fields, and does a single
// non-blocking enqueue. It NEVER touches the ResponseWriter. Everything past
// the enqueue happens on a worker goroutine.
func recordProxyPageview(p *statsPipeline, resolve siteResolver, r *http.Request) {
	if p == nil {
		return
	}
	pv, ok := evalPageView(r)
	if !ok {
		return // not a page navigation — skipped cheaply
	}
	serverID, ownHost, ok := resolve(pv.host)
	if !ok {
		return // unknown host — skip silently
	}
	pv.serverID = serverID
	pv.ownHost = ownHost
	pv.ip = extractIPFromRequest(r)
	pv.country = extractCFCountry(r)
	p.enqueue(pv)
}

// defaultStatsPipeline is the process-wide pipeline, started once on first use.
var (
	statsPipelineMu      sync.Mutex
	defaultStatsPipeline *statsPipeline
)

// ensureStatsPipeline lazily constructs and starts the process-wide pipeline
// with the production DB sink.
func ensureStatsPipeline() *statsPipeline {
	statsPipelineMu.Lock()
	defer statsPipelineMu.Unlock()
	if defaultStatsPipeline == nil {
		defaultStatsPipeline = newStatsPipeline(dbStatsSink{}, statsQueueSize)
		defaultStatsPipeline.start(statsWorkers)
	}
	return defaultStatsPipeline
}

// newProxyStatsRecorder returns the func(*http.Request) injected into
// proxyDispatch (mirroring blockCheck). It captures the process-wide pipeline
// and the production site resolver. Returns nil-safe behaviour throughout.
func newProxyStatsRecorder() func(*http.Request) {
	p := ensureStatsPipeline()
	return func(r *http.Request) {
		recordProxyPageview(p, configSiteResolver, r)
	}
}
