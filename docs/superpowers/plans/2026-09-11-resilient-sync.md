# Resilient Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `org-mirror sync` resume after an interruption, pace itself so it trips neither GitHub's rate limits nor local antivirus heuristics, and add a `limit` verb plus a `--limit` cap.

**Architecture:** A new `internal/ratelimit` package supplies a `Clock`, a jittered `Pacer`, and an `http.RoundTripper` that honours GitHub's rate-limit headers. The pacer also gates each repository in the mirror loop, and transient git failures retry with backoff. `internal/history` gains a run lifecycle so each repository is checkpointed as it finishes, which is what makes resume possible. All waiting is done through an injected clock so no test sleeps.

**Tech Stack:** Go 1.26, Cobra, `github.com/cli/go-gh/v2`, `modernc.org/sqlite`, `charm.land/bubbletea/v2`. Standard library only for the new package.

**Spec:** `docs/superpowers/specs/2026-09-11-resilient-sync-design.md`

## Global Constraints

- Go 1.26.0 as declared in `go.mod`. Do not raise it. Do not add a third-party dependency; everything new uses the standard library.
- Never reset, stash, delete, or overwrite a working copy. Conflicts are recorded, never resolved.
- Every wait selects on the context so `Ctrl+C` is immediate.
- Every sleep goes through the injected `Clock`, never `time.Sleep`, so tests are instant.
- Timestamps stored in SQLite are UTC RFC 3339 nanosecond strings, matching the existing rows.
- Existing exported functions keep working: `history.Database.Record`, `mirror.Service.Mirror`, and `mirror.Service.MirrorWithProgress` stay callable with their current signatures.
- Tests are table-driven when several cases exercise the SAME behaviour with different
  inputs. Cases that exercise DIFFERENT branches with different assertions stay as
  separate named tests; a table whose rows each need their own flag is a switch
  statement wearing a table's clothes.
- Tests use `t.TempDir()` for files and must not sleep for a measurable duration. The
  single exception is the one test that exercises `SystemClock.Sleep` itself, where the
  real wait IS the behaviour under test; it uses one millisecond.
- Run `go build ./... && go vet ./... && go test ./...` before every commit.
- Commit messages are conventional (`feat:`, `fix:`, `test:`, `docs:`) and end with the line `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`.

---

## File Structure

- `internal/ratelimit/clock.go` — the `Clock` interface and the real implementation. New.
- `internal/ratelimit/clock_test.go` — real clock behaviour and the shared fake clock used by the package's other tests. New.
- `internal/ratelimit/pacer.go` — jittered minimum-interval pacer. New.
- `internal/ratelimit/pacer_test.go` — spacing, jitter bounds, zero interval, cancellation. New.
- `internal/ratelimit/transport.go` — the rate-limit aware `http.RoundTripper` and its snapshot. New.
- `internal/ratelimit/transport_test.go` — header handling, 403, 429, 5xx, transport error, max wait, cancellation. New.
- `internal/mirror/retry.go` — transient git failure classification and the retry policy type. New.
- `internal/mirror/retry_test.go` — the classification table and retry behaviour. New.
- `internal/mirror/progress.go` — gains a waiting event kind and two event fields. Modified.
- `internal/mirror/model.go` — gains the `skipped` outcome. Modified.
- `internal/mirror/service.go` — gains `MirrorWithOptions`, pacing, retrying git calls, the skip set, and the repository cap. Modified.
- `internal/mirror/service_test.go` — new cases for pacing, retry, skip, and cap. Modified.
- `internal/history/database.go` — gains the run lifecycle and the resume query. Modified.
- `internal/history/database_test.go` — new cases for the lifecycle and resume. Modified.
- `internal/githubapi/ratelimit.go` — the rate-limit endpoint and the organization repository count. New.
- `internal/githubapi/ratelimit_test.go` — parsing both payloads. New.
- `internal/githubapi/authenticated.go` — installs the limiter transport and returns it. Modified.
- `internal/tui/model.go` — renders the waiting line. Modified.
- `internal/tui/model_test.go` — the waiting line renders. Modified.
- `cmd/org-mirror/cmd_limit.go` — the `limit` command. New.
- `cmd/org-mirror/cmd_limit_test.go` — table and JSON rendering. New.
- `cmd/org-mirror/cmd_sync.go` — new flags, resume decision, per-repository checkpointing, final status. Modified.
- `cmd/org-mirror/cmd_sync_test.go` — new flag cases and the final-status helper. Modified.
- `README.md` — documents the new flags and the `limit` verb. Modified.

---

### Task 1: Clock and Pacer

The foundation everything else waits on. A `Clock` so tests never sleep, and a pacer that spaces operations with jitter.

**Files:**
- Create: `internal/ratelimit/clock.go`
- Create: `internal/ratelimit/clock_test.go`
- Create: `internal/ratelimit/pacer.go`
- Create: `internal/ratelimit/pacer_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Clock interface { Now() time.Time; Sleep(ctx context.Context, d time.Duration) error }`
  - `type SystemClock struct{}` implementing `Clock`.
  - `type PacerOptions struct { Interval time.Duration; Jitter float64; Clock Clock; Rand func() float64 }`
  - `func NewPacer(options PacerOptions) *Pacer`
  - `func (p *Pacer) Wait(ctx context.Context) error`
  - `type fakeClock` in `clock_test.go` with `func newFakeClock(start time.Time) *fakeClock` and field `slept []time.Duration`, used by every other test in this package.

- [ ] **Step 1: Write the failing tests**

Create `internal/ratelimit/clock_test.go`:

```go
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
	// onSleep runs while a sleep is in flight, so a test can simulate another
	// goroutine changing state that the sleeping code will act on when it wakes.
	onSleep func()
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
	if c.onSleep != nil {
		hook := c.onSleep
		c.mu.Unlock()
		hook()
		c.mu.Lock()
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
```

Create `internal/ratelimit/pacer_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/ratelimit/ -run 'Clock|Pacer' -v`
Expected: FAIL, the package does not compile because `SystemClock`, `NewPacer`, `PacerOptions` and `Pacer` are undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/ratelimit/clock.go`:

```go
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
```

Create `internal/ratelimit/pacer.go`:

```go
package ratelimit

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"
)

// PacerOptions configures a Pacer. The zero value paces nothing.
type PacerOptions struct {
	// Interval is the minimum time between two operations. Zero disables pacing.
	Interval time.Duration
	// Jitter is the fraction of Interval that may be added at random, so the
	// spacing is not perfectly regular. 0.3 means up to 30 percent extra.
	Jitter float64
	// Clock defaults to SystemClock.
	Clock Clock
	// Rand returns a value in [0,1). It defaults to the standard generator and
	// exists so tests can pin the jitter.
	Rand func() float64
}

// Pacer enforces a minimum interval between operations. It is safe for
// concurrent use, though the mirror runs sequentially today.
type Pacer struct {
	mu       sync.Mutex
	last     time.Time
	started  bool
	interval time.Duration
	jitter   float64
	clock    Clock
	random   func() float64
}

func NewPacer(options PacerOptions) *Pacer {
	pacer := &Pacer{
		interval: options.Interval,
		jitter:   options.Jitter,
		clock:    options.Clock,
		random:   options.Rand,
	}
	if pacer.clock == nil {
		pacer.clock = SystemClock{}
	}
	if pacer.random == nil {
		pacer.random = rand.Float64
	}
	if pacer.interval < 0 {
		pacer.interval = 0
	}
	if pacer.jitter < 0 {
		pacer.jitter = 0
	}
	return pacer
}

// Wait blocks until enough time has passed since the previous Wait returned.
// The first call never waits, so a single-repository run pays nothing.
func (p *Pacer) Wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.interval == 0 {
		p.last = p.clock.Now()
		p.started = true
		return nil
	}
	if !p.started {
		p.started = true
		p.last = p.clock.Now()
		return nil
	}
	gap := p.interval + time.Duration(float64(p.interval)*p.jitter*p.random())
	remaining := p.last.Add(gap).Sub(p.clock.Now())
	if remaining > 0 {
		if err := p.clock.Sleep(ctx, remaining); err != nil {
			return err
		}
	}
	p.last = p.clock.Now()
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/ratelimit/ -v`
Expected: PASS, every test in the package.

- [ ] **Step 5: Commit**

```bash
git add internal/ratelimit/clock.go internal/ratelimit/clock_test.go internal/ratelimit/pacer.go internal/ratelimit/pacer_test.go
git commit -m "feat: add injectable clock and jittered pacer

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Rate-limit aware HTTP transport

The round tripper that reads GitHub's rate-limit headers, waits when the budget is nearly spent, and retries throttled or failed requests.

**Files:**
- Create: `internal/ratelimit/transport.go`
- Create: `internal/ratelimit/transport_test.go`

**Interfaces:**
- Consumes: `Clock`, `SystemClock`, `Pacer` and `NewPacer` from Task 1; `fakeClock` and `newFakeClock` from `clock_test.go`.
- Produces:
  - `type Snapshot struct { Limit, Used, Remaining int; Reset time.Time; Valid bool }`
  - `type Wait struct { Reason string; Duration time.Duration; Until time.Time }`
  - `type TransportOptions struct { Base http.RoundTripper; Pacer *Pacer; Clock Clock; Reserve int; MaxAttempts int; MaxWait time.Duration; Notify func(Wait) }`
  - `func NewTransport(options TransportOptions) *Transport`
  - `func (t *Transport) RoundTrip(request *http.Request) (*http.Response, error)`
  - `func (t *Transport) Snapshot() Snapshot`
  - `var ErrWaitTooLong = errors.New("rate limit wait exceeds the configured maximum")`

- [ ] **Step 1: Write the failing tests**

Create `internal/ratelimit/transport_test.go`:

