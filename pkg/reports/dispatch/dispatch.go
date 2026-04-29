// Package dispatch runs the Phase-3 scheduled-report loop: load
// enabled reports from BoltDB, compute next_fire_at per cron +
// timezone, tick every minute, and fire the next-due reports via
// the renderer + mailer.
//
// Retries: exponential back-off on transient SMTP errors — 1m / 5m
// / 25m, capped at 3 attempts. Each attempt writes a ReportRun row
// so the admin UI's "Last runs" view has an audit trail.

package dispatch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/mailer"
	"github.com/tlalocweb/hulation/pkg/reports/render"
	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"
	"github.com/tlalocweb/hulation/pkg/store/storage"
)

// dispatchLog is the tagged logger for every line from this
// package. Keeps grep-able output in production.
var dispatchLog = log.GetTaggedLogger("reports-dispatch", "Scheduled-report dispatcher")

// cronParser — standard five-field cron ("min hr dom mon dow").
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// Dispatcher holds the runtime state. One per process.
type Dispatcher struct {
	mu       sync.Mutex
	m        *mailer.Mailer
	enqueued chan string // report IDs to fire ASAP (SendNow queue)
	stopCh   chan struct{}
	stopped  bool
}

// Global handle so ReportsService.SendNow can enqueue without a full
// DI chain. Set by Start; nil before.
var (
	globalMu sync.RWMutex
	global   *Dispatcher
)

// Start launches the dispatcher goroutine. Safe to call once per
// process; subsequent calls are no-ops. Returns a stop func for
// graceful shutdown.
func Start(ctx context.Context, m *mailer.Mailer) *Dispatcher {
	globalMu.Lock()
	if global != nil {
		globalMu.Unlock()
		return global
	}
	d := &Dispatcher{
		m:        m,
		enqueued: make(chan string, 32),
		stopCh:   make(chan struct{}),
	}
	global = d
	globalMu.Unlock()

	go d.loop(ctx)
	dispatchLog.Infof("dispatcher started")
	return d
}

// Get returns the global dispatcher, or nil when Start hasn't run.
func Get() *Dispatcher {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return global
}

// Stop signals the loop to exit. Idempotent.
func (d *Dispatcher) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return
	}
	d.stopped = true
	close(d.stopCh)
}

// Enqueue schedules an immediate render+send for the given report.
// Returns a new run_id that the caller can surface to ReportsService.SendNow.
func (d *Dispatcher) Enqueue(reportID string) (string, error) {
	if d == nil {
		return "", fmt.Errorf("dispatcher not started")
	}
	select {
	case d.enqueued <- reportID:
		return newRunID(), nil
	default:
		return "", fmt.Errorf("dispatcher queue full")
	}
}

// loop is the goroutine body. One minute ticker; runs due reports
// plus anything in the SendNow queue.
func (d *Dispatcher) loop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	// Fire once on startup so due-at-boot reports don't wait a
	// minute.
	d.runDueReports()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stopCh:
			return
		case <-t.C:
			d.runDueReports()
		case id := <-d.enqueued:
			d.sendOne(id, /*force=*/ true)
		}
	}
}

func (d *Dispatcher) runDueReports() {
	s := storage.Global()
	if s == nil {
		return
	}
	ctx := context.Background()
	reports, err := hulabolt.ListReports(ctx, s, "")
	if err != nil {
		dispatchLog.Warnf("list reports: %s", err)
		return
	}
	now := time.Now().UTC()
	for _, r := range reports {
		if !r.Enabled {
			continue
		}
		if r.NextFireAt.IsZero() || r.NextFireAt.After(now) {
			continue
		}
		d.sendOne(r.ID, /*force=*/ false)
	}
}

