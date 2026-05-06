package relay

// Drainer is the background goroutine that pulls batches off the
// Outbox and ships them to a peer's RelayService.RecordEventBatch.
// HA_PLAN3 §6.2: exponential backoff on failure (caps at 30s); the
// drainer never gives up — events stay in the outbox until either
// delivered or evicted by FIFO when capacity is exhausted.

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/tlalocweb/hulation/model"
)

// Sender is the function the drainer calls to ship a batch. The
// production wiring plugs in a gRPC client to RelayService; tests
// substitute a fake.
type Sender func(ctx context.Context, batch []*model.Event) error

// DrainerConfig tunes the drainer.
type DrainerConfig struct {
	BatchSize    int           // events per RecordEventBatch call. Default 64.
	IdlePoll     time.Duration // sleep when the outbox was empty. Default 250ms.
	BackoffMin   time.Duration // first retry delay after a failure. Default 100ms.
	BackoffMax   time.Duration // cap on retry delay. Default 30s.
	BackoffMult  float64       // backoff multiplier per failure. Default 3.0.
	SendTimeout  time.Duration // per-batch RecordEventBatch deadline. Default 5s.
}

// Drainer is the running goroutine handle. Stop() returns once the
// goroutine exits cleanly.
type Drainer struct {
	cfg    DrainerConfig
	outbox *Outbox
	send   Sender

	stopped chan struct{}
	cancel  context.CancelFunc

	failures atomic.Uint64
	successes atomic.Uint64
}

// Start wires + launches the goroutine. Returns immediately. Stop
// the drainer with Stop().
func Start(ctx context.Context, outbox *Outbox, send Sender, cfg DrainerConfig) *Drainer {
	cfg = applyDrainerDefaults(cfg)
	cctx, cancel := context.WithCancel(ctx)
	d := &Drainer{
		cfg:     cfg,
		outbox:  outbox,
		send:    send,
		stopped: make(chan struct{}),
		cancel:  cancel,
	}
	go d.run(cctx)
	return d
}

// Stop cancels the drainer goroutine and waits for it to exit.
func (d *Drainer) Stop() {
	d.cancel()
	<-d.stopped
}

// Successes / Failures are exported counters for tests + ops.
func (d *Drainer) Successes() uint64 { return d.successes.Load() }
func (d *Drainer) Failures() uint64  { return d.failures.Load() }

func (d *Drainer) run(ctx context.Context) {
	defer close(d.stopped)
	delay := d.cfg.BackoffMin
	for {
		if ctx.Err() != nil {
			return
		}

		items, err := d.outbox.Drain(d.cfg.BatchSize)
		if err != nil {
			relayLog.Warnf("relay drainer: drain error: %v", err)
			d.sleepOrCancel(ctx, d.cfg.IdlePoll)
			continue
		}
		if len(items) == 0 {
			d.sleepOrCancel(ctx, d.cfg.IdlePoll)
			delay = d.cfg.BackoffMin
			continue
		}

		batch := make([]*model.Event, 0, len(items))
		for _, it := range items {
			batch = append(batch, it.Event())
		}

		callCtx, cancel := context.WithTimeout(ctx, d.cfg.SendTimeout)
		err = d.send(callCtx, batch)
		cancel()

		if err == nil {
			if ackErr := d.outbox.Ack(items); ackErr != nil {
				relayLog.Warnf("relay drainer: ack error (delivered events may resend): %v", ackErr)
			}
			d.successes.Add(1)
			delay = d.cfg.BackoffMin
			continue
		}

		d.failures.Add(1)
		// Re-queue the in-memory portion (disk-backed entries stayed
		// in place; ringSeq entries were popped on Drain).
		d.outbox.Requeue(items)

		if errors.Is(err, context.Canceled) {
			return
		}
		relayLog.Warnf("relay drainer: send failed (delay=%s): %v", delay, err)
		d.sleepOrCancel(ctx, delay)
		delay = nextDelay(delay, d.cfg.BackoffMax, d.cfg.BackoffMult)
	}
}

func (d *Drainer) sleepOrCancel(ctx context.Context, dur time.Duration) {
	if dur <= 0 {
		return
	}
	t := time.NewTimer(dur)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func nextDelay(cur, cap time.Duration, mult float64) time.Duration {
	if mult <= 1 {
		mult = 2
	}
	next := time.Duration(float64(cur) * mult)
	if next > cap {
		return cap
	}
	if next < cur {
		return cap // overflow safety
	}
	return next
}

func applyDrainerDefaults(c DrainerConfig) DrainerConfig {
	if c.BatchSize <= 0 {
		c.BatchSize = 64
	}
	if c.IdlePoll <= 0 {
		c.IdlePoll = 250 * time.Millisecond
	}
	if c.BackoffMin <= 0 {
		c.BackoffMin = 100 * time.Millisecond
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = 30 * time.Second
	}
	if c.BackoffMult <= 0 {
		c.BackoffMult = 3.0
	}
	if c.SendTimeout <= 0 {
		c.SendTimeout = 5 * time.Second
	}
	return c
}