```go
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// stubTransport replays a fixed script of responses and errors.
type stubTransport struct {
	responses []stubResponse
	calls     int
}

type stubResponse struct {
	status  int
	headers map[string]string
	err     error
}

func (s *stubTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if s.calls >= len(s.responses) {
		return nil, fmt.Errorf("unexpected call %d", s.calls+1)
	}
	scripted := s.responses[s.calls]
	s.calls++
	if scripted.err != nil {
		return nil, scripted.err
	}
	recorder := httptest.NewRecorder()
	for key, value := range scripted.headers {
		recorder.Header().Set(key, value)
	}
	recorder.WriteHeader(scripted.status)
	response := recorder.Result()
	response.Request = request
	return response, nil
}

func newRequest(t *testing.T, ctx context.Context) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/orgs/acme/repos", nil)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func resetHeader(clock *fakeClock, in time.Duration) string {
	return strconv.FormatInt(clock.Now().Add(in).Unix(), 10)
}

func TestTransportRecordsTheRateLimitSnapshot(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	base := &stubTransport{responses: []stubResponse{{
		status: http.StatusOK,
		headers: map[string]string{
			"x-ratelimit-limit":     "5000",
			"x-ratelimit-used":      "120",
			"x-ratelimit-remaining": "4880",
			"x-ratelimit-reset":     resetHeader(clock, time.Hour),
		},
	}}}
	transport := NewTransport(TransportOptions{Base: base, Clock: clock})

	response, err := transport.RoundTrip(newRequest(t, context.Background()))
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	response.Body.Close()

	snapshot := transport.Snapshot()
	if !snapshot.Valid || snapshot.Limit != 5000 || snapshot.Used != 120 || snapshot.Remaining != 4880 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if want := clock.Now().Add(time.Hour); !snapshot.Reset.Equal(want) {
		t.Fatalf("reset = %v, want %v", snapshot.Reset, want)
	}
}

func TestTransportWaitsForTheResetWhenTheBudgetReachesTheReserve(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	lowBudget := map[string]string{
		"x-ratelimit-limit":     "5000",
		"x-ratelimit-remaining": "2",
		"x-ratelimit-reset":     resetHeader(clock, 30*time.Minute),
	}
	base := &stubTransport{responses: []stubResponse{
		{status: http.StatusOK, headers: lowBudget},
		{status: http.StatusOK},
	}}
	transport := NewTransport(TransportOptions{Base: base, Clock: clock, Reserve: 5, MaxWait: time.Hour})
	ctx := context.Background()

	first, err := transport.RoundTrip(newRequest(t, ctx))
	if err != nil {
		t.Fatalf("first round trip: %v", err)
	}
	first.Body.Close()
	if got := clock.sleeps(); len(got) != 0 {
		t.Fatalf("observing a low budget must not delay the response it came on, slept %v", got)
	}

	second, err := transport.RoundTrip(newRequest(t, ctx))
	if err != nil {
		t.Fatalf("second round trip: %v", err)
	}
	second.Body.Close()
	got := clock.sleeps()
	if len(got) != 1 || got[0] != 30*time.Minute {
		t.Fatalf("sleeps = %v, want one sleep of 30m before the next request", got)
	}
}

func TestTransportRetriesAfterRetryAfterOnTooManyRequests(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	base := &stubTransport{responses: []stubResponse{
		{status: http.StatusTooManyRequests, headers: map[string]string{"retry-after": "42"}},
		{status: http.StatusOK},
	}}
	transport := NewTransport(TransportOptions{Base: base, Clock: clock, MaxWait: time.Hour})

	response, err := transport.RoundTrip(newRequest(t, context.Background()))
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	got := clock.sleeps()
	if len(got) != 1 || got[0] != 42*time.Second {
		t.Fatalf("sleeps = %v, want one sleep of 42s", got)
	}
}

func TestTransportRetriesForbiddenWithAnExhaustedBudget(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	base := &stubTransport{responses: []stubResponse{
		{status: http.StatusForbidden, headers: map[string]string{
			"x-ratelimit-remaining": "0",
			"x-ratelimit-reset":     resetHeader(clock, 5*time.Minute),
		}},
		{status: http.StatusOK},
	}}
	transport := NewTransport(TransportOptions{Base: base, Clock: clock, MaxWait: time.Hour})

	response, err := transport.RoundTrip(newRequest(t, context.Background()))
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if got := clock.sleeps(); len(got) != 1 || got[0] != 5*time.Minute {
		t.Fatalf("sleeps = %v, want one sleep of 5m", got)
	}
}

func TestTransportReturnsAPlainForbiddenUnchanged(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	base := &stubTransport{responses: []stubResponse{{status: http.StatusForbidden}}}
	transport := NewTransport(TransportOptions{Base: base, Clock: clock, MaxWait: time.Hour})

	response, err := transport.RoundTrip(newRequest(t, context.Background()))
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.StatusCode)
	}
	if base.calls != 1 {
		t.Fatalf("calls = %d, want a permission error not to be retried", base.calls)
	}
	if got := clock.sleeps(); len(got) != 0 {
		t.Fatalf("slept %v, want none", got)
	}
}

func TestTransportRetriesServerErrorsWithExponentialBackoff(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	base := &stubTransport{responses: []stubResponse{
		{status: http.StatusBadGateway},
		{status: http.StatusServiceUnavailable},
		{status: http.StatusOK},
	}}
	transport := NewTransport(TransportOptions{Base: base, Clock: clock, MaxWait: time.Hour})

	response, err := transport.RoundTrip(newRequest(t, context.Background()))
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer response.Body.Close()

	want := []time.Duration{time.Second, 2 * time.Second}
	got := clock.sleeps()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("sleeps = %v, want %v", got, want)
	}
}

func TestTransportRetriesATransportError(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	base := &stubTransport{responses: []stubResponse{
		{err: errors.New("connection reset by peer")},
		{status: http.StatusOK},
	}}
	transport := NewTransport(TransportOptions{Base: base, Clock: clock, MaxWait: time.Hour})

	response, err := transport.RoundTrip(newRequest(t, context.Background()))
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	response.Body.Close()
	if base.calls != 2 {
		t.Fatalf("calls = %d, want 2", base.calls)
	}
}

func TestTransportGivesUpAfterTheAttemptLimit(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	base := &stubTransport{responses: []stubResponse{
		{status: http.StatusBadGateway},
		{status: http.StatusBadGateway},
		{status: http.StatusBadGateway},
	}}
	transport := NewTransport(TransportOptions{Base: base, Clock: clock, MaxAttempts: 3, MaxWait: time.Hour})

	response, err := transport.RoundTrip(newRequest(t, context.Background()))
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want the last response returned as-is", response.StatusCode)
	}
	if base.calls != 3 {
		t.Fatalf("calls = %d, want 3", base.calls)
	}
}

func TestTransportFailsRatherThanWaitingLongerThanMaxWait(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	base := &stubTransport{responses: []stubResponse{
		{status: http.StatusTooManyRequests, headers: map[string]string{"retry-after": "3600"}},
	}}
	transport := NewTransport(TransportOptions{Base: base, Clock: clock, MaxWait: time.Minute})

	_, err := transport.RoundTrip(newRequest(t, context.Background()))
	if !errors.Is(err, ErrWaitTooLong) {
		t.Fatalf("error = %v, want ErrWaitTooLong", err)
	}
}

func TestTransportKeepsANewerHoldArmedWhileWaitingOutAnOlderOne(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	transport := NewTransport(TransportOptions{Base: &stubTransport{}, Clock: clock, MaxWait: time.Hour})

	// Arm an old hold, then simulate a concurrent response arming a later one
	// while the first wait is still outstanding.
	old := clock.Now().Add(time.Minute)
	newer := clock.Now().Add(time.Hour)
	transport.mu.Lock()
	transport.holdUntil = old
	transport.mu.Unlock()

	clock.onSleep = func() {
		transport.mu.Lock()
		transport.holdUntil = newer
		transport.mu.Unlock()
	}
	if err := transport.honourHold(context.Background()); err != nil {
		t.Fatalf("honour hold: %v", err)
	}
	clock.onSleep = nil

	transport.mu.Lock()
	remaining := transport.holdUntil
	transport.mu.Unlock()
	if !remaining.Equal(newer) {
		t.Fatalf("holdUntil = %v, want the newer hold %v to survive", remaining, newer)
	}
}

func TestTransportReportsWaitsToTheNotifyHook(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	base := &stubTransport{responses: []stubResponse{
		{status: http.StatusTooManyRequests, headers: map[string]string{"retry-after": "9"}},
		{status: http.StatusOK},
	}}
	var waits []Wait
	transport := NewTransport(TransportOptions{
		Base:    base,
		Clock:   clock,
		MaxWait: time.Hour,
		Notify:  func(wait Wait) { waits = append(waits, wait) },
	})

	response, err := transport.RoundTrip(newRequest(t, context.Background()))
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	response.Body.Close()

	if len(waits) != 1 || waits[0].Duration != 9*time.Second || waits[0].Reason == "" {
		t.Fatalf("waits = %#v, want one described wait of 9s", waits)
	}
}

func TestTransportStopsWaitingWhenTheContextIsCancelled(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	base := &stubTransport{responses: []stubResponse{
		{status: http.StatusTooManyRequests, headers: map[string]string{"retry-after": "30"}},
	}}
	transport := NewTransport(TransportOptions{Base: base, Clock: clock, MaxWait: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	request := newRequest(t, ctx)
	cancel()

	_, err := transport.RoundTrip(request)

	// All three assertions together pin the behaviour. Asserting only that err is
	// non-nil would pass even if the context were ignored, because the stub runs
	// out of scripted responses and errors for an unrelated reason.
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if base.calls != 1 {
		t.Fatalf("calls = %d, want the cancellation to abort before a retry", base.calls)
	}
	if got := clock.sleeps(); len(got) != 0 {
		t.Fatalf("slept %v, want the wait to be refused outright", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/ratelimit/ -run Transport -v`
Expected: FAIL, the package does not compile because `NewTransport`, `TransportOptions`, `Snapshot`, `Wait` and `ErrWaitTooLong` are undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/ratelimit/transport.go`:

```go
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

// clearHold releases a hold only if it is still the one we waited out. A newer
// hold armed by a concurrent response must not be silently discarded, or a
// later request skips a wait it should have honoured.
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/ratelimit/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ratelimit/transport.go internal/ratelimit/transport_test.go
git commit -m "feat: add rate-limit aware HTTP transport

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Transient git failure classification and retry

Distinguish a dropped connection from a real error, and retry only the first kind.

**Files:**
- Create: `internal/mirror/retry.go`
- Create: `internal/mirror/retry_test.go`
- Modify: `internal/mirror/service.go` (add the `retry` field, add `runGit`, use it for clone and fetch)
- Modify: `internal/mirror/service_test.go` (append the new cases)

**Interfaces:**
- Consumes: `Runner` and `Service` from `internal/mirror`.
- Produces:
  - `func IsTransientGitFailure(output string, err error) bool`
  - `type RetryPolicy struct { Attempts int; Backoff func(attempt int) time.Duration; Sleep func(ctx context.Context, d time.Duration) error }`
  - `func (p RetryPolicy) attempts() int`
  - `func (s *Service) runGit(ctx context.Context, dir string, args ...string) (string, error)` — unexported, used by `Sync`.
  - `func NewServiceWithOptions(runner Runner, source RepositorySource, retry RetryPolicy) *Service`

- [ ] **Step 1: Write the failing tests**

Create `internal/mirror/retry_test.go`:

```go
package mirror

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestIsTransientGitFailure(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{name: "connection reset", output: "fatal: unable to access 'https://github.com/acme/api.git/': Recv failure: Connection reset by peer", want: true},
		{name: "early EOF", output: "fatal: the remote end hung up unexpectedly\nfatal: early EOF", want: true},
		{name: "rpc failed", output: "error: RPC failed; curl 92 HTTP/2 stream 5 was not closed cleanly", want: true},
		{name: "unresolved host", output: "fatal: unable to access 'https://github.com/acme/api.git/': Could not resolve host: github.com", want: true},
		{name: "timeout", output: "fatal: unable to access 'https://github.com/acme/api.git/': Operation timed out after 30000 milliseconds", want: true},
		{name: "server error", output: "error: RPC failed; HTTP 502 curl 22 The requested URL returned error: 502", want: true},
		{name: "too many requests", output: "error: RPC failed; HTTP 429 curl 22 The requested URL returned error: 429", want: true},
		{name: "tls handshake", output: "fatal: unable to access 'https://github.com/acme/api.git/': OpenSSL SSL_read: Connection was reset", want: true},
		{name: "authentication", output: "remote: Invalid username or password.\nfatal: Authentication failed for 'https://github.com/acme/api.git/'", want: false},
		{name: "missing repository", output: "remote: Repository not found.\nfatal: repository 'https://github.com/acme/api.git/' not found", want: false},
		{name: "permission denied", output: "remote: Permission to acme/api.git denied to nobody.", want: false},
		{name: "not fast forward", output: "fatal: Not possible to fast-forward, aborting.", want: false},
		{name: "empty", output: "", want: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := IsTransientGitFailure(testCase.output, errors.New("exit status 128")); got != testCase.want {
				t.Fatalf("IsTransientGitFailure(%q) = %v, want %v", testCase.output, got, testCase.want)
			}
		})
	}
}

func TestIsTransientGitFailureIsFalseWithoutAnError(t *testing.T) {
	if IsTransientGitFailure("Connection reset by peer", nil) {
		t.Fatal("a successful command is never a transient failure")
	}
}

func TestSyncRetriesATransientCloneFailure(t *testing.T) {
	root := t.TempDir()
	repository := Repository{Name: "api", NameWithOwner: "acme/api", CloneURL: "https://github.com/acme/api.git"}
	path := filepath.Join(root, "api")
	cloneKey := commandKey("", "git", []string{"clone", repository.CloneURL, path})
	runner := &scriptedRunner{responses: map[string]scriptedResponse{
		cloneKey: {output: "fatal: early EOF", err: errors.New("exit status 128")},
		commandKey(path, "git", []string{"rev-parse", "HEAD"}): {output: "local-sha\n"},
		commandKey(path, "git", []string{"rev-parse", "@{u}"}): {output: "remote-sha\n"},
	}}
	runner.succeedAfter = map[string]int{cloneKey: 2}

	var slept []time.Duration
	service := NewServiceWithOptions(runner, nil, RetryPolicy{
		Attempts: 3,
		Backoff:  func(attempt int) time.Duration { return time.Duration(attempt) * time.Second },
		Sleep: func(ctx context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		},
	})

	result := service.Sync(context.Background(), repository, root, false)
	if result.Outcome != OutcomeCloned {
		t.Fatalf("outcome = %s (%s), want cloned", result.Outcome, result.Message)
	}
	if len(slept) != 1 || slept[0] != time.Second {
		t.Fatalf("slept %v, want one backoff of 1s", slept)
	}
}

func TestSyncDoesNotRetryAPermanentCloneFailure(t *testing.T) {
	root := t.TempDir()
	repository := Repository{Name: "api", NameWithOwner: "acme/api", CloneURL: "https://github.com/acme/api.git"}
	path := filepath.Join(root, "api")
	cloneKey := commandKey("", "git", []string{"clone", repository.CloneURL, path})
	runner := &scriptedRunner{responses: map[string]scriptedResponse{
		cloneKey: {output: "remote: Repository not found.", err: errors.New("exit status 128")},
	}}

	service := NewServiceWithOptions(runner, nil, RetryPolicy{
		Attempts: 3,
		Backoff:  func(int) time.Duration { return time.Second },
		Sleep:    func(context.Context, time.Duration) error { return nil },
	})

	result := service.Sync(context.Background(), repository, root, false)
	if result.Outcome != OutcomeError {
		t.Fatalf("outcome = %s, want error", result.Outcome)
	}
	if got := runner.countFor(cloneKey); got != 1 {
		t.Fatalf("clone attempted %d times, want 1", got)
	}
}

func TestSyncGivesUpAfterTheRetryBudget(t *testing.T) {
	root := t.TempDir()
	repository := Repository{Name: "api", NameWithOwner: "acme/api", CloneURL: "https://github.com/acme/api.git"}
	path := filepath.Join(root, "api")
	cloneKey := commandKey("", "git", []string{"clone", repository.CloneURL, path})
	runner := &scriptedRunner{responses: map[string]scriptedResponse{
		cloneKey: {output: "fatal: early EOF", err: errors.New("exit status 128")},
	}}

	service := NewServiceWithOptions(runner, nil, RetryPolicy{
		Attempts: 3,
		Backoff:  func(int) time.Duration { return time.Second },
		Sleep:    func(context.Context, time.Duration) error { return nil },
	})

	result := service.Sync(context.Background(), repository, root, false)
	if result.Outcome != OutcomeError {
		t.Fatalf("outcome = %s, want error", result.Outcome)
	}
	if got := runner.countFor(cloneKey); got != 3 {
		t.Fatalf("clone attempted %d times, want 3", got)
	}
}
```

