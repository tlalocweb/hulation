// Package evaluator runs the Phase-4 alert rule loop: load enabled
// alerts from BoltDB, tick every minute, evaluate each rule's
// kind-specific predicate against ClickHouse, and on fire write an
// AlertEvent row + hand a rendered email to pkg/mailer.
//
// Cooldown: if a rule fired within its CooldownMins window, the
// evaluator skips it this tick even if the predicate still evaluates
// true. Prevents alert storms on a sustained anomaly.
//
// Throttle: caps total fires per tick at maxFiresPerTick so a
// ClickHouse slowdown that makes many rules look "spike-y" at once
// can't overload the mailer.

package evaluator

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/mailer"
	"github.com/tlalocweb/hulation/pkg/mobile/tokenbox"
	"github.com/tlalocweb/hulation/pkg/notifier"
	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"
)

var evalLog = log.GetTaggedLogger("alerts-eval", "Alert rule evaluator")

// Tunables.
const (
	tickInterval     = 1 * time.Minute
	maxFiresPerTick  = 10
	defaultCooldown  = 60 * time.Minute // safety when CooldownMins is unset
)

// Evaluator holds the runtime state. One per process.
type Evaluator struct {
	mu     sync.Mutex
	m      *mailer.Mailer
	db     *sql.DB
	stopCh chan struct{}
}

// Global handle for introspection / stop from signal path.
var (
	globalMu sync.RWMutex
	global   *Evaluator
)

// Start launches the evaluator goroutine. Safe to call once per
// process; subsequent calls are no-ops.
func Start(ctx context.Context, m *mailer.Mailer, db *sql.DB) *Evaluator {
	globalMu.Lock()
	if global != nil {
		globalMu.Unlock()
		return global
	}
	e := &Evaluator{m: m, db: db, stopCh: make(chan struct{})}
	global = e
	globalMu.Unlock()

	go e.loop(ctx)
	evalLog.Infof("evaluator started")
	return e
}

// Stop stops the evaluator goroutine. Idempotent.
func Stop() {
	globalMu.Lock()
	defer globalMu.Unlock()
	if global == nil {
		return
	}
	close(global.stopCh)
	global = nil
}

func (e *Evaluator) loop(ctx context.Context) {
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	// Fire once at startup so operators don't have to wait a full tick
	// to see the evaluator working after a boot.
	e.runDue(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stopCh:
			return
		case <-t.C:
			e.runDue(ctx)
		}
	}
}

// runDue evaluates every enabled alert, fires the ones whose
// predicate holds + cooldown has elapsed, and caps total fires at
// maxFiresPerTick.
func (e *Evaluator) runDue(ctx context.Context) {
	alerts, err := hulabolt.ListAlerts("")
	if err != nil {
		// Bolt not opened — degrade silently. Admin ops get told at
		// boot when the store is unavailable, so no need to shout.
		return
	}
	fired := 0
	for _, a := range alerts {
		if fired >= maxFiresPerTick {
			evalLog.Warnf("fire cap reached (%d); throttling remaining alerts this tick", maxFiresPerTick)
			return
		}
		if !a.Enabled {
			continue
		}
		if e.inCooldown(a) {
			continue
		}
		obs, ok, err := e.evaluate(ctx, a)
		if err != nil {
			evalLog.Debugf("alert %s (%s): evaluate error: %s", a.ID, a.Kind, err)
			continue
		}
		if !ok {
			continue
		}
		e.fire(a, obs)
		fired++
	}
}

func (e *Evaluator) inCooldown(a hulabolt.StoredAlert) bool {
	if a.LastFiredAt.IsZero() {
		return false
	}
	cd := time.Duration(a.CooldownMins) * time.Minute
	if cd <= 0 {
		cd = defaultCooldown
	}
	return time.Since(a.LastFiredAt) < cd
}

