package badactor

import (
	"strings"
	"time"

	"github.com/tlalocweb/hulation/handler"
)

// init registers the ipinfo lookup hook with the handler package so
// handler/visitor.go can enrich events with geo + ASN info without
// creating a handler → badactor import cycle.
//
// The hook is invoked exclusively from the BounceMap writer
// goroutine (handler.enrichEventFromBounce → ApplyEnrichment), which
// runs OFF the request hot path — the visitor's HTTP response has
// already gone out by the time we get here. That means we can
// afford to do a synchronous lookup on cache miss instead of
// returning blanks and praying the next event for this IP arrives
// after the async lookup completes.
//
// Trade-off: a fresh-IP miss now blocks the bounce writer for up to
// the ip-api.com client timeout (5s worst case; ~100-300ms typical).
// The writer is single-threaded, so events queued behind a slow
// lookup wait their turn. Acceptable for the analytics-write path
// since the browser never sees this latency.
func init() {
	handler.IPInfoHook = func(ip string) (countryCode, region, city, asn, isp, org string) {
		info := LookupIPInfoSync(ip)
		if info == nil {
			return "", "", "", "", "", ""
		}
		// ip-api.com puts the AS number + Org in one "AS" field
		// (e.g. "AS16509 Amazon.com, Inc."). Split on the first
		// space so we can show the bare ASN and a clean Org.
		bareASN := info.ASN
		if idx := strings.IndexByte(bareASN, ' '); idx > 0 {
			bareASN = bareASN[:idx]
		}
		return info.CountryCode, info.Region, info.City, bareASN, info.ISP, info.Org
	}
}

// BadActorListEntry is a single entry returned by the list API.
type BadActorListEntry struct {
	IP         string    `json:"ip"`
	Score      int       `json:"score"`
	DetectedAt time.Time `json:"detected_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	LastReason string    `json:"last_reason"`
	Blocked    bool      `json:"blocked"`
}

type manualBlockReq struct {
	IP     string `json:"ip"`
	Reason string `json:"reason"`
}

type allowlistReq struct {
	IP     string `json:"ip"`
	Reason string `json:"reason"`
}

func ListBadActors(ctx handler.RequestCtx) error {
	s := GetStore()
	if s == nil {
		return ctx.Status(503).SendString("bad actor feature not enabled")
	}
	entries := s.ListBlockedIPsWithDetail(100, 0)
	return ctx.SendJSON(entries)
}

func EvictBadActor(ctx handler.RequestCtx) error {
	s := GetStore()
	if s == nil {
		return ctx.Status(503).SendString("bad actor feature not enabled")
	}
	ip := ctx.Param("ip")
	if ip == "" {
		return ctx.Status(400).SendString("ip required")
	}
	s.EvictIPManual(ip)
	return ctx.Status(200).SendJSON(map[string]string{"evicted": ip})
}

func ManualBlock(ctx handler.RequestCtx) error {
	s := GetStore()
	if s == nil {
		return ctx.Status(503).SendString("bad actor feature not enabled")
	}
	var req manualBlockReq
	if err := ctx.BodyParser(&req); err != nil {
		return ctx.Status(400).SendString("bad parse: " + err.Error())
	}
	if req.IP == "" {
		return ctx.Status(400).SendString("ip required")
	}
	reason := req.Reason
	if reason == "" {
		reason = "manually blocked"
	}
	// Insert with score >= threshold so it's immediately blocked
	err := InsertBadActorRecord(s.db, req.IP, "", "", "", "", reason, "manual", "manual", s.blockThreshold)
	if err != nil {
		return ctx.Status(500).SendString("error recording: " + err.Error())
	}
	// Add to radix tree
	txn := s.tree.Load().Txn()
	txn.Insert([]byte(req.IP), BadActorEntry{
		Score:      s.blockThreshold,
		DetectedAt: time.Now(),
		ExpiresAt:  time.Now().Add(s.ttl),
		LastReason: reason,
	})
	s.tree.Store(txn.Commit())
	return ctx.Status(200).SendJSON(map[string]string{"blocked": req.IP})
}

func ListAllowlistHandler(ctx handler.RequestCtx) error {
	s := GetStore()
	if s == nil {
		return ctx.Status(503).SendString("bad actor feature not enabled")
	}
	ips, err := LoadAllowlist(s.db)
	if err != nil {
		return ctx.Status(500).SendString("error loading allowlist: " + err.Error())
	}
	return ctx.SendJSON(ips)
}

func AddToAllowlistHandler(ctx handler.RequestCtx) error {
	s := GetStore()
	if s == nil {
		return ctx.Status(503).SendString("bad actor feature not enabled")
	}
	var req allowlistReq
	if err := ctx.BodyParser(&req); err != nil {
		return ctx.Status(400).SendString("bad parse: " + err.Error())
	}
	if req.IP == "" {
		return ctx.Status(400).SendString("ip required")
	}
	err := AddToAllowlistDB(s.db, req.IP, req.Reason, "admin")
	if err != nil {
		return ctx.Status(500).SendString("error adding: " + err.Error())
	}
	s.AddToAllowlist(req.IP)
	// Also evict from bad actor list if present
	s.EvictIPManual(req.IP)
	return ctx.Status(200).SendJSON(map[string]string{"allowed": req.IP})
}

func RemoveFromAllowlistHandler(ctx handler.RequestCtx) error {
	s := GetStore()
	if s == nil {
		return ctx.Status(503).SendString("bad actor feature not enabled")
	}
	ip := ctx.Param("ip")
	if ip == "" {
		return ctx.Status(400).SendString("ip required")
	}
	err := RemoveFromAllowlistDB(s.db, ip)
	if err != nil {
		return ctx.Status(500).SendString("error removing: " + err.Error())
	}
	s.RemoveFromAllowlist(ip)
	return ctx.Status(200).SendJSON(map[string]string{"removed": ip})
}

func BadActorStats(ctx handler.RequestCtx) error {
	s := GetStore()
	if s == nil {
		return ctx.Status(503).SendString("bad actor feature not enabled")
	}
	return ctx.SendJSON(map[string]interface{}{
		"enabled":         !s.cfg.Disable,
		"dry_run":         s.cfg.DryRun,
		"block_threshold": s.blockThreshold,
		"ttl":             s.cfg.TTL,
		"blocked_ips":     s.GetBlockedCount(),
		"allowlisted_ips": s.GetAllowlistCount(),
		"signatures":      len(s.sigs.All),
	})
}

func ListSignaturesHandler(ctx handler.RequestCtx) error {
	s := GetStore()
	if s == nil {
		return ctx.Status(503).SendString("bad actor feature not enabled")
	}
	type sigInfo struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		Score    int    `json:"score"`
		Reason   string `json:"reason"`
		Category string `json:"category"`
	}
	var sigs []sigInfo
	for _, sig := range s.sigs.All {
		sigs = append(sigs, sigInfo{
			Name:     sig.Name,
			Type:     string(sig.Type),
			Score:    sig.Score,
			Reason:   sig.Reason,
			Category: sig.Category,
		})
	}
	return ctx.SendJSON(sigs)
}
