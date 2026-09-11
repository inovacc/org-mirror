package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestPacerDoesNotWaitBeforeTheFirstOperation(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	pacer := NewPacer(PacerOptions{Interval: time.Second, Clock: clock})

	if err := pacer.Wait(context.Background()); err != nil {
		t.Fatalf("first wait: %v", err)
	}
	if got := clock.sleeps(); len(got) != 0 {
		t.Fatalf("first wait slept %v, want none", got)
	}
}

func TestPacerSpacesSubsequentOperationsByTheInterval(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	pacer := NewPacer(PacerOptions{Interval: 750 * time.Millisecond, Clock: clock})
	ctx := context.Background()

	for range 3 {
		if err := pacer.Wait(ctx); err != nil {
			t.Fatalf("wait: %v", err)
		}
	}

	want := []time.Duration{750 * time.Millisecond, 750 * time.Millisecond}
	got := clock.sleeps()
	if len(got) != len(want) {
		t.Fatalf("sleeps = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("sleep %d = %v, want %v", index, got[index], want[index])
		}
	}
}

func TestPacerAddsJitterWithinItsBound(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	pacer := NewPacer(PacerOptions{
		Interval: time.Second,
		Jitter:   0.3,
		Clock:    clock,
		Rand:     func() float64 { return 1 },
	})
	ctx := context.Background()

	if err := pacer.Wait(ctx); err != nil {
		t.Fatalf("first wait: %v", err)
	}
	if err := pacer.Wait(ctx); err != nil {
		t.Fatalf("second wait: %v", err)
	}

	got := clock.sleeps()
	if len(got) != 1 || got[0] != 1300*time.Millisecond {
		t.Fatalf("sleeps = %v, want one sleep of 1.3s", got)
	}
}

func TestPacerWithZeroIntervalNeverWaits(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	pacer := NewPacer(PacerOptions{Interval: 0, Clock: clock})
	ctx := context.Background()

	for range 5 {
		if err := pacer.Wait(ctx); err != nil {
			t.Fatalf("wait: %v", err)
		}
	}
	if got := clock.sleeps(); len(got) != 0 {
		t.Fatalf("zero interval slept %v, want none", got)
	}
}

func TestPacerSubtractsTimeAlreadySpentOnTheOperation(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	pacer := NewPacer(PacerOptions{Interval: time.Second, Clock: clock})
	ctx := context.Background()

	if err := pacer.Wait(ctx); err != nil {
		t.Fatalf("first wait: %v", err)
	}
	// The caller spent 400ms doing real work before asking to pace again.
	if err := clock.Sleep(ctx, 400*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := pacer.Wait(ctx); err != nil {
		t.Fatalf("second wait: %v", err)
	}

	got := clock.sleeps()
	if len(got) != 2 || got[1] != 600*time.Millisecond {
		t.Fatalf("sleeps = %v, want the second to be 600ms", got)
	}
}

func TestPacerReturnsTheContextErrorWhenCancelled(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	pacer := NewPacer(PacerOptions{Interval: time.Hour, Clock: clock})
	ctx, cancel := context.WithCancel(context.Background())

	if err := pacer.Wait(ctx); err != nil {
		t.Fatalf("first wait: %v", err)
	}
	cancel()
	if err := pacer.Wait(ctx); err == nil {
		t.Fatal("wait on a cancelled context must return an error")
	}
}

func TestNewPacerUsesTheSystemClockWhenNoneIsGiven(t *testing.T) {
	pacer := NewPacer(PacerOptions{Interval: time.Millisecond})
	if pacer.clock == nil {
		t.Fatal("pacer must default its clock")
	}
	if err := pacer.Wait(context.Background()); err != nil {
		t.Fatalf("wait: %v", err)
	}
}