// evaluate returns (observed_value, fire, err). Predicate is
// kind-specific; on success, the observed_value gets persisted on
// the AlertEvent row for the admin UI.
func (e *Evaluator) evaluate(ctx context.Context, a hulabolt.StoredAlert) (float64, bool, error) {
	if e.db == nil {
		return 0, false, fmt.Errorf("clickhouse not available")
	}
	w := time.Duration(a.WindowMinutes) * time.Minute
	if w <= 0 {
		w = 60 * time.Minute
	}
	since := time.Now().UTC().Add(-w)

	switch a.Kind {
	case "goal_count_above":
		// Counts events flagged by the ingest-path goal evaluator as
		// belonging to the target goal. is_goal / goal_id columns come
		// from the Phase-3 events enrichment.
		if a.TargetGoalID == "" {
			return 0, false, fmt.Errorf("target_goal_id required")
		}
		var n int64
		row := e.db.QueryRowContext(ctx,
			`SELECT count() FROM events WHERE server_id = ? AND when >= ? AND is_goal = 1 AND goal_id = ?`,
			a.ServerID, since, a.TargetGoalID)
		if err := row.Scan(&n); err != nil {
			return 0, false, err
		}
		return float64(n), float64(n) > a.Threshold, nil

	case "page_traffic_delta":
		// Compare current-window pageviews on TargetPath against the
		// same window one week ago. Fire when |delta_pct| > threshold.
		if a.TargetPath == "" {
			return 0, false, fmt.Errorf("target_path required")
		}
		current, err := e.countPageviews(ctx, a.ServerID, a.TargetPath, since, time.Now().UTC())
		if err != nil {
			return 0, false, err
		}
		prevStart := since.Add(-7 * 24 * time.Hour)
		prevEnd := time.Now().UTC().Add(-7 * 24 * time.Hour)
		prior, err := e.countPageviews(ctx, a.ServerID, a.TargetPath, prevStart, prevEnd)
		if err != nil {
			return 0, false, err
		}
		if prior == 0 {
			// No baseline — don't fire. Avoids spurious "infinite delta"
			// on newly-created pages.
			return 0, false, nil
		}
		delta := (float64(current) - float64(prior)) / float64(prior) * 100
		return delta, absF(delta) > a.Threshold, nil

	case "form_submission_rate":
		// Submissions per minute in window.
		if a.TargetFormID == "" {
			return 0, false, fmt.Errorf("target_form_id required")
		}
		var n int64
		row := e.db.QueryRowContext(ctx,
			`SELECT count() FROM events WHERE server_id = ? AND when >= ? AND code = 0x20 AND position(data, ?) > 0`,
			a.ServerID, since, a.TargetFormID)
		if err := row.Scan(&n); err != nil {
			return 0, false, err
		}
		rate := float64(n) / w.Minutes()
		return rate, rate > a.Threshold, nil

	case "bad_actor_rate":
		// Bad-actor hits/min. Rides on the events.is_bot column (Phase
		// 0 enrichment tags bot traffic there).
		var n int64
		row := e.db.QueryRowContext(ctx,
			`SELECT count() FROM events WHERE server_id = ? AND when >= ? AND is_bot = 1`,
			a.ServerID, since)
		if err := row.Scan(&n); err != nil {
			return 0, false, err
		}
		rate := float64(n) / w.Minutes()
		return rate, rate > a.Threshold, nil

	case "build_failed":
		// Any build_failed event in window. Threshold ignored; fire on
		// first observation. The Phase-3 site-deploy pipeline emits a
		// synthetic "build_failed" row on each failed build.
		var n int64
		row := e.db.QueryRowContext(ctx,
			`SELECT count() FROM events WHERE server_id = ? AND when >= ? AND code = 0x1000`, // build_failed code reserved
			a.ServerID, since)
		if err := row.Scan(&n); err != nil {
			return 0, false, err
		}
		return float64(n), n > 0, nil
	}
	return 0, false, fmt.Errorf("unknown kind %q", a.Kind)
}

func (e *Evaluator) countPageviews(ctx context.Context, serverID, path string, from, to time.Time) (int64, error) {
	var n int64
	err := e.db.QueryRowContext(ctx,
		`SELECT count() FROM events WHERE server_id = ? AND url_path = ? AND when >= ? AND when < ? AND code = 1`,
		serverID, path, from, to).Scan(&n)
	return n, err
}

