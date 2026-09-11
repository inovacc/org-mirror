package ratelimit

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeClock advances only when something sleeps on it, so tests are instant.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	slept []time.Duration
	err   error
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	if d > 0 {
		c.slept = append(c.slept, d)
		c.now = c.now.Add(d)
	}
	return nil
}

func (c *fakeClock) sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.slept...)
}

func TestSystemClockSleepReturnsAfterTheDuration(t *testing.T) {
	clock := SystemClock{}
	start := clock.Now()
	if err := clock.Sleep(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("sleep: %v", err)
	}
	if !clock.Now().After(start) {
		t.Fatal("system clock did not advance across a sleep")
	}
}

func TestSystemClockSleepReturnsWhenTheContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (SystemClock{}).Sleep(ctx, time.Hour); err == nil {
		t.Fatal("sleep on a cancelled context must return an error")
	}
}

func TestSystemClockSleepIgnoresNonPositiveDurations(t *testing.T) {
	if err := (SystemClock{}).Sleep(context.Background(), -time.Hour); err != nil {
		t.Fatalf("negative sleep: %v", err)
	}
}
