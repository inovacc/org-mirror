package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// ErrWaitTooLong is returned when honouring a rate limit would block longer
// than the configured maximum. Failing loudly beats hanging silently.
var ErrWaitTooLong = errors.New("rate limit wait exceeds the configured maximum")

const (
	defaultMaxAttempts = 5
	defaultMaxWait     = 15 * time.Minute
	defaultReserve     = 5
	backoffBase        = time.Second
	backoffCeiling     = 60 * time.Second
)

// Snapshot is the most recent rate-limit state GitHub reported.
type Snapshot struct {
	Limit     int
	Used      int
	Remaining int
	Reset     time.Time
	Valid     bool
}

// Wait describes a delay the transport is about to take, so a caller can show
// it rather than appearing hung.
type Wait struct {
	Reason   string
	Duration time.Duration
	Until    time.Time
}

// TransportOptions configures a Transport.
type TransportOptions struct {
	// Base defaults to http.DefaultTransport.
	Base http.RoundTripper
	// Pacer, when set, spaces requests. Nil means no pacing.
	Pacer *Pacer
	// Clock defaults to SystemClock.
	Clock Clock
	// Reserve is how many requests of budget to keep in hand. Zero uses 5.
	Reserve int
	// MaxAttempts counts the first try. Zero uses 5.
	MaxAttempts int
	// MaxWait caps any single wait. Zero uses 15 minutes.
	MaxWait time.Duration
	// Notify, when set, is called before each wait.
	Notify func(Wait)
}

// Transport is an http.RoundTripper that honours GitHub's rate-limit headers.
type Transport struct {
	base        http.RoundTripper
	pacer       *Pacer
	clock       Clock
	reserve     int
	maxAttempts int
	maxWait     time.Duration
	notify      func(Wait)

	mu        sync.Mutex
	snapshot  Snapshot
	holdUntil time.Time
}

// NewTransport builds a Transport from options, applying defaults for any
// zero values.
func NewTransport(options TransportOptions) *Transport {
	transport := &Transport{
		base:        options.Base,
		pacer:       options.Pacer,
		clock:       options.Clock,
		reserve:     options.Reserve,
		maxAttempts: options.MaxAttempts,
		maxWait:     options.MaxWait,
		notify:      options.Notify,
	}
	if transport.base == nil {
		transport.base = http.DefaultTransport
	}
	if transport.clock == nil {
		transport.clock = SystemClock{}
	}
	if transport.reserve <= 0 {
		transport.reserve = defaultReserve
	}
	if transport.maxAttempts <= 0 {
		transport.maxAttempts = defaultMaxAttempts
	}
	if transport.maxWait <= 0 {
		transport.maxWait = defaultMaxWait
	}
	return transport
}

// Snapshot returns the last rate-limit state seen on any response.
func (t *Transport) Snapshot() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snapshot
}

// RoundTrip implements http.RoundTripper. It paces requests, waits out any
// hold armed by a previous response's rate-limit headers, and retries
// throttled, rate-limited, server-error or transport-level failures with
// backoff, up to MaxAttempts and never past MaxWait for a single wait.
func (t *Transport) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx := request.Context()
	retryable := request.Method == http.MethodGet || request.Method == http.MethodHead

	for attempt := 1; ; attempt++ {
		if err := t.honourHold(ctx); err != nil {
			return nil, err
		}
		if t.pacer != nil {
			if err := t.pacer.Wait(ctx); err != nil {
				return nil, err
			}
		}

		response, err := t.base.RoundTrip(request)
		if err != nil {
			if !retryable || attempt >= t.maxAttempts {
				return nil, err
			}
			if waitErr := t.sleep(ctx, Wait{
				Reason:   fmt.Sprintf("request failed (%v), retrying", err),
				Duration: backoff(attempt),
			}); waitErr != nil {
				return nil, waitErr
			}
			continue
		}

		t.observe(response)
		delay, reason, retry := t.retryPlan(response, attempt)
		if !retry || !retryable || attempt >= t.maxAttempts {
			return response, nil
		}
		if delay > t.maxWait {
			response.Body.Close()
			return nil, fmt.Errorf("%w: %v", ErrWaitTooLong, delay)
		}
		drain(response)
		if err := t.sleep(ctx, Wait{Reason: reason, Duration: delay}); err != nil {
			return nil, err
		}
	}
}