// fire writes an AlertEvent row, updates LastFiredAt on the alert, and
// hands a rendered envelope to the notifier (email + push fan-out).
// Falls back to the legacy mailer-only path when the notifier
// global isn't set — lets the evaluator keep working on a partially-
// configured host.
func (e *Evaluator) fire(a hulabolt.StoredAlert, observed float64) {
	eventID := newID()
	recipients := append([]string(nil), a.Recipients...)

	status := "success"
	errText := ""

	// Prefer the notifier composite. When unavailable, fall through
	// to the mailer-only path preserved from Phase 4.
	if n := notifier.Global(); n != nil {
		report, _ := n.Deliver(context.Background(), buildEnvelope(eventID, a, observed, recipients))
		switch {
		case report.AnyOK():
			// delivered on at least one channel
		case !report.AllConfigured():
			status = "mailer_unconfigured"
		default:
			status = "failed"
			// Collect the first non-nil error as the displayed
			// message. Full per-channel detail lives in the
			// StoredNotificationSend row (stage 5a.5 future work).
			for _, c := range report.Results {
				if c.Err != nil {
					errText = c.Err.Error()
					break
				}
			}
		}
	} else if e.m == nil {
		status = "mailer_unconfigured"
	} else {
		err := e.m.Send(context.Background(), mailer.Message{
			To:      recipients,
			Subject: fmt.Sprintf("[hula alert] %s", a.Name),
			HTML:    renderAlertBody(a, observed),
		})
		switch {
		case err == nil:
			// delivered
		case errors.Is(err, mailer.ErrNotConfigured):
			status = "mailer_unconfigured"
		default:
			status = "failed"
			errText = err.Error()
		}
	}

	row := hulabolt.StoredAlertEvent{
		ID:             eventID,
		AlertID:        a.ID,
		ServerID:       a.ServerID,
		FiredAt:        time.Now().UTC(),
		ObservedValue:  observed,
		Threshold:      a.Threshold,
		Recipients:     recipients,
		DeliveryStatus: status,
		Error:          errText,
	}
	if err := hulabolt.PutAlertEvent(row); err != nil {
		evalLog.Errorf("alert %s: persist event: %s", a.ID, err)
	}

	// Update LastFiredAt so cooldown kicks in.
	a.LastFiredAt = row.FiredAt
	if _, err := hulabolt.PutAlert(a); err != nil {
		evalLog.Errorf("alert %s: update last_fired_at: %s", a.ID, err)
	}
	evalLog.Infof("alert %s (%s) fired: observed=%.2f threshold=%.2f status=%s", a.ID, a.Kind, observed, a.Threshold, status)
}

// tokenKey is the AES-GCM master key used by the tokenbox to open
// sealed push tokens. The evaluator needs to open tokens when
// resolving recipients → DeviceAddrs. Set at boot by
// server.RunUnified; nil leaves push delivery disabled (email-only).
var tokenKey []byte

// SetTokenKey installs the process-wide master key. Separate from
// Start() so the evaluator package doesn't depend on the TOTP-key
// loader directly.
func SetTokenKey(k []byte) { tokenKey = k }

