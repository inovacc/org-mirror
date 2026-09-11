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
