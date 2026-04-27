package dispatch

import (
	"testing"
	"time"
)

// TestCronParserDailyAt9AmUTC confirms the scheduler wrapper accepts
// standard five-field cron + yields the right next-fire time in UTC.
func TestCronParserDailyAt9AmUTC(t *testing.T) {
	sched, err := cronParser.Parse("0 9 * * *")
	if err != nil {
		t.Fatalf("parse cron: %v", err)
	}
	// At 08:00 UTC the next fire is today 09:00 UTC.
	now := time.Date(2026, 4, 24, 8, 0, 0, 0, time.UTC)
	next := sched.Next(now)
	want := time.Date(2026, 4, 24, 9, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next fire = %s, want %s", next, want)
	}
}

// TestCronParserAcrossDSTBoundary pins that Loc-in / UTC-out works
// across a daylight-saving transition. New York observes DST; on
// 2026-03-08 at 02:00 local time the clock jumps to 03:00.
// A 01:30 cron fires before the jump; a 02:30 cron skips that
// wall-clock hour and next fires on the 9th.
func TestCronParserAcrossDSTBoundary(t *testing.T) {
	tz, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	sched, err := cronParser.Parse("30 2 * * *") // 02:30 local
	if err != nil {
		t.Fatalf("parse cron: %v", err)
	}
	now := time.Date(2026, 3, 8, 1, 0, 0, 0, tz) // 01:00 the morning of spring-forward
	next := sched.Next(now)
	// The 02:30 slot doesn't exist on this day — expect the 9th's 02:30.
	if next.Day() != 9 {
		t.Fatalf("expected DST-skip to next day, got %s", next)
	}
}

// TestEnqueueNoDispatcher guards the RPC-side error path: calling
// d.Enqueue on a nil Dispatcher must not panic.
func TestEnqueueNoDispatcher(t *testing.T) {
	var d *Dispatcher
	_, err := d.Enqueue("whatever")
	if err == nil {
		t.Fatalf("Enqueue on nil dispatcher should error")
	}
}

// TestEnqueueQueueFull fills the SendNow channel to its cap and
// confirms the next Enqueue returns an error instead of blocking.
func TestEnqueueQueueFull(t *testing.T) {
	d := &Dispatcher{enqueued: make(chan string, 2), stopCh: make(chan struct{})}
	if _, err := d.Enqueue("a"); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	if _, err := d.Enqueue("b"); err != nil {
		t.Fatalf("second enqueue: %v", err)
	}
	// Third should overflow.
	if _, err := d.Enqueue("c"); err == nil {
		t.Fatalf("third enqueue should have errored (queue full)")
	}
}