Now extend the existing fake runner. In `internal/mirror/service_test.go`, find the `scriptedRunner` type and its `Run` method and replace them with this version, which counts calls and can succeed after a given attempt. Keep every other line of the file as it is.

```go
type scriptedRunner struct {
	responses map[string]scriptedResponse
	calls     []call
	counts    map[string]int
	// succeedAfter maps a command key to the attempt number on which it starts
	// succeeding, so a transient failure can be scripted.
	succeedAfter map[string]int
}

func (r *scriptedRunner) countFor(key string) int {
	return r.counts[key]
}

func (r *scriptedRunner) Run(_ context.Context, dir, name string, args ...string) (string, error) {
	r.calls = append(r.calls, call{dir: dir, name: name, args: args})
	key := commandKey(dir, name, args)
	if r.counts == nil {
		r.counts = map[string]int{}
	}
	r.counts[key]++
	if after, ok := r.succeedAfter[key]; ok && r.counts[key] >= after {
		return "", nil
	}
	response, ok := r.responses[key]
	if !ok {
		return "", fmt.Errorf("unexpected command: %s", key)
	}
	return response.output, response.err
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/mirror/ -run 'Transient|Retr|GivesUp' -v`
Expected: FAIL, `IsTransientGitFailure`, `RetryPolicy` and `NewServiceWithOptions` are undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/mirror/retry.go`:

```go
package mirror

import (
	"context"
	"strings"
	"time"
)

// transientGitMessages are substrings of git's own output that mean the
// network failed rather than the request being wrong. Retrying these is safe;
// retrying anything else wastes time and can look like abuse.
var transientGitMessages = []string{
	"connection reset",
	"connection was reset",
	"connection refused",
	"early eof",
	"the remote end hung up",
	"rpc failed",
	"could not resolve host",
	"could not resolve proxy",
	"operation timed out",
	"timed out",
	"network is unreachable",
	"temporary failure in name resolution",
	"gnutls_handshake() failed",
	"ssl_read",
	"http 429",
	"http 500",
	"http 502",
	"http 503",
	"http 504",
	"unexpected disconnect",
	"transfer closed",
	"empty reply from server",
}

// IsTransientGitFailure reports whether a failed git command is worth retrying.
// It is false when err is nil, because a command that succeeded is not a failure.
func IsTransientGitFailure(output string, err error) bool {
	if err == nil {
		return false
	}
	haystack := strings.ToLower(output + " " + err.Error())
	for _, message := range transientGitMessages {
		if strings.Contains(haystack, message) {
			return true
		}
	}
	return false
}

// RetryPolicy governs how often a transient git failure is retried.
// The zero value performs a single attempt and never sleeps.
type RetryPolicy struct {
	// Attempts counts the first try. Values below 1 mean 1.
	Attempts int
	// Backoff returns how long to wait before the given attempt, counting from 1.
	Backoff func(attempt int) time.Duration
	// Sleep performs the wait and must honour the context.
	Sleep func(ctx context.Context, d time.Duration) error
}

func (p RetryPolicy) attempts() int {
	if p.Attempts < 1 {
		return 1
	}
	return p.Attempts
}

func (p RetryPolicy) wait(ctx context.Context, attempt int) error {
	if p.Backoff == nil || p.Sleep == nil {
		return nil
	}
	delay := p.Backoff(attempt)
	if delay <= 0 {
		return nil
	}
	return p.Sleep(ctx, delay)
}
```

Modify `internal/mirror/service.go`. Add the field and the constructor next to the existing `Service` and `NewService`:

```go
type Service struct {
	runner Runner
	source RepositorySource
	retry  RetryPolicy
}