// honourHold waits out a budget exhaustion observed on an earlier response, so
// the wait is paid before the next request rather than after the last one.
func (t *Transport) honourHold(ctx context.Context) error {
	t.mu.Lock()
	hold := t.holdUntil
	t.mu.Unlock()
	if hold.IsZero() {
		return nil
	}
	remaining := hold.Sub(t.clock.Now())
	if remaining <= 0 {
		t.clearHold(hold)
		return nil
	}
	if remaining > t.maxWait {
		return fmt.Errorf("%w: %v until the rate limit resets", ErrWaitTooLong, remaining)
	}
	err := t.sleep(ctx, Wait{
		Reason:   "GitHub rate limit exhausted, waiting for the reset",
		Duration: remaining,
		Until:    hold,
	})
	if err != nil {
		return err
	}
	t.clearHold(hold)
	return nil
}

// clearHold releases a hold only if it is still the one we waited out, so a
// newer hold armed by a concurrent response is not silently discarded.
func (t *Transport) clearHold(waited time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.holdUntil.Equal(waited) {
		t.holdUntil = time.Time{}
	}
}

func (t *Transport) sleep(ctx context.Context, wait Wait) error {
	if wait.Duration <= 0 {
		return nil
	}
	if wait.Until.IsZero() {
		wait.Until = t.clock.Now().Add(wait.Duration)
	}
	if t.notify != nil {
		t.notify(wait)
	}
	return t.clock.Sleep(ctx, wait.Duration)
}

// observe records the rate-limit headers and arms a hold when the remaining
// budget has fallen to the reserve.
func (t *Transport) observe(response *http.Response) {
	remaining, hasRemaining := headerInt(response, "x-ratelimit-remaining")
	limit, _ := headerInt(response, "x-ratelimit-limit")
	used, _ := headerInt(response, "x-ratelimit-used")
	reset, hasReset := t.resetAt(response)
	if !hasRemaining {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.snapshot = Snapshot{Limit: limit, Used: used, Remaining: remaining, Reset: reset, Valid: true}
	if remaining <= t.reserve && hasReset && reset.After(t.clock.Now()) {
		t.holdUntil = reset
	}
}

// retryPlan decides whether a response should be retried, how long to wait
// first, and what to tell the caller.
func (t *Transport) retryPlan(response *http.Response, attempt int) (time.Duration, string, bool) {
	switch {
	case response.StatusCode == http.StatusTooManyRequests:
		delay, reason := t.throttleDelay(response, attempt, "GitHub returned 429")
		return delay, reason, true
	case response.StatusCode == http.StatusForbidden && t.isRateLimited(response):
		delay, reason := t.throttleDelay(response, attempt, "GitHub rate limit reached")
		return delay, reason, true
	case response.StatusCode >= 500:
		return backoff(attempt), fmt.Sprintf("GitHub returned %d, retrying", response.StatusCode), true
	default:
		return 0, "", false
	}
}

// isRateLimited distinguishes a throttled 403 from a permission error without
// reading the body, so the body a caller receives is never disturbed.
func (t *Transport) isRateLimited(response *http.Response) bool {
	if response.Header.Get("retry-after") != "" {
		return true
	}
	remaining, ok := headerInt(response, "x-ratelimit-remaining")
	return ok && remaining <= 0
}

func (t *Transport) throttleDelay(response *http.Response, attempt int, reason string) (time.Duration, string) {
	if seconds, ok := headerInt(response, "retry-after"); ok && seconds > 0 {
		return time.Duration(seconds) * time.Second, reason + ", honouring retry-after"
	}
	if reset, ok := t.resetAt(response); ok {
		if delay := reset.Sub(t.clock.Now()); delay > 0 {
			return delay, reason + ", waiting for the reset"
		}
	}
	return backoff(attempt), reason + ", backing off"
}

func (t *Transport) resetAt(response *http.Response) (time.Time, bool) {
	seconds, ok := headerInt(response, "x-ratelimit-reset")
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(seconds), 0).UTC(), true
}

func headerInt(response *http.Response, name string) (int, bool) {
	raw := response.Header.Get(name)
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return value, true
}

// backoff doubles from one second up to the ceiling. attempt starts at 1.
func backoff(attempt int) time.Duration {
	delay := backoffBase << (attempt - 1)
	if delay > backoffCeiling || delay <= 0 {
		return backoffCeiling
	}
	return delay
}

// drain lets the underlying connection be reused before a retry.
func drain(response *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	_ = response.Body.Close()
}