// buildEnvelope turns an alert fire + email recipient list into a
// notifier.Envelope. Email DeviceAddrs come from the alert's raw
// Recipients[]; push DeviceAddrs come from every Active device
// registered to each recipient user, if push is enabled in their
// notification prefs and they're not in quiet hours.
//
// Recipient lookup uses ListNotificationPrefs indirectly: we resolve
// email → user_id by iterating user rows. For Phase 5a we match on
// the notification_prefs.user_id only — full user-directory lookup
// by email lives in pkg/store/bolt when stage 5a.7 lands it.
// Today we best-effort match the recipient email to a prefs row
// keyed on user_id == email (works for the canned admin user); any
// missing match falls through as email-only.
func buildEnvelope(id string, a hulabolt.StoredAlert, observed float64, emails []string) notifier.Envelope {
	env := notifier.Envelope{
		ID:        id,
		Subject:   fmt.Sprintf("[hula alert] %s", a.Name),
		HTMLBody:  renderAlertBody(a, observed),
		ShortText: fmt.Sprintf("%s fired: %.2f vs %.2f", a.Name, observed, a.Threshold),
	}
	for _, e := range emails {
		env.Recipients = append(env.Recipients, notifier.DeviceAddr{
			Channel: notifier.ChannelEmail,
			Email:   e,
			UserID:  e, // best-effort — see buildEnvelope doc comment
		})
		if tokenKey == nil {
			continue
		}
		prefs, _ := hulabolt.GetNotificationPrefs(e)
		if !prefs.PushEnabled {
			continue
		}
		if quietNow(prefs) {
			continue
		}
		devs, err := hulabolt.ListDevicesForUser(e)
		if err != nil {
			continue
		}
		for _, d := range devs {
			if !d.Active || len(d.TokenCipher) == 0 {
				continue
			}
			plain, err := tokenbox.Open(d.TokenCipher, tokenKey)
			if err != nil {
				// Tampered or wrong key — skip; the admin UI's
				// "forget device" flow handles remediation.
				continue
			}
			ch := notifier.ChannelAPNS
			if d.Platform == "fcm" {
				ch = notifier.ChannelFCM
			}
			env.Recipients = append(env.Recipients, notifier.DeviceAddr{
				Channel:   ch,
				UserID:    d.UserID,
				DeviceID:  d.ID,
				PushToken: plain,
			})
		}
	}
	return env
}

// quietNow returns true when `now` in prefs.Timezone falls inside
// the prefs.QuietHoursStart..End window. Handles midnight-spanning
// ranges (e.g. 22:00 → 07:00).
func quietNow(prefs hulabolt.StoredNotificationPrefs) bool {
	if prefs.QuietHoursStart == "" || prefs.QuietHoursEnd == "" {
		return false
	}
	loc, err := time.LoadLocation(prefs.Timezone)
	if err != nil || loc == nil {
		return false
	}
	now := time.Now().In(loc)
	start, err := time.ParseInLocation("15:04", prefs.QuietHoursStart, loc)
	if err != nil {
		return false
	}
	end, err := time.ParseInLocation("15:04", prefs.QuietHoursEnd, loc)
	if err != nil {
		return false
	}
	// Promote parsed-time to today-in-loc so date components match.
	y, m, d := now.Date()
	start = time.Date(y, m, d, start.Hour(), start.Minute(), 0, 0, loc)
	end = time.Date(y, m, d, end.Hour(), end.Minute(), 0, 0, loc)
	if start.Before(end) {
		return !now.Before(start) && now.Before(end)
	}
	// Midnight-spanning — quiet if now >= start OR now < end.
	return !now.Before(start) || now.Before(end)
}

func renderAlertBody(a hulabolt.StoredAlert, observed float64) string {
	return fmt.Sprintf(`<html><body>
<h2>%s fired</h2>
<p>%s</p>
<ul>
  <li>Rule: <code>%s</code></li>
  <li>Observed value: <strong>%.2f</strong></li>
  <li>Threshold: <strong>%.2f</strong></li>
  <li>Window: <strong>%d minutes</strong></li>
  <li>Server: <code>%s</code></li>
</ul>
<p style="color:#666">Manage alerts at /analytics/admin/alerts</p>
</body></html>`,
		htmlEscape(a.Name),
		htmlEscape(a.Description),
		htmlEscape(a.Kind),
		observed,
		a.Threshold,
		a.WindowMinutes,
		htmlEscape(a.ServerID),
	)
}

// htmlEscape is a tiny subset of html.EscapeString — avoids the stdlib
// html package pull just for five characters. Safe because the only
// user-controlled strings fed through are descriptive labels.
func htmlEscape(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<':
			out = append(out, []byte("&lt;")...)
		case '>':
			out = append(out, []byte("&gt;")...)
		case '&':
			out = append(out, []byte("&amp;")...)
		case '"':
			out = append(out, []byte("&quot;")...)
		case '\'':
			out = append(out, []byte("&#39;")...)
		default:
			out = append(out, s[i])
		}
	}
	return string(out)
}

func absF(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
