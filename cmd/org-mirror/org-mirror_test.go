package main

import (
	"context"
	"errors"
	"testing"
)

// TestNewExecutionContextIsCancellableAndNotTheBareBackgroundContext covers
// the property finalStatus depends on: that the context Execute runs the
// root command against is one an interrupt can actually end, not the bare
// background context that never does. Sending a real OS signal from a test
// is awkward and this does not attempt it; calling the returned stop
// function is the documented equivalent ("... or the returned stop function
// is called, whichever happens first" - signal.NotifyContext), so it
// exercises the same cancellation path a delivered signal would.
func TestNewExecutionContextIsCancellableAndNotTheBareBackgroundContext(t *testing.T) {
	ctx, stop := newExecutionContext()
	defer stop()

	if ctx == context.Background() {
		t.Fatal("execution context must not be the bare background context - an interrupt would then kill the process outright instead of letting sync record its final status")
	}
	select {
	case <-ctx.Done():
		t.Fatal("execution context must not already be done")
	default:
	}

	stop()

	select {
	case <-ctx.Done():
	default:
		t.Fatal("execution context must become done once stopped, the same way a delivered interrupt or SIGTERM would end it")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("ctx.Err() = %v, want context.Canceled", ctx.Err())
	}
}