// NewServiceWithOptions builds a service that retries transient git failures.
// A nil source means discovery is unavailable, which suits Sync-only callers.
func NewServiceWithOptions(runner Runner, source RepositorySource, retry RetryPolicy) *Service {
	return &Service{runner: runner, source: source, retry: retry}
}
```

Leave `NewService` exactly as it is; it produces a service with the zero `RetryPolicy`, which attempts once.

Add `runGit` to the same file:

```go
// runGit runs a network-touching git command, retrying transient failures.
// Local commands call the runner directly, because a local failure is real.
func (s *Service) runGit(ctx context.Context, dir string, args ...string) (string, error) {
	attempts := s.retry.attempts()
	var output string
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		output, err = s.runner.Run(ctx, dir, "git", args...)
		if err == nil {
			return output, nil
		}
		if attempt == attempts || !IsTransientGitFailure(output, err) {
			return output, err
		}
		if waitErr := s.retry.wait(ctx, attempt); waitErr != nil {
			return output, waitErr
		}
	}
	return output, err
}
```

In `Service.Sync`, replace the clone call

```go
		if _, err := s.runner.Run(ctx, "", "git", "clone", repository.CloneURL, path); err != nil {
```

with

```go
		if _, err := s.runGit(ctx, "", "clone", repository.CloneURL, path); err != nil {
```

and replace the fetch call

```go
	if _, err := s.runner.Run(ctx, path, "git", "fetch", "origin"); err != nil {
```

with

```go
	if _, err := s.runGit(ctx, path, "fetch", "origin"); err != nil {
```

Leave `git status`, `git merge --ff-only` and both `git rev-parse` calls on `s.runner.Run`. They are local operations, so a failure is a real answer rather than a network hiccup.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/mirror/ -v`
Expected: PASS, including the pre-existing tests, which still use `NewService`.

- [ ] **Step 5: Commit**

```bash
git add internal/mirror/retry.go internal/mirror/retry_test.go internal/mirror/service.go internal/mirror/service_test.go
git commit -m "feat: retry transient git failures with backoff

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Pacing, the skipped outcome, and the waiting progress event

The mirror loop learns to wait between repositories, to skip ones already done, to stop at a cap, and to report a wait so the interface does not look hung.

**Files:**
- Modify: `internal/mirror/model.go` (add `OutcomeSkipped`)
- Modify: `internal/mirror/progress.go` (add `ProgressWaiting`, `Message`, `Until`)
- Modify: `internal/mirror/service.go` (add `MirrorOptions` and `MirrorWithOptions`)
- Modify: `internal/mirror/service_test.go` (append the new cases)
- Modify: `internal/mirror/model_test.go` (assert the new outcome value)

**Interfaces:**
- Consumes: `RetryPolicy` and `NewServiceWithOptions` from Task 3.
- Produces:
  - `const OutcomeSkipped Outcome = "skipped"`
  - `const ProgressWaiting ProgressKind = "waiting"`
  - `ProgressEvent` gains `Message string` and `Until time.Time`.
  - `type Waiter interface { Wait(ctx context.Context) error }`
  - `type MirrorOptions struct { Report ProgressFunc; Pacer Waiter; Skip map[string]string; Limit int; OnResult func(Result) error }`
  - `func (s *Service) MirrorWithOptions(ctx context.Context, organization, root string, dryRun bool, options MirrorOptions) (Metadata, error)`
  - `func (s *Service) Mirror(...)` and `MirrorWithProgress(...)` keep their signatures and delegate.

`OnResult` is the checkpoint hook: Task 8 passes the database writer through it, and an error from it aborts the run, because losing the checkpoint defeats resume.

- [ ] **Step 1: Write the failing tests**

Append to `internal/mirror/service_test.go`:

```go
type countingWaiter struct {
	waits int
	err   error
}

func (w *countingWaiter) Wait(context.Context) error {
	w.waits++
	return w.err
}

func threeRepositories() *scriptedRepositorySource {
	return &scriptedRepositorySource{repositories: []Repository{
		{Name: "one", NameWithOwner: "acme/one", CloneURL: "https://github.com/acme/one.git"},
		{Name: "two", NameWithOwner: "acme/two", CloneURL: "https://github.com/acme/two.git"},
		{Name: "three", NameWithOwner: "acme/three", CloneURL: "https://github.com/acme/three.git"},
	}}
}

func clonesFor(root string, names ...string) map[string]scriptedResponse {
	responses := map[string]scriptedResponse{}
	for _, name := range names {
		path := filepath.Join(root, "acme", name)
		url := "https://github.com/acme/" + name + ".git"
		responses[commandKey("", "git", []string{"clone", url, path})] = scriptedResponse{}
		responses[commandKey(path, "git", []string{"rev-parse", "HEAD"})] = scriptedResponse{output: "local\n"}
		responses[commandKey(path, "git", []string{"rev-parse", "@{u}"})] = scriptedResponse{output: "remote\n"}
	}
	return responses
}

func TestMirrorPacesEveryRepositoryThatRunsGit(t *testing.T) {
	root := t.TempDir()
	runner := &scriptedRunner{responses: clonesFor(root, "one", "two", "three")}
	waiter := &countingWaiter{}

	metadata, err := NewService(runner, threeRepositories()).MirrorWithOptions(
		context.Background(), "acme", root, false, MirrorOptions{Pacer: waiter})
	if err != nil {
		t.Fatalf("mirror: %v", err)
	}
	if len(metadata.Repositories) != 3 {
		t.Fatalf("results = %d, want 3", len(metadata.Repositories))
	}
	if waiter.waits != 3 {
		t.Fatalf("waits = %d, want one per repository", waiter.waits)
	}
}

func TestMirrorPacesAfterAFailedRepository(t *testing.T) {
	root := t.TempDir()
	responses := clonesFor(root, "two", "three")
	failedPath := filepath.Join(root, "acme", "one")
	responses[commandKey("", "git", []string{"clone", "https://github.com/acme/one.git", failedPath})] = scriptedResponse{
		output: "remote: Repository not found.", err: errors.New("exit status 128"),
	}
	runner := &scriptedRunner{responses: responses}
	waiter := &countingWaiter{}

	metadata, err := NewService(runner, threeRepositories()).MirrorWithOptions(
		context.Background(), "acme", root, false, MirrorOptions{Pacer: waiter})
	if err != nil {
		t.Fatalf("mirror: %v", err)
	}
	if metadata.Repositories[0].Outcome != OutcomeError {
		t.Fatalf("first outcome = %s, want error", metadata.Repositories[0].Outcome)
	}
	if waiter.waits != 3 {
		t.Fatalf("waits = %d, want the error path to pace like any other", waiter.waits)
	}
}

func TestMirrorSkipsRepositoriesRecordedInAnEarlierRun(t *testing.T) {
	root := t.TempDir()
	runner := &scriptedRunner{responses: clonesFor(root, "two", "three")}
	waiter := &countingWaiter{}

	metadata, err := NewService(runner, threeRepositories()).MirrorWithOptions(
		context.Background(), "acme", root, false, MirrorOptions{
			Pacer: waiter,
			Skip:  map[string]string{"acme/one": "completed in run 7"},
		})
	if err != nil {
		t.Fatalf("mirror: %v", err)
	}
	if metadata.Repositories[0].Outcome != OutcomeSkipped {
		t.Fatalf("first outcome = %s, want skipped", metadata.Repositories[0].Outcome)
	}
	if metadata.Repositories[0].Message != "completed in run 7" {
		t.Fatalf("message = %q", metadata.Repositories[0].Message)
	}
	if waiter.waits != 2 {
		t.Fatalf("waits = %d, want a skip not to pace", waiter.waits)
	}
}

func TestMirrorStopsAtTheRepositoryLimit(t *testing.T) {
	root := t.TempDir()
	runner := &scriptedRunner{responses: clonesFor(root, "one", "two")}

	metadata, err := NewService(runner, threeRepositories()).MirrorWithOptions(
		context.Background(), "acme", root, false, MirrorOptions{Limit: 2})
	if err != nil {
		t.Fatalf("mirror: %v", err)
	}
	if len(metadata.Repositories) != 2 {
		t.Fatalf("results = %d, want 2", len(metadata.Repositories))
	}
	if !metadata.Truncated {
		t.Fatal("a capped run must report that work remains")
	}
}

func TestMirrorLimitCountsOnlyProcessedRepositories(t *testing.T) {
	root := t.TempDir()
	runner := &scriptedRunner{responses: clonesFor(root, "two")}

	metadata, err := NewService(runner, threeRepositories()).MirrorWithOptions(
		context.Background(), "acme", root, false, MirrorOptions{
			Limit: 1,
			Skip:  map[string]string{"acme/one": "already done"},
		})
	if err != nil {
		t.Fatalf("mirror: %v", err)
	}
	if len(metadata.Repositories) != 2 {
		t.Fatalf("results = %d, want the skip plus one processed repository", len(metadata.Repositories))
	}
	if metadata.Repositories[1].Repository.Name != "two" {
		t.Fatalf("processed %q, want two", metadata.Repositories[1].Repository.Name)
	}
	if !metadata.Truncated {
		t.Fatal("a capped run must report that work remains")
	}
}

func TestMirrorCallsTheResultHookForEveryRepository(t *testing.T) {
	root := t.TempDir()
	runner := &scriptedRunner{responses: clonesFor(root, "two", "three")}
	var recorded []string

	_, err := NewService(runner, threeRepositories()).MirrorWithOptions(
		context.Background(), "acme", root, false, MirrorOptions{
			Skip: map[string]string{"acme/one": "already done"},
			OnResult: func(result Result) error {
				recorded = append(recorded, string(result.Outcome)+" "+result.Repository.Name)
				return nil
			},
		})
	if err != nil {
		t.Fatalf("mirror: %v", err)
	}
	want := []string{"skipped one", "cloned two", "cloned three"}
	if len(recorded) != len(want) {
		t.Fatalf("recorded %v, want %v", recorded, want)
	}
	for index := range want {
		if recorded[index] != want[index] {
			t.Fatalf("recorded %v, want %v", recorded, want)
		}
	}
}

func TestMirrorAbortsWhenTheResultHookFails(t *testing.T) {
	root := t.TempDir()
	runner := &scriptedRunner{responses: clonesFor(root, "one")}

	_, err := NewService(runner, threeRepositories()).MirrorWithOptions(
		context.Background(), "acme", root, false, MirrorOptions{
			OnResult: func(Result) error { return errors.New("disk full") },
		})
	if err == nil {
		t.Fatal("a failed checkpoint must abort the run")
	}
}

func TestMirrorStopsWhenThePacerReportsCancellation(t *testing.T) {
	root := t.TempDir()
	runner := &scriptedRunner{responses: clonesFor(root, "one", "two", "three")}
	waiter := &countingWaiter{err: context.Canceled}

	_, err := NewService(runner, threeRepositories()).MirrorWithOptions(
		context.Background(), "acme", root, false, MirrorOptions{Pacer: waiter})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
```

Add `"errors"` to that file's import block if it is not already there.

Append to `internal/mirror/model_test.go`:

```go
func TestSkippedOutcomeHasItsWireValue(t *testing.T) {
	if OutcomeSkipped != "skipped" {
		t.Fatalf("OutcomeSkipped = %q, want skipped", OutcomeSkipped)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/mirror/ -run 'Paces|Skips|Limit|Hook|Cancellation|SkippedOutcome' -v`
Expected: FAIL, `MirrorOptions`, `MirrorWithOptions`, `OutcomeSkipped` and `Metadata.Truncated` are undefined.

- [ ] **Step 3: Write the implementation**

In `internal/mirror/model.go`, add the outcome to the existing block and the field to `Metadata`:

```go
const (
	OutcomeCloned              Outcome = "cloned"
	OutcomeUpdated             Outcome = "updated"
	OutcomeUnchanged           Outcome = "unchanged"
	OutcomeConflict            Outcome = "conflict"
	OutcomeError               Outcome = "error"
	OutcomeSkipped             Outcome = "skipped"
	OutcomeAbsentFromDiscovery Outcome = "absent_from_discovery"
)

type Metadata struct {
	Organization string    `json:"organization"`
	GeneratedAt  time.Time `json:"generatedAt"`
	Repositories []Result  `json:"repositories"`
	// Truncated reports that a repository cap stopped the run before every
	// repository was processed, so the next run has work to continue.
	Truncated bool `json:"truncated,omitempty"`
}
```

In `internal/mirror/progress.go`, add the kind and the two fields:

```go
const (
	ProgressDiscoveryStarted    ProgressKind = "discovery_started"
	ProgressDiscoveryCompleted  ProgressKind = "discovery_completed"
	ProgressRepositoryStarted   ProgressKind = "repository_started"
	ProgressRepositoryCompleted ProgressKind = "repository_completed"
	ProgressMetadataWritten     ProgressKind = "metadata_written"
	ProgressWaiting             ProgressKind = "waiting"
)

type ProgressEvent struct {
	Kind         ProgressKind
	Organization string
	Repository   Repository
	Result       Result
	Completed    int
	Total        int
	Path         string
	// Message explains a ProgressWaiting event in one line.
	Message string
	// Until is when a ProgressWaiting event expects to resume. Zero when unknown.
	Until time.Time
}
```

Add `"time"` to that file's imports.

In `internal/mirror/service.go`, replace `Mirror` and `MirrorWithProgress` with the three functions below, keeping their existing signatures intact:

```go
// Waiter paces work. *ratelimit.Pacer satisfies it; the interface keeps this
// package free of that dependency.
type Waiter interface {
	Wait(ctx context.Context) error
}

// MirrorOptions configures one mirror run.
type MirrorOptions struct {
	// Report receives progress events. Nil disables reporting.
	Report ProgressFunc
	// Pacer, when set, is waited on before each repository that runs git.
	Pacer Waiter
	// Skip maps a repository's full name to the reason it is being skipped.
	Skip map[string]string
	// Limit caps how many repositories this run processes, not counting skips.
	// Zero means no cap.
	Limit int
	// OnResult is called with every result as it is produced, before the next
	// repository starts. An error aborts the run, because a checkpoint that
	// cannot be written makes resume unsafe.
	OnResult func(Result) error
}

func (s *Service) Mirror(ctx context.Context, organization, root string, dryRun bool) (Metadata, error) {
	return s.MirrorWithOptions(ctx, organization, root, dryRun, MirrorOptions{})
}

func (s *Service) MirrorWithProgress(ctx context.Context, organization, root string, dryRun bool, report ProgressFunc) (Metadata, error) {
	return s.MirrorWithOptions(ctx, organization, root, dryRun, MirrorOptions{Report: report})
}

func (s *Service) MirrorWithOptions(ctx context.Context, organization, root string, dryRun bool, options MirrorOptions) (Metadata, error) {
	report := options.Report
	reportProgress(report, ProgressEvent{Kind: ProgressDiscoveryStarted, Organization: organization})
	repositories, err := s.Discover(ctx, organization)
	if err != nil {
		return Metadata{}, err
	}
	total := len(repositories)
	reportProgress(report, ProgressEvent{Kind: ProgressDiscoveryCompleted, Organization: organization, Total: total})

	metadata := Metadata{
		Organization: organization,
		GeneratedAt:  time.Now().UTC(),
		Repositories: make([]Result, 0, total),
	}
	organizationRoot := filepath.Join(root, organization)
	processed := 0

	for index, repository := range repositories {
		if reason, skip := options.Skip[repository.NameWithOwner]; skip {
			result := Result{
				Repository: repository,
				Path:       filepath.Join(organizationRoot, repository.Name),
				Outcome:    OutcomeSkipped,
				Message:    reason,
			}
			metadata.Repositories = append(metadata.Repositories, result)
			if err := notifyResult(options.OnResult, result); err != nil {
				return metadata, err
			}
			reportProgress(report, ProgressEvent{Kind: ProgressRepositoryCompleted, Organization: organization, Repository: repository, Result: result, Completed: index + 1, Total: total})
			continue
		}
		if options.Limit > 0 && processed >= options.Limit {
			metadata.Truncated = true
			break
		}
		if options.Pacer != nil {
			if err := options.Pacer.Wait(ctx); err != nil {
				return metadata, err
			}
		}

		reportProgress(report, ProgressEvent{Kind: ProgressRepositoryStarted, Organization: organization, Repository: repository, Completed: index, Total: total})
		result := s.Sync(ctx, repository, organizationRoot, dryRun)
		processed++
		metadata.Repositories = append(metadata.Repositories, result)
		if err := notifyResult(options.OnResult, result); err != nil {
			return metadata, err
		}
		reportProgress(report, ProgressEvent{Kind: ProgressRepositoryCompleted, Organization: organization, Repository: repository, Result: result, Completed: index + 1, Total: total})
	}

	if dryRun {
		return metadata, nil
	}
	if err := WriteMetadata(filepath.Join(organizationRoot, "metadata.json"), metadata); err != nil {
		return metadata, err
	}
	reportProgress(report, ProgressEvent{Kind: ProgressMetadataWritten, Organization: organization, Completed: len(metadata.Repositories), Total: total, Path: filepath.Join(organizationRoot, "metadata.json")})
	return metadata, nil
}

func notifyResult(hook func(Result) error, result Result) error {
	if hook == nil {
		return nil
	}
	return hook(result)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/mirror/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mirror/
git commit -m "feat: pace, skip and cap repositories in the mirror loop

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: History run lifecycle

Checkpoint each repository as it finishes so an interrupted run leaves a resumable record.

**Files:**
- Modify: `internal/history/database.go`
- Modify: `internal/history/database_test.go`

**Interfaces:**
- Consumes: `mirror.Result`, `mirror.Metadata` and the outcome constants including `OutcomeSkipped` from Task 4.
- Produces:
  - `type Status string` with `StatusRunning`, `StatusCompleted`, `StatusFailed`, `StatusInterrupted`.
  - `func (d *Database) StartRun(organization string, started time.Time, dryRun bool) (*Run, error)`
  - `func (d *Database) ResumableRun(organization string) (*Run, bool, error)`
  - `func (r *Run) ID() int64`
  - `func (r *Run) CompletedRepositories() (map[string]string, error)` — full name to the reason string a skip will carry.
  - `func (r *Run) RecordRepository(result mirror.Result, at time.Time) error`
  - `func (r *Run) Finish(status Status, finished time.Time, runErr error) error`
  - `Database.Record` keeps its signature and delegates to the lifecycle.

- [ ] **Step 1: Write the failing tests**

Append to `internal/history/database_test.go`:

```go
func TestStartRunRecordsARunningRow(t *testing.T) {
	database, err := Open(t.TempDir() + "/database.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	run, err := database.StartRun("acme", time.Unix(1, 0), false)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if run.ID() == 0 {
		t.Fatal("a started run must have an identity")
	}

	var status string
	var count int
	if err := database.db.QueryRow("SELECT status, repository_count FROM sync_runs WHERE id = ?", run.ID()).Scan(&status, &count); err != nil {
		t.Fatal(err)
	}
	if status != string(StatusRunning) || count != 0 {
		t.Fatalf("status=%q count=%d, want running and 0", status, count)
	}
}

func TestFinishRunSetsTheStatusAndTheRepositoryCount(t *testing.T) {
	database, err := Open(t.TempDir() + "/database.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	run, err := database.StartRun("acme", time.Unix(1, 0), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.RecordRepository(resultFor("acme/one", mirror.OutcomeCloned), time.Unix(2, 0)); err != nil {
		t.Fatal(err)
	}
	if err := run.Finish(StatusCompleted, time.Unix(3, 0), nil); err != nil {
		t.Fatalf("finish: %v", err)
	}

	var status string
	var count int
	var completedAt string
	if err := database.db.QueryRow("SELECT status, repository_count, completed_at FROM sync_runs WHERE id = ?", run.ID()).Scan(&status, &count, &completedAt); err != nil {
		t.Fatal(err)
	}
	if status != string(StatusCompleted) || count != 1 || completedAt == "" {
		t.Fatalf("status=%q count=%d completedAt=%q", status, count, completedAt)
	}
}

func TestFinishRunStoresTheErrorText(t *testing.T) {
	database, err := Open(t.TempDir() + "/database.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	run, err := database.StartRun("acme", time.Unix(1, 0), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Finish(StatusFailed, time.Unix(3, 0), errors.New("discovery failed")); err != nil {
		t.Fatal(err)
	}

	var text string
	if err := database.db.QueryRow("SELECT error FROM sync_runs WHERE id = ?", run.ID()).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if text != "discovery failed" {
		t.Fatalf("error = %q", text)
	}
}

func TestResumableRunFindsAnInterruptedRunAndItsCompletedRepositories(t *testing.T) {
	path := t.TempDir() + "/database.db"
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	run, err := database.StartRun("acme", time.Unix(1, 0), false)
	if err != nil {
		t.Fatal(err)
	}
	for name, outcome := range map[string]mirror.Outcome{
		"acme/one":   mirror.OutcomeCloned,
		"acme/two":   mirror.OutcomeConflict,
		"acme/three": mirror.OutcomeError,
	} {
		if err := run.RecordRepository(resultFor(name, outcome), time.Unix(2, 0)); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate a crash: the process dies without finishing the run.
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	resumed, found, err := reopened.ResumableRun("acme")
	if err != nil {
		t.Fatalf("find resumable run: %v", err)
	}
	if !found || resumed.ID() != run.ID() {
		t.Fatalf("found=%v id=%d, want the interrupted run", found, resumed.ID())
	}

	completed, err := resumed.CompletedRepositories()
	if err != nil {
		t.Fatal(err)
	}
	if len(completed) != 2 {
		t.Fatalf("completed = %v, want the cloned and conflicted repositories only", completed)
	}
	if _, ok := completed["acme/three"]; ok {
		t.Fatal("an errored repository must be retried, not skipped")
	}
	if completed["acme/one"] == "" {
		t.Fatal("a skip reason must explain where the repository was completed")
	}
}

func TestResumableRunIgnoresFinishedAndDryRuns(t *testing.T) {
	database, err := Open(t.TempDir() + "/database.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	finished, err := database.StartRun("acme", time.Unix(1, 0), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := finished.Finish(StatusCompleted, time.Unix(2, 0), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := database.StartRun("acme", time.Unix(3, 0), true); err != nil {
		t.Fatal(err)
	}
	if _, err := database.StartRun("other", time.Unix(4, 0), false); err != nil {
		t.Fatal(err)
	}

	if _, found, err := database.ResumableRun("acme"); err != nil || found {
		t.Fatalf("found=%v err=%v, want no resumable run", found, err)
	}
}

func TestResumableRunPrefersTheMostRecentInterruptedRun(t *testing.T) {
	database, err := Open(t.TempDir() + "/database.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if _, err := database.StartRun("acme", time.Unix(1, 0), false); err != nil {
		t.Fatal(err)
	}
	newer, err := database.StartRun("acme", time.Unix(2, 0), false)
	if err != nil {
		t.Fatal(err)
	}

	resumed, found, err := database.ResumableRun("acme")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if resumed.ID() != newer.ID() {
		t.Fatalf("resumed run %d, want %d", resumed.ID(), newer.ID())
	}
}

func resultFor(fullName string, outcome mirror.Outcome) mirror.Result {
	return mirror.Result{
		Repository: mirror.Repository{Name: fullName, NameWithOwner: fullName, DefaultBranch: "main"},
		Outcome:    outcome,
	}
}
```

Add `"errors"` to that file's import block.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/history/ -v`
Expected: FAIL, `StartRun`, `ResumableRun`, `Run`, `Status` and the status constants are undefined.

- [ ] **Step 3: Write the implementation**

Replace the body of `internal/history/database.go` below the `Open`/`Close` functions with the following, keeping `Open`, `Close`, `errorText` and `schema` as they are:

```go
// Status is the lifecycle state of a sync run.
type Status string

const (
	// StatusRunning marks a run in flight. A run left in this state means the
	// process died before it could finish, so the run is resumable.
	StatusRunning Status = "running"
	// StatusCompleted marks a run that processed every repository.
	StatusCompleted Status = "completed"
	// StatusFailed marks a run that stopped on an error.
	StatusFailed Status = "failed"
	// StatusInterrupted marks a run cancelled by the operator or stopped by a
	// repository cap. It is resumable and is deliberately distinct from failed.
	StatusInterrupted Status = "interrupted"
)

// Run is one sync run's row, open for checkpointing.
type Run struct {
	db *sql.DB
	id int64
}

func (r *Run) ID() int64 { return r.id }

// StartRun opens a run in the running state. The repository count and the
// completion time are filled in by Finish.
func (d *Database) StartRun(organization string, started time.Time, dryRun bool) (*Run, error) {
	result, err := d.db.Exec(
		`INSERT INTO sync_runs (organization, started_at, completed_at, status, dry_run, repository_count, error) VALUES (?, ?, '', ?, ?, 0, NULL)`,
		organization, started.UTC().Format(time.RFC3339Nano), string(StatusRunning), dryRun,
	)
	if err != nil {
		return nil, fmt.Errorf("start sync run: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("start sync run: %w", err)
	}
	return &Run{db: d.db, id: id}, nil
}

// ResumableRun returns the most recent real run for the organization that was
// never finished, which is what a killed process leaves behind, or that the
// operator stopped on purpose with Ctrl+C or a repository cap. A failed run is
// deliberately excluded: it stopped on a real error, so a rerun starts clean.
func (d *Database) ResumableRun(organization string) (*Run, bool, error) {
	var id int64
	err := d.db.QueryRow(
		`SELECT id FROM sync_runs WHERE organization = ? AND status IN (?, ?) AND dry_run = 0 ORDER BY id DESC LIMIT 1`,
		organization, string(StatusRunning), string(StatusInterrupted),
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("find resumable run: %w", err)
	}
	return &Run{db: d.db, id: id}, true, nil
}

// resumableOutcomes are the outcomes that mean a repository does not need to be
// visited again. An error is absent on purpose: it may have been the interruption.
var resumableOutcomes = []mirror.Outcome{
	mirror.OutcomeCloned,
	mirror.OutcomeUpdated,
	mirror.OutcomeUnchanged,
	mirror.OutcomeConflict,
	mirror.OutcomeSkipped,
}

// CompletedRepositories maps each already-finished repository to the reason a
// resumed run will give for skipping it.
func (r *Run) CompletedRepositories() (map[string]string, error) {
	placeholders := make([]string, 0, len(resumableOutcomes))
	arguments := make([]any, 0, len(resumableOutcomes)+1)
	arguments = append(arguments, r.id)
	for _, outcome := range resumableOutcomes {
		placeholders = append(placeholders, "?")
		arguments = append(arguments, string(outcome))
	}
	query := fmt.Sprintf(
		`SELECT full_name, outcome FROM repositories WHERE sync_run_id = ? AND outcome IN (%s)`,
		strings.Join(placeholders, ","),
	)
	rows, err := r.db.Query(query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("read completed repositories: %w", err)
	}
	defer rows.Close()

	completed := map[string]string{}
	for rows.Next() {
		var fullName, outcome string
		if err := rows.Scan(&fullName, &outcome); err != nil {
			return nil, fmt.Errorf("read completed repositories: %w", err)
		}
		completed[fullName] = fmt.Sprintf("%s in run %d", outcome, r.id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read completed repositories: %w", err)
	}
	return completed, nil
}

// RecordRepository commits one repository immediately, so a process killed at
// any instant leaves behind exactly the work that finished.
func (r *Run) RecordRepository(result mirror.Result, at time.Time) error {
	_, err := r.db.Exec(
		`INSERT INTO repositories (sync_run_id, name, full_name, clone_url, default_branch, commit_sha, remote_sha, open_issues, outcome, path, synced_at, message) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.id, result.Repository.Name, result.Repository.NameWithOwner, result.Repository.CloneURL,
		result.Repository.DefaultBranch, result.LocalSHA, result.RemoteSHA, result.Repository.OpenIssues,
		string(result.Outcome), result.Path, at.UTC().Format(time.RFC3339Nano), result.Message,
	)
	if err != nil {
		return fmt.Errorf("record repository %s: %w", result.Repository.NameWithOwner, err)
	}
	return nil
}

// Finish closes the run. After this the run is no longer resumable unless the
// status says otherwise.
func (r *Run) Finish(status Status, finished time.Time, runErr error) error {
	var total int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM repositories WHERE sync_run_id = ?`, r.id).Scan(&total); err != nil {
		return fmt.Errorf("count run repositories: %w", err)
	}
	_, err := r.db.Exec(
		`UPDATE sync_runs SET status = ?, completed_at = ?, repository_count = ?, error = ? WHERE id = ?`,
		string(status), finished.UTC().Format(time.RFC3339Nano), total, errorText(runErr), r.id,
	)
	if err != nil {
		return fmt.Errorf("finish sync run: %w", err)
	}
	return nil
}

// Record writes a whole run at once. It is retained for callers that already
// have complete metadata and do not need checkpointing.
func (d *Database) Record(organization string, started, finished time.Time, dryRun bool, metadata mirror.Metadata, runErr error) error {
	run, err := d.StartRun(organization, started, dryRun)
	if err != nil {
		return err
	}
	for _, item := range metadata.Repositories {
		if err := run.RecordRepository(item, finished); err != nil {
			return err
		}
	}
	status := StatusCompleted
	if runErr != nil {
		status = StatusFailed
	}
	return run.Finish(status, finished, runErr)
}
```

Set the file's import block to exactly:

```go
import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inovacc/org-mirror/internal/mirror"
	_ "modernc.org/sqlite"
)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/history/ -v`
Expected: PASS, including the pre-existing `TestRecordStoresRunAndRepositoryHistory`.

- [ ] **Step 5: Commit**

```bash
git add internal/history/
git commit -m "feat: checkpoint each repository through a sync run lifecycle

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: The rate-limit endpoint and the organization repository count

The two API reads the `limit` verb needs.

**Files:**
- Create: `internal/githubapi/ratelimit.go`
- Create: `internal/githubapi/ratelimit_test.go`

**Interfaces:**
- Consumes: `RESTClient` and `Source` from `internal/githubapi`.
- Produces:
  - `type RateLimit struct { Limit, Used, Remaining int; Reset int64 }` with `func (l RateLimit) ResetAt() time.Time`
  - `type RateLimits struct { Resources map[string]RateLimit }` with `func (l RateLimits) Ordered() []NamedRateLimit`
  - `type NamedRateLimit struct { Name string; RateLimit }`
  - `func (s *Source) RateLimits(ctx context.Context) (RateLimits, error)`
  - `func (s *Source) RepositoryCount(ctx context.Context, organization string) (int, error)`

- [ ] **Step 1: Write the failing tests**

Create `internal/githubapi/ratelimit_test.go`:

```go
package githubapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// jsonClient answers each path with a canned JSON document.
type jsonClient struct {
	documents map[string]string
	requested []string
	err       error
}

func (c *jsonClient) Get(path string, response any) error {
	c.requested = append(c.requested, path)
	if c.err != nil {
		return c.err
	}
	document, ok := c.documents[path]
	if !ok {
		return errors.New("unexpected path: " + path)
	}
	return json.Unmarshal([]byte(document), response)
}

func TestRateLimitsParsesEveryResource(t *testing.T) {
	client := &jsonClient{documents: map[string]string{
		"rate_limit": `{"resources":{
			"core":{"limit":5000,"used":120,"remaining":4880,"reset":1700000000},
			"search":{"limit":30,"used":1,"remaining":29,"reset":1700000060}
		}}`,
	}}

	limits, err := NewSource(client).RateLimits(context.Background())
	if err != nil {
		t.Fatalf("rate limits: %v", err)
	}
	core, ok := limits.Resources["core"]
	if !ok || core.Remaining != 4880 || core.Limit != 5000 || core.Used != 120 {
		t.Fatalf("core = %#v", core)
	}
	if want := time.Unix(1700000000, 0).UTC(); !core.ResetAt().Equal(want) {
		t.Fatalf("reset = %v, want %v", core.ResetAt(), want)
	}
}

func TestRateLimitsOrdersCoreFirstThenTheRestAlphabetically(t *testing.T) {
	limits := RateLimits{Resources: map[string]RateLimit{
		"search":  {Limit: 30},
		"core":    {Limit: 5000},
		"graphql": {Limit: 5000},
	}}

	var names []string
	for _, resource := range limits.Ordered() {
		names = append(names, resource.Name)
	}
	want := []string{"core", "graphql", "search"}
	if len(names) != len(want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for index := range want {
		if names[index] != want[index] {
			t.Fatalf("names = %v, want %v", names, want)
		}
	}
}

func TestRateLimitsToleratesAPayloadWithoutOptionalResources(t *testing.T) {
	client := &jsonClient{documents: map[string]string{
		"rate_limit": `{"resources":{"core":{"limit":5000,"used":0,"remaining":5000,"reset":1700000000}}}`,
	}}

	limits, err := NewSource(client).RateLimits(context.Background())
	if err != nil {
		t.Fatalf("rate limits: %v", err)
	}
	if len(limits.Ordered()) != 1 {
		t.Fatalf("resources = %d, want 1", len(limits.Ordered()))
	}
}

func TestRateLimitsWrapsTheClientError(t *testing.T) {
	client := &jsonClient{err: errors.New("network down")}

	if _, err := NewSource(client).RateLimits(context.Background()); err == nil {
		t.Fatal("a client failure must surface")
	}
}

func TestRepositoryCountSumsPublicAndPrivateRepositories(t *testing.T) {
	client := &jsonClient{documents: map[string]string{
		"orgs/acme": `{"public_repos":12,"total_private_repos":30}`,
	}}

	count, err := NewSource(client).RepositoryCount(context.Background(), "acme")
	if err != nil {
		t.Fatalf("repository count: %v", err)
	}
	if count != 42 {
		t.Fatalf("count = %d, want 42", count)
	}
}

func TestRepositoryCountEscapesTheOrganization(t *testing.T) {
	client := &jsonClient{documents: map[string]string{
		"orgs/a%2Fb": `{"public_repos":1,"total_private_repos":0}`,
	}}

	if _, err := NewSource(client).RepositoryCount(context.Background(), "a/b"); err != nil {
		t.Fatalf("repository count: %v", err)
	}
	if client.requested[0] != "orgs/a%2Fb" {
		t.Fatalf("requested %q, want the escaped path", client.requested[0])
	}
}

func TestRateLimitsRespectsACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := NewSource(&jsonClient{}).RateLimits(ctx); err == nil {
		t.Fatal("a cancelled context must stop the call")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/githubapi/ -run 'RateLimit|RepositoryCount' -v`
Expected: FAIL, `RateLimits`, `RateLimit`, `Ordered` and `RepositoryCount` are undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/githubapi/ratelimit.go`:

```go
package githubapi

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"time"
)

// RateLimit is one resource's budget as GitHub reports it.
type RateLimit struct {
	Limit     int   `json:"limit"`
	Used      int   `json:"used"`
	Remaining int   `json:"remaining"`
	Reset     int64 `json:"reset"`
}

// ResetAt is when the budget refills.
func (l RateLimit) ResetAt() time.Time { return time.Unix(l.Reset, 0).UTC() }

// NamedRateLimit pairs a resource with its name for ordered rendering.
type NamedRateLimit struct {
	Name string
	RateLimit
}

// RateLimits is the whole rate_limit payload.
type RateLimits struct {
	Resources map[string]RateLimit `json:"resources"`
}

// Ordered lists core first, because it is the budget a sync spends, then the
// remaining resources alphabetically. Absent resources are simply not listed.
func (l RateLimits) Ordered() []NamedRateLimit {
	names := make([]string, 0, len(l.Resources))
	for name := range l.Resources {
		names = append(names, name)
	}
	sort.Slice(names, func(first, second int) bool {
		if names[first] == "core" {
			return true
		}
		if names[second] == "core" {
			return false
		}
		return names[first] < names[second]
	})
	ordered := make([]NamedRateLimit, 0, len(names))
	for _, name := range names {
		ordered = append(ordered, NamedRateLimit{Name: name, RateLimit: l.Resources[name]})
	}
	return ordered
}

// RateLimits reads the authenticated account's remaining budget. The call does
// not itself consume the core budget.
func (s *Source) RateLimits(ctx context.Context) (RateLimits, error) {
	if err := ctx.Err(); err != nil {
		return RateLimits{}, err
	}
	var limits RateLimits
	if err := s.client.Get("rate_limit", &limits); err != nil {
		return RateLimits{}, fmt.Errorf("read rate limits: %w", err)
	}
	return limits, nil
}

// RepositoryCount reports how many repositories an organization holds, which
// is what turns a remaining budget into an answer about a planned sync.
func (s *Source) RepositoryCount(ctx context.Context, organization string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var payload struct {
		PublicRepos int `json:"public_repos"`
		TotalPrivate int `json:"total_private_repos"`
	}
	path := fmt.Sprintf("orgs/%s", url.PathEscape(organization))
	if err := s.client.Get(path, &payload); err != nil {
		return 0, fmt.Errorf("read organization %s: %w", organization, err)
	}
	return payload.PublicRepos + payload.TotalPrivate, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/githubapi/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/githubapi/ratelimit.go internal/githubapi/ratelimit_test.go
git commit -m "feat: read GitHub rate limits and organization size

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: The `limit` command

The verb that answers "can I start a sync right now".

**Files:**
- Create: `cmd/org-mirror/cmd_limit.go`
- Create: `cmd/org-mirror/cmd_limit_test.go`
- Modify: `internal/githubapi/authenticated.go` (install the limiter transport and return it)

**Interfaces:**
- Consumes: `Source.RateLimits`, `Source.RepositoryCount` and `RateLimits.Ordered` from Task 6; `ratelimit.NewTransport`, `ratelimit.NewPacer` and `ratelimit.Transport` from Tasks 1 and 2.
- Produces:
  - `func newLimitCommand() *cobra.Command`
  - `func renderLimits(out io.Writer, limits githubapi.RateLimits, now time.Time) error`
  - `func renderLimitsJSON(out io.Writer, limits githubapi.RateLimits) error`
  - `func budgetVerdict(limits githubapi.RateLimits, repositories int) string`
  - `githubapi.NewAuthenticatedSource` changes signature to `func NewAuthenticatedSource(host string, options ClientOptions) (*Source, *ratelimit.Transport, string, string, error)` with `type ClientOptions struct { Delay time.Duration; MaxWait time.Duration; Notify func(ratelimit.Wait) }`.

- [ ] **Step 1: Write the failing tests**

Create `cmd/org-mirror/cmd_limit_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/inovacc/org-mirror/internal/githubapi"
)

func sampleLimits() githubapi.RateLimits {
	return githubapi.RateLimits{Resources: map[string]githubapi.RateLimit{
		"core":   {Limit: 5000, Used: 120, Remaining: 4880, Reset: 1700003600},
		"search": {Limit: 30, Used: 1, Remaining: 29, Reset: 1700000060},
	}}
}

func TestRenderLimitsListsCoreFirstWithACountdown(t *testing.T) {
	var out bytes.Buffer
	if err := renderLimits(&out, sampleLimits(), time.Unix(1700000000, 0)); err != nil {
		t.Fatalf("render: %v", err)
	}
	text := out.String()

	if !strings.Contains(text, "core") || !strings.Contains(text, "4880") {
		t.Fatalf("core row missing from:\n%s", text)
	}
	if !strings.Contains(text, "1h0m0s") {
		t.Fatalf("core countdown missing from:\n%s", text)
	}
	coreAt := strings.Index(text, "core")
	searchAt := strings.Index(text, "search")
	if coreAt < 0 || searchAt < 0 || coreAt > searchAt {
		t.Fatalf("core must be listed before search:\n%s", text)
	}
}

func TestRenderLimitsShowsAPastResetAsAvailableNow(t *testing.T) {
	var out bytes.Buffer
	if err := renderLimits(&out, sampleLimits(), time.Unix(1700009999, 0)); err != nil {
		t.Fatalf("render: %v", err)
	}

	// The timestamp column contains hyphens, so assert on the countdown column
	// itself: every row whose reset is in the past must read "now".
	rows := strings.Split(strings.TrimSpace(out.String()), "
")[1:]
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2:
%s", len(rows), out.String())
	}
	for _, row := range rows {
		if !strings.HasSuffix(strings.TrimSpace(row), "now") {
			t.Fatalf("row %q must show a past reset as now", row)
		}
	}
}

func TestRenderLimitsJSONRoundTrips(t *testing.T) {
	var out bytes.Buffer
	if err := renderLimitsJSON(&out, sampleLimits()); err != nil {
		t.Fatalf("render json: %v", err)
	}
	var decoded githubapi.RateLimits
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v\n%s", err, out.String())
	}
	if decoded.Resources["core"].Remaining != 4880 {
		t.Fatalf("decoded = %#v", decoded.Resources["core"])
	}
}

func TestBudgetVerdict(t *testing.T) {
	cases := []struct {
		name         string
		remaining    int
		repositories int
		wantSuffices bool
	}{
		{name: "plenty", remaining: 4880, repositories: 300, wantSuffices: true},
		{name: "exactly enough", remaining: 3, repositories: 300, wantSuffices: true},
		{name: "short", remaining: 1, repositories: 300, wantSuffices: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			limits := githubapi.RateLimits{Resources: map[string]githubapi.RateLimit{
				"core": {Limit: 5000, Remaining: testCase.remaining, Reset: 1700003600},
			}}
			verdict := budgetVerdict(limits, testCase.repositories)
			suffices := strings.Contains(verdict, "enough")
			if suffices != testCase.wantSuffices {
				t.Fatalf("verdict %q, want suffices=%v", verdict, testCase.wantSuffices)
			}
		})
	}
}

func TestBudgetVerdictIsEmptyWithoutACoreResource(t *testing.T) {
	if verdict := budgetVerdict(githubapi.RateLimits{}, 10); verdict != "" {
		t.Fatalf("verdict = %q, want empty", verdict)
	}
}

func TestNewLimitCommandAcceptsAnOptionalOrganization(t *testing.T) {
	command := newLimitCommand()
	if command.Use != "limit [organization]" {
		t.Fatalf("use = %q", command.Use)
	}
	if command.Flags().Lookup("json") == nil {
		t.Fatal("limit must expose a json flag")
	}
	if err := command.Args(command, []string{}); err != nil {
		t.Fatalf("no argument must be valid: %v", err)
	}
	if err := command.Args(command, []string{"acme"}); err != nil {
		t.Fatalf("one argument must be valid: %v", err)
	}
	if err := command.Args(command, []string{"acme", "extra"}); err == nil {
		t.Fatal("two arguments must be rejected")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/org-mirror/ -run 'Limit|Budget' -v`
Expected: FAIL, `renderLimits`, `renderLimitsJSON`, `budgetVerdict` and `newLimitCommand` are undefined.

- [ ] **Step 3: Write the implementation**

First change `internal/githubapi/authenticated.go` so every client is rate limited. Replace the whole file with:

```go
package githubapi

import (
	"fmt"
	"os"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/config"
	"github.com/inovacc/org-mirror/internal/githubauth"
	"github.com/inovacc/org-mirror/internal/ratelimit"
	"github.com/zalando/go-keyring"
)

// ClientOptions tunes the rate limiting applied to every API request.
type ClientOptions struct {
	// Delay is the minimum interval between requests. Zero disables pacing.
	Delay time.Duration
	// MaxWait caps a single rate-limit wait. Zero uses the package default.
	MaxWait time.Duration
	// Notify, when set, is called before each wait so a caller can show it.
	Notify func(ratelimit.Wait)
}

// NewAuthenticatedSource builds a source whose transport honours GitHub's rate
// limits. The transport is returned so a caller can read the current budget.
func NewAuthenticatedSource(host string, options ClientOptions) (*Source, *ratelimit.Transport, string, string, error) {
	configuration, err := config.Read(nil)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("read GitHub CLI configuration: %w", err)
	}
	resolver := githubauth.Resolver{
		Config:     configuration,
		LookupEnv:  os.LookupEnv,
		KeyringGet: keyring.Get,
	}
	token, user, err := resolver.Token(host)
	if err != nil {
		return nil, nil, "", user, err
	}

	transport := ratelimit.NewTransport(ratelimit.TransportOptions{
		Pacer:   ratelimit.NewPacer(ratelimit.PacerOptions{Interval: options.Delay, Jitter: 0.3}),
		MaxWait: options.MaxWait,
		Notify:  options.Notify,
	})
	client, err := api.NewRESTClient(api.ClientOptions{
		Host:         host,
		AuthToken:    token,
		Timeout:      30 * time.Second,
		LogIgnoreEnv: true,
		Transport:    transport,
	})
	if err != nil {
		return nil, nil, "", user, fmt.Errorf("create GitHub API client: %w", err)
	}
	return NewSource(client), transport, token, user, nil
}
```

Create `cmd/org-mirror/cmd_limit.go`:

```go
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/inovacc/org-mirror/internal/githubapi"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(newLimitCommand())
}

func newLimitCommand() *cobra.Command {
	var asJSON bool

	command := &cobra.Command{
		Use:   "limit [organization]",
		Short: "Show the authenticated account's GitHub rate-limit budget",
		Long: `Show how much GitHub API budget the authenticated account has left.

Naming an organization adds a line saying whether the remaining core budget
covers discovering that organization's repositories.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			source, _, _, _, err := githubapi.NewAuthenticatedSource("github.com", githubapi.ClientOptions{})
			if err != nil {
				return err
			}
			limits, err := source.RateLimits(command.Context())
			if err != nil {
				return err
			}
			if asJSON {
				return renderLimitsJSON(command.OutOrStdout(), limits)
			}
			if err := renderLimits(command.OutOrStdout(), limits, time.Now()); err != nil {
				return err
			}
			if len(args) == 0 {
				return nil
			}
			count, err := source.RepositoryCount(command.Context(), args[0])
			if err != nil {
				return err
			}
			if verdict := budgetVerdict(limits, count); verdict != "" {
				fmt.Fprintf(command.OutOrStdout(), "\n%s has %d repositories. %s\n", args[0], count, verdict)
			}
			return nil
		},
	}
	command.Flags().BoolVar(&asJSON, "json", false, "print the rate-limit payload as JSON")
	return command
}

func renderLimits(out io.Writer, limits githubapi.RateLimits, now time.Time) error {
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "RESOURCE\tLIMIT\tUSED\tREMAINING\tRESET (UTC)\tIN")
	for _, resource := range limits.Ordered() {
		fmt.Fprintf(writer, "%s\t%d\t%d\t%d\t%s\t%s\n",
			resource.Name, resource.Limit, resource.Used, resource.Remaining,
			resource.ResetAt().Format(time.RFC3339), countdown(resource.ResetAt(), now))
	}
	return writer.Flush()
}

func renderLimitsJSON(out io.Writer, limits githubapi.RateLimits) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(limits)
}

// countdown never renders a negative duration, because a reset in the past
// simply means the budget is already available.
func countdown(reset, now time.Time) string {
	remaining := reset.Sub(now)
	if remaining <= 0 {
		return "now"
	}
	return remaining.Round(time.Second).String()
}

// requestsPerPage matches the page size the repository listing uses.
const requestsPerPage = 100

// budgetVerdict says whether the core budget covers discovering an
// organization of the given size. It is empty when core is not reported.
func budgetVerdict(limits githubapi.RateLimits, repositories int) string {
	core, ok := limits.Resources["core"]
	if !ok {
		return ""
	}
	needed := repositories / requestsPerPage
	if repositories%requestsPerPage != 0 || needed == 0 {
		needed++
	}
	if core.Remaining >= needed {
		return fmt.Sprintf("Discovery needs about %d requests and %d remain, which is enough.", needed, core.Remaining)
	}
	return fmt.Sprintf("Discovery needs about %d requests but only %d remain. The budget resets at %s.",
		needed, core.Remaining, core.ResetAt().Format(time.RFC3339))
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go build ./... && go test ./cmd/org-mirror/ -v`
Expected: the build FAILS first, because `cmd_sync.go` still calls `NewAuthenticatedSource` with the old signature. Fix that single call site now by replacing

```go
			source, token, _, err := githubapi.NewAuthenticatedSource("github.com")
```

with

```go
			source, _, token, _, err := githubapi.NewAuthenticatedSource("github.com", githubapi.ClientOptions{})
```

Task 8 replaces this line properly. Then re-run the command above.
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/org-mirror/cmd_limit.go cmd/org-mirror/cmd_limit_test.go internal/githubapi/authenticated.go cmd/org-mirror/cmd_sync.go
git commit -m "feat: add the limit command and rate-limit the API client

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Wire sync to resume, pace and report waiting

The last task joins everything: new flags, the resume decision, per-repository checkpointing, an honest final status, and a TUI that shows a wait instead of looking hung.

**Files:**
- Modify: `cmd/org-mirror/cmd_sync.go`
- Modify: `cmd/org-mirror/cmd_sync_test.go`
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/model_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: everything produced by Tasks 1 through 7.
- Produces:
  - `func finalStatus(runErr error, truncated bool) history.Status`
  - `func resumeSkips(database *history.Database, organization string, disabled, dryRun bool) (*history.Run, map[string]string, error)`
  - `type waitReporter struct { mu sync.Mutex; report mirror.ProgressFunc; organization string; fallback io.Writer }` with `attach(mirror.ProgressFunc)` and `notify(ratelimit.Wait)`
  - `Model` gains `waiting string` and `waitingUntil time.Time`, cleared by the next repository event.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/org-mirror/cmd_sync_test.go`:

```go
func TestFinalStatus(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		truncated bool
		want      history.Status
	}{
		{name: "clean run", want: history.StatusCompleted},
		{name: "capped run", truncated: true, want: history.StatusInterrupted},
		{name: "cancelled run", err: context.Canceled, want: history.StatusInterrupted},
		{name: "deadline", err: context.DeadlineExceeded, want: history.StatusInterrupted},
		{name: "wrapped cancellation", err: fmt.Errorf("mirror: %w", context.Canceled), want: history.StatusInterrupted},
		{name: "real failure", err: errors.New("discovery failed"), want: history.StatusFailed},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := finalStatus(testCase.err, testCase.truncated); got != testCase.want {
				t.Fatalf("finalStatus = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestResumeSkipsReturnsTheInterruptedRunsCompletedRepositories(t *testing.T) {
	root, databasePath := defaultSyncPaths(t.TempDir())
	database, err := prepareSyncStorage(root, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	previous, err := database.StartRun("acme", time.Unix(1, 0), false)
	if err != nil {
		t.Fatal(err)
	}
	done := mirror.Result{
		Repository: mirror.Repository{Name: "one", NameWithOwner: "acme/one"},
		Outcome:    mirror.OutcomeCloned,
	}
	if err := previous.RecordRepository(done, time.Unix(2, 0)); err != nil {
		t.Fatal(err)
	}

	run, skips, err := resumeSkips(database, "acme", false, false)
	if err != nil {
		t.Fatalf("resume skips: %v", err)
	}
	if run.ID() != previous.ID() {
		t.Fatalf("run %d, want the interrupted run %d", run.ID(), previous.ID())
	}
	if _, ok := skips["acme/one"]; !ok {
		t.Fatalf("skips = %v, want acme/one", skips)
	}
}

func TestResumeSkipsStartsFreshWhenResumeIsDisabled(t *testing.T) {
	root, databasePath := defaultSyncPaths(t.TempDir())
	database, err := prepareSyncStorage(root, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	previous, err := database.StartRun("acme", time.Unix(1, 0), false)
	if err != nil {
		t.Fatal(err)
	}
	done := mirror.Result{
		Repository: mirror.Repository{Name: "one", NameWithOwner: "acme/one"},
		Outcome:    mirror.OutcomeCloned,
	}
	if err := previous.RecordRepository(done, time.Unix(2, 0)); err != nil {
		t.Fatal(err)
	}

	run, skips, err := resumeSkips(database, "acme", true, false)
	if err != nil {
		t.Fatalf("resume skips: %v", err)
	}
	if run.ID() == previous.ID() {
		t.Fatal("no-resume must open a new run")
	}
	if len(skips) != 0 {
		t.Fatalf("skips = %v, want none", skips)
	}
}

func TestResumeSkipsStartsAFreshRunWhenNothingIsResumable(t *testing.T) {
	root, databasePath := defaultSyncPaths(t.TempDir())
	database, err := prepareSyncStorage(root, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	run, skips, err := resumeSkips(database, "acme", false, false)
	if err != nil {
		t.Fatalf("resume skips: %v", err)
	}
	if run == nil || run.ID() == 0 {
		t.Fatal("a fresh run must be opened")
	}
	if len(skips) != 0 {
		t.Fatalf("skips = %v, want none", skips)
	}
}

func TestNewSyncCommandExposesTheResilienceFlags(t *testing.T) {
	command := newSyncCommand()
	for _, name := range []string{"delay", "max-wait", "retries", "no-resume", "limit"} {
		if command.Flags().Lookup(name) == nil {
			t.Fatalf("sync must expose the %s flag", name)
		}
	}

	delay, err := command.Flags().GetDuration("delay")
	if err != nil {
		t.Fatalf("read delay: %v", err)
	}
	if delay != 750*time.Millisecond {
		t.Fatalf("default delay = %v, want 750ms", delay)
	}
	retries, err := command.Flags().GetInt("retries")
	if err != nil {
		t.Fatalf("read retries: %v", err)
	}
	if retries != 3 {
		t.Fatalf("default retries = %d, want 3", retries)
	}
	limit, err := command.Flags().GetInt("limit")
	if err != nil {
		t.Fatalf("read limit: %v", err)
	}
	if limit != 0 {
		t.Fatalf("default limit = %d, want 0", limit)
	}
}

func TestWaitReporterWritesToTheFallbackUntilAFrontEndAttaches(t *testing.T) {
	var out bytes.Buffer
	reporter := &waitReporter{organization: "acme", fallback: &out}

	reporter.notify(ratelimit.Wait{Reason: "GitHub rate limit reached", Duration: 30 * time.Second})
	if !strings.Contains(out.String(), "GitHub rate limit reached") {
		t.Fatalf("fallback output = %q", out.String())
	}

	var events []mirror.ProgressEvent
	reporter.attach(func(event mirror.ProgressEvent) { events = append(events, event) })
	reporter.notify(ratelimit.Wait{Reason: "waiting for the reset", Duration: time.Minute})

	if len(events) != 1 || events[0].Kind != mirror.ProgressWaiting {
		t.Fatalf("events = %#v, want one waiting event", events)
	}
	if events[0].Message != "waiting for the reset" || events[0].Organization != "acme" {
		t.Fatalf("event = %#v", events[0])
	}
	if strings.Count(out.String(), "waiting") > 1 {
		t.Fatal("once a front end is attached the fallback must stay quiet")
	}
}
```

Set that file's import block to exactly:

```go
import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inovacc/org-mirror/internal/history"
	"github.com/inovacc/org-mirror/internal/mirror"
	"github.com/inovacc/org-mirror/internal/ratelimit"
)
```

Append to `internal/tui/model_test.go`:

```go
func TestModelRendersAWaitingLine(t *testing.T) {
	model := NewModel("acme", false)
	model.total = 10
	model.completed = 3

	updated, _ := model.Update(mirror.ProgressEvent{
		Kind:    mirror.ProgressWaiting,
		Message: "GitHub rate limit reached, waiting for the reset",
		Until:   time.Unix(1700003600, 0).UTC(),
	})
	view := updated.(Model).View().Content

	if !strings.Contains(view, "Waiting:") {
		t.Fatalf("waiting line missing from view:\n%s", view)
	}
	if !strings.Contains(view, "rate limit") {
		t.Fatalf("waiting reason missing from view:\n%s", view)
	}
}

func TestModelClearsTheWaitingLineWhenWorkResumes(t *testing.T) {
	model := NewModel("acme", false)
	model.total = 10

	waiting, _ := model.Update(mirror.ProgressEvent{Kind: mirror.ProgressWaiting, Message: "waiting"})
	resumed, _ := waiting.(Model).Update(mirror.ProgressEvent{
		Kind:       mirror.ProgressRepositoryStarted,
		Repository: mirror.Repository{NameWithOwner: "acme/one"},
		Completed:  1,
		Total:      10,
	})

	if strings.Contains(resumed.(Model).View().Content, "Waiting:") {
		t.Fatal("the waiting line must clear once work resumes")
	}
}
```

Make sure `internal/tui/model_test.go` imports `strings`, `testing`, `time` and `github.com/inovacc/org-mirror/internal/mirror`. The `waitForProgress` command reads `m.context`, but `Update` does not, so these tests need no context.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/org-mirror/ ./internal/tui/ -v`
Expected: FAIL, `finalStatus`, `resumeSkips` and the waiting flags are undefined.

- [ ] **Step 3: Write the implementation**

Replace `internal/tui/model.go`'s `Model` struct, its `ProgressEvent` case, and its `View` waiting section. Add two fields to the struct, after `elapsed`:

```go
	waiting      string
	waitingUntil time.Time
```

In `Update`, inside `case mirror.ProgressEvent:`, change the switch to clear the waiting line on any non-waiting event and set it on a waiting one:

```go
		switch message.Kind {
		case mirror.ProgressDiscoveryStarted:
			m.waiting = ""
			m.discovering = true
		case mirror.ProgressDiscoveryCompleted:
			m.waiting = ""
			m.discovering = false
			m.total = message.Total
		case mirror.ProgressRepositoryStarted:
			m.waiting = ""
			m.current = message.Repository.NameWithOwner
			m.completed = message.Completed
			m.total = message.Total
		case mirror.ProgressRepositoryCompleted:
			m.waiting = ""
			m.current = message.Repository.NameWithOwner
			m.completed = message.Completed
			m.total = message.Total
			m.recent = append(m.recent, message.Result)
		case mirror.ProgressWaiting:
			m.waiting = message.Message
			m.waitingUntil = message.Until
		}
```

In `View`, immediately after the `if m.discovering || m.total == 0 { ... } else { ... }` block and before the `if len(m.recent) > 0` block, add:

```go
	if m.waiting != "" {
		if m.waitingUntil.IsZero() {
			fmt.Fprintf(&content, "Waiting:   %s\n", m.waiting)
		} else {
			fmt.Fprintf(&content, "Waiting:   %s (until %s)\n", m.waiting, m.waitingUntil.UTC().Format("15:04:05Z"))
		}
	}
```

Now replace `cmd/org-mirror/cmd_sync.go` entirely:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/inovacc/org-mirror/internal/githubapi"
	"github.com/inovacc/org-mirror/internal/history"
	"github.com/inovacc/org-mirror/internal/mirror"
	"github.com/inovacc/org-mirror/internal/ratelimit"
	"github.com/inovacc/org-mirror/internal/tui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func init() {
	rootCmd.AddCommand(newSyncCommand())
}

func newSyncCommand() *cobra.Command {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	root, databasePath := defaultSyncPaths(home)
	var dryRun bool
	var noTUI bool
	var noResume bool
	var delay time.Duration
	var maxWait time.Duration
	var retries int
	var limit int

	command := &cobra.Command{
		Use:   "sync <organization>",
		Short: "Mirror an organization as local working copies",
		Long: `Mirror an organization as local working copies.

The run paces itself between repositories so it trips neither GitHub's rate
limits nor local endpoint-protection heuristics, and it checkpoints every
repository as it finishes. An interrupted run is continued automatically by the
next sync of the same organization.`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			organization := args[0]
			database, err := prepareSyncStorage(root, databasePath)
			if err != nil {
				return err
			}
			defer database.Close()

			run, skips, err := resumeSkips(database, organization, noResume || dryRun, dryRun)
			if err != nil {
				return err
			}

			// waits carries rate-limit notices from the transport to whichever front
			// end is running, so a long wait is visible rather than looking like a
			// hang. The transport calls it from the mirroring goroutine while the
			// front end attaches from another, hence the lock inside waitReporter.
			waits := &waitReporter{organization: organization, fallback: command.ErrOrStderr()}

			source, _, token, _, err := githubapi.NewAuthenticatedSource("github.com", githubapi.ClientOptions{
				Delay:   delay,
				MaxWait: maxWait,
				Notify:  waits.notify,
			})
			if err != nil {
				_ = run.Finish(history.StatusFailed, time.Now(), err)
				return err
			}

			service := mirror.NewServiceWithOptions(
				mirror.OSRunner{GitHubToken: token},
				source,
				mirror.RetryPolicy{
					Attempts: retries,
					Backoff:  gitBackoff,
					Sleep:    ratelimit.SystemClock{}.Sleep,
				},
			)
			pacer := ratelimit.NewPacer(ratelimit.PacerOptions{Interval: delay, Jitter: 0.3})

			options := mirror.MirrorOptions{
				Pacer: pacer,
				Skip:  skips,
				Limit: limit,
				OnResult: func(result mirror.Result) error {
					if dryRun {
						return nil
					}
					return run.RecordRepository(result, time.Now())
				},
			}

			var metadata mirror.Metadata
			if shouldUseTUI(noTUI, command.OutOrStdout(), term.IsTerminal) {
				metadata, err = tui.Run(command.Context(), organization, dryRun, func(ctx context.Context, progress mirror.ProgressFunc) (mirror.Metadata, error) {
					waits.attach(progress)
					options.Report = progress
					return service.MirrorWithOptions(ctx, organization, root, dryRun, options)
				})
			} else {
				metadata, err = service.MirrorWithOptions(command.Context(), organization, root, dryRun, options)
			}

			if finishErr := run.Finish(finalStatus(err, metadata.Truncated), time.Now(), err); finishErr != nil {
				return finishErr
			}
			if err != nil {
				return err
			}

			for _, result := range metadata.Repositories {
				if result.Message == "" {
					fmt.Fprintf(command.OutOrStdout(), "%s: %s\n", result.Repository.NameWithOwner, result.Outcome)
					continue
				}
				fmt.Fprintf(command.OutOrStdout(), "%s: %s (%s)\n", result.Repository.NameWithOwner, result.Outcome, result.Message)
			}
			if metadata.Truncated {
				fmt.Fprintf(command.OutOrStdout(), "stopped at the --limit of %d; run sync again to continue\n", limit)
			}
			if dryRun {
				fmt.Fprintln(command.OutOrStdout(), "dry-run: metadata was not written")
				return nil
			}
			fmt.Fprintf(command.OutOrStdout(), "metadata: %s\n", filepath.Join(root, organization, "metadata.json"))
			return nil
		},
	}

	command.Flags().StringVar(&root, "root", root, "directory that contains organization mirrors")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "report actions without changing repositories or metadata")
	command.Flags().BoolVar(&noTUI, "no-tui", false, "disable the interactive progress interface")
	command.Flags().StringVar(&databasePath, "database", databasePath, "SQLite database for sync history")
	command.Flags().DurationVar(&delay, "delay", 750*time.Millisecond, "minimum interval between repositories; 0 disables pacing")
	command.Flags().DurationVar(&maxWait, "max-wait", 15*time.Minute, "longest a rate-limit wait may block before failing")
	command.Flags().IntVar(&retries, "retries", 3, "attempts for a transient git failure, counting the first")
	command.Flags().BoolVar(&noResume, "no-resume", false, "start fresh instead of continuing an interrupted run")
	command.Flags().IntVar(&limit, "limit", 0, "process at most this many repositories; 0 means no cap")
	return command
}

// waitReporter turns a transport wait into something the operator can see. It
// prints to a writer until a progress front end attaches, then reports events.
type waitReporter struct {
	mu           sync.Mutex
	report       mirror.ProgressFunc
	organization string
	fallback     io.Writer
}

func (w *waitReporter) attach(report mirror.ProgressFunc) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.report = report
}

func (w *waitReporter) notify(wait ratelimit.Wait) {
	w.mu.Lock()
	report := w.report
	w.mu.Unlock()

	if report == nil {
		fmt.Fprintf(w.fallback, "waiting %s: %s
", wait.Duration.Round(time.Second), wait.Reason)
		return
	}
	report(mirror.ProgressEvent{
		Kind:         mirror.ProgressWaiting,
		Organization: w.organization,
		Message:      wait.Reason,
		Until:        wait.Until,
	})
}

// gitBackoff doubles from two seconds, which is long enough that a retry is not
// itself a burst.
func gitBackoff(attempt int) time.Duration {
	delay := 2 * time.Second << (attempt - 1)
	if delay > time.Minute || delay <= 0 {
		return time.Minute
	}
	return delay
}

// resumeSkips continues an interrupted run when there is one, and reports which
// repositories that run already finished.
func resumeSkips(database *history.Database, organization string, disabled, dryRun bool) (*history.Run, map[string]string, error) {
	if !disabled {
		run, found, err := database.ResumableRun(organization)
		if err != nil {
			return nil, nil, err
		}
		if found {
			skips, err := run.CompletedRepositories()
			if err != nil {
				return nil, nil, err
			}
			return run, skips, nil
		}
	}
	run, err := database.StartRun(organization, time.Now(), dryRun)
	if err != nil {
		return nil, nil, err
	}
	return run, map[string]string{}, nil
}

// finalStatus keeps a cancelled or capped run distinct from a failed one, because
// only the operator's own stop should read as deliberate.
func finalStatus(runErr error, truncated bool) history.Status {
	switch {
	case errors.Is(runErr, context.Canceled), errors.Is(runErr, context.DeadlineExceeded):
		return history.StatusInterrupted
	case runErr != nil:
		return history.StatusFailed
	case truncated:
		return history.StatusInterrupted
	default:
		return history.StatusCompleted
	}
}

func defaultSyncPaths(home string) (string, string) {
	base := filepath.Join(home, "Downloads", "mirror")
	return filepath.Join(base, "orgs"), filepath.Join(base, "database.db")
}

func prepareSyncStorage(root, databasePath string) (*history.Database, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create organizations directory: %w", err)
	}
	return history.Open(databasePath)
}

func shouldUseTUI(disabled bool, output io.Writer, isTerminal func(int) bool) bool {
	if disabled {
		return false
	}
	file, ok := output.(interface{ Fd() uintptr })
	return ok && isTerminal(int(file.Fd()))
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go build ./... && go vet ./... && go test ./... -race`
Expected: PASS across every package.

- [ ] **Step 5: Update the README**

In `README.md`, add these rows to the command table:

```markdown
| `limit [organization]` | Show the account's GitHub rate-limit budget |
```

And add this section directly after the `## Usage` code block:

```markdown
### Pacing, resume and limits

The sync paces itself between repositories. The delay protects two different
things: GitHub's secondary rate limits react to bursts of requests, and local
endpoint-protection software reacts to bursts of process creation, which is what
a fast mirror run of several hundred repositories looks like.

```bash
# Slow down to one repository every two seconds.
org-mirror sync floci-io --delay 2s

# Turn pacing off entirely, accepting both risks.
org-mirror sync floci-io --delay 0

# Process 50 repositories, then stop. Run it again to continue.
org-mirror sync floci-io --limit 50

# Ignore an interrupted run and start over.
org-mirror sync floci-io --no-resume

# Check the remaining API budget before starting.
org-mirror limit floci-io
```

Every repository is written to the history database the moment it finishes, so a
crash, a dropped connection or `Ctrl+C` loses nothing. The next sync of the same
organization continues the interrupted run and skips what was already done.
Repositories that failed are retried rather than skipped.
```

- [ ] **Step 6: Commit**

```bash
git add cmd/org-mirror/ internal/tui/ README.md
git commit -m "feat: resume interrupted syncs and pace against rate limits

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Verification

After Task 8, confirm the whole thing builds and behaves:

- [ ] `go build ./... && go vet ./... && go test ./... -race` passes.
- [ ] `go run ./cmd/org-mirror limit` prints the budget table.
- [ ] `go run ./cmd/org-mirror limit --json` prints valid JSON.
- [ ] `go run ./cmd/org-mirror sync <org> --dry-run --no-tui --limit 3` processes three repositories and says work remains.
- [ ] `go run ./cmd/org-mirror sync <org> --no-tui --limit 3`, then the same command again, shows the first three as `skipped` and processes three more.
- [ ] `go run ./cmd/org-mirror cmdtree` lists `limit` and the new sync flags.

Report each result as what the command printed. A passing test is a fact about a machine check, not a declaration that the feature works. Only the operator declares that.
