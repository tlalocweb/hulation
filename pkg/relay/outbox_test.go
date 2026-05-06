package relay

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tlalocweb/hulation/model"
)

func newEvent(id string) *model.Event {
	return &model.Event{HModel: model.HModel{ID: id}, BelongsTo: "vis-" + id}
}

func TestOutbox_EnqueueDrainAck(t *testing.T) {
	o, err := New(Config{RingSize: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Close() })

	for _, id := range []string{"a", "b", "c"} {
		if err := o.Enqueue(newEvent(id)); err != nil {
			t.Fatal(err)
		}
	}
	items, err := o.Drain(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("Drain got %d items, want 3", len(items))
	}
	gotIDs := []string{items[0].Event().ID, items[1].Event().ID, items[2].Event().ID}
	want := []string{"a", "b", "c"}
	for i := range gotIDs {
		if gotIDs[i] != want[i] {
			t.Errorf("Drain order wrong: got %v want %v", gotIDs, want)
			break
		}
	}
	if err := o.Ack(items); err != nil {
		t.Fatal(err)
	}
	// Outbox is now empty.
	again, _ := o.Drain(1)
	if len(again) != 0 {
		t.Errorf("expected empty after ack, got %d", len(again))
	}
}

func TestOutbox_RingFIFOEvictionWhenNoDisk(t *testing.T) {
	o, err := New(Config{RingSize: 3, EvictionLogEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Close() })

	// Push 5; ring holds only 3, so the oldest two are evicted.
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		if err := o.Enqueue(newEvent(id)); err != nil {
			t.Fatal(err)
		}
	}
	if got := o.Stats().Evictions; got != 2 {
		t.Errorf("Evictions got %d want 2", got)
	}
	items, _ := o.Drain(10)
	gotIDs := []string{items[0].Event().ID, items[1].Event().ID, items[2].Event().ID}
	want := []string{"c", "d", "e"}
	for i := range want {
		if gotIDs[i] != want[i] {
			t.Errorf("post-eviction order: got %v want %v", gotIDs, want)
			break
		}
	}
}

func TestOutbox_DiskOverflowPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "outbox.db")

	// First instance: ring=2, push 5; 3 spill to disk.
	o, err := New(Config{
		RingSize:         2,
		DiskOverflowPath: path,
		EvictionLogEvery: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		if err := o.Enqueue(newEvent(id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen — disk-backed entries survive.
	o2, err := New(Config{RingSize: 2, DiskOverflowPath: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o2.Close() })

	items, err := o2.Drain(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 events surviving (the disk-overflowed ones), got %d", len(items))
	}
	// Ring held a+b in-memory which were lost on Close. The disk-
	// overflowed events (c, d, e) survive.
	got := []string{items[0].Event().ID, items[1].Event().ID, items[2].Event().ID}
	want := []string{"c", "d", "e"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("disk order: got %v want %v", got, want)
			break
		}
	}
}

func TestOutbox_RequeueRoundTrips(t *testing.T) {
	o, err := New(Config{RingSize: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Close() })

	for _, id := range []string{"a", "b", "c"} {
		_ = o.Enqueue(newEvent(id))
	}
	items, _ := o.Drain(2) // grabs a, b
	o.Requeue(items)       // pretend send failed

	// Ring now has a, b at the head + c at the tail.
	items2, _ := o.Drain(10)
	if len(items2) != 3 {
		t.Fatalf("post-requeue Drain got %d, want 3", len(items2))
	}
	if items2[0].Event().ID != "a" || items2[2].Event().ID != "c" {
		t.Errorf("requeue order broken: %s/%s/%s", items2[0].Event().ID, items2[1].Event().ID, items2[2].Event().ID)
	}
}

func TestEncodeDecodeEvent_RoundTrip(t *testing.T) {
	src := &model.Event{
		HModel:    model.HModel{ID: "evt-1"},
		BelongsTo: "v-1",
		URL:       "https://example.com/foo",
		UrlPath:   "/foo",
		Channel:   "Direct",
		Browser:   "Firefox",
	}
	b, err := EncodeEvent(src)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeEvent(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != src.ID || got.URL != src.URL || got.Browser != src.Browser {
		t.Errorf("round-trip lost fields: src=%+v got=%+v", src, got)
	}
}

func TestDrainer_SuccessAcks(t *testing.T) {
	o, err := New(Config{RingSize: 16})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Close() })

	for i := 0; i < 5; i++ {
		_ = o.Enqueue(newEvent("x"))
	}

	var sent atomic.Uint64
	d := Start(context.Background(), o, func(_ context.Context, batch []*model.Event) error {
		sent.Add(uint64(len(batch)))
		return nil
	}, DrainerConfig{BatchSize: 10, IdlePoll: 10 * time.Millisecond})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sent.Load() >= 5 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	d.Stop()

	if sent.Load() < 5 {
		t.Errorf("drainer sent %d, want >= 5", sent.Load())
	}
	if d.Successes() == 0 {
		t.Error("Successes counter not incremented")
	}
}

func TestDrainer_RetriesOnFailure(t *testing.T) {
	o, err := New(Config{RingSize: 16})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Close() })

	for i := 0; i < 3; i++ {
		_ = o.Enqueue(newEvent("y"))
	}

	var attempts atomic.Uint64
	var mu sync.Mutex
	failUntilN := uint64(2)
	d := Start(context.Background(), o,
		func(_ context.Context, batch []*model.Event) error {
			mu.Lock()
			defer mu.Unlock()
			n := attempts.Add(1)
			if n <= failUntilN {
				return errors.New("simulated peer failure")
			}
			return nil
		}, DrainerConfig{
			BatchSize:   10,
			IdlePoll:    10 * time.Millisecond,
			BackoffMin:  10 * time.Millisecond,
			BackoffMax:  100 * time.Millisecond,
			BackoffMult: 2,
		})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if d.Successes() >= 1 && d.Failures() >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	d.Stop()

	if d.Failures() < 2 {
		t.Errorf("Failures got %d, want >= 2", d.Failures())
	}
	if d.Successes() == 0 {
		t.Error("eventually-succeed branch never ran")
	}
}

func TestNextDelay_ExponentialAndCapped(t *testing.T) {
	got := nextDelay(100*time.Millisecond, 1*time.Second, 3)
	if got != 300*time.Millisecond {
		t.Errorf("100ms*3 = %s, want 300ms", got)
	}
	if got := nextDelay(500*time.Millisecond, 1*time.Second, 3); got != time.Second {
		t.Errorf("capped value got %s, want 1s", got)
	}
}
