// Package ratelimit paces and retries outbound work so a long run trips
// neither GitHub's rate limits nor local endpoint-protection heuristics.
package ratelimit

import (
	"context"
	"time"
)

// Clock is the package's only source of time. Injecting it keeps every test
// instant, because a fake clock advances without waiting.
type Clock interface {
	Now() time.Time
	// Sleep blocks for d, or returns the context's error if it ends first.
	// A non-positive d returns immediately.
	Sleep(ctx context.Context, d time.Duration) error
}

// SystemClock is the real clock used in production.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

func (SystemClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
