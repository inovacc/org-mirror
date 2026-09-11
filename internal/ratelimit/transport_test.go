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

	if _, err := transport.RoundTrip(request); err == nil {
		t.Fatal("a cancelled context must abort the round trip")
	}
}