func (d *Dispatcher) sendOne(reportID string, force bool) {
	s := storage.Global()
	if s == nil {
		return
	}
	ctx := context.Background()
	r, err := hulabolt.GetReport(ctx, s, reportID)
	if err != nil || r == nil {
		dispatchLog.Warnf("sendOne: missing report %s: %v", reportID, err)
		return
	}
	variant := render.VariantSummary
	if r.TemplateVariant == "detailed" {
		variant = render.VariantDetailed
	}
	now := time.Now().UTC()
	in := render.SummaryInput{
		ReportName:                r.Name,
		ServerID:                  r.ServerID,
		From:                      now.Add(-7 * 24 * time.Hour),
		To:                        now,
		TimezoneLabel:             r.Timezone,
		Visitors:                  0, // TODO: wire analytics summary query
		Pageviews:                 0,
		BounceRate:                0,
		AvgSessionDurationSeconds: 0,
	}
	html, subject, rerr := render.Render(variant, in)
	if rerr != nil {
		writeRun(r.ID, "failed", 1, rerr.Error(), r.Recipients, now)
		dispatchLog.Warnf("render %s: %s", reportID, rerr)
		d.advanceNextFire(r)
		return
	}

	// Attempt send with exponential back-off retry.
	attempt := int32(1)
	delays := []time.Duration{0, time.Minute, 5 * time.Minute, 25 * time.Minute}
	var lastErr error
	for ; attempt <= int32(len(delays)); attempt++ {
		if attempt > 1 {
			time.Sleep(delays[attempt-1])
		}
		if d.m == nil {
			writeRun(r.ID, "failed", attempt, "mailer not configured", r.Recipients, now)
			lastErr = mailer.ErrNotConfigured
			break
		}
		sendErr := d.m.Send(context.Background(), mailer.Message{
			To:      r.Recipients,
			Subject: subject,
			HTML:    html,
		})
		if sendErr == nil {
			writeRun(r.ID, "success", attempt, "", r.Recipients, now)
			dispatchLog.Infof("sent report %s (%q) to %v", r.ID, r.Name, r.Recipients)
			lastErr = nil
			break
		}
		lastErr = sendErr
		writeRun(r.ID, "retrying", attempt, sendErr.Error(), r.Recipients, now)
		dispatchLog.Warnf("send %s attempt %d failed: %s", reportID, attempt, sendErr)
	}
	if lastErr != nil && attempt > int32(len(delays)) {
		writeRun(r.ID, "failed", attempt-1, lastErr.Error(), r.Recipients, now)
	}
	d.advanceNextFire(r)
}

// advanceNextFire recomputes the next fire time from the cron+tz
// and persists it so the next minute-tick skips this report until
// the bucket is due again.
func (d *Dispatcher) advanceNextFire(r *hulabolt.StoredReport) {
	tz, err := time.LoadLocation(r.Timezone)
	if err != nil {
		dispatchLog.Warnf("invalid timezone %q for report %s: %s", r.Timezone, r.ID, err)
		tz = time.UTC
	}
	sched, err := cronParser.Parse(r.Cron)
	if err != nil {
		dispatchLog.Warnf("invalid cron %q for report %s: %s", r.Cron, r.ID, err)
		return
	}
	now := time.Now().In(tz)
	r.NextFireAt = sched.Next(now).UTC()
	if s := storage.Global(); s != nil {
		if _, err := hulabolt.PutReport(context.Background(), s, *r); err != nil {
			dispatchLog.Warnf("persist next_fire_at for %s: %s", r.ID, err)
		}
	}
}

func writeRun(reportID, status string, attempt int32, errText string, recipients []string, startedAt time.Time) {
	run := hulabolt.StoredReportRun{
		ID:         newRunID(),
		ReportID:   reportID,
		StartedAt:  startedAt,
		FinishedAt: time.Now().UTC(),
		Status:     status,
		Attempt:    attempt,
		Error:      errText,
		Recipients: append([]string(nil), recipients...),
	}
	if s := storage.Global(); s != nil {
		if err := hulabolt.AppendReportRun(context.Background(), s, run); err != nil {
			dispatchLog.Warnf("append run: %s", err)
		}
	}
}

func newRunID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
