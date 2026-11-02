package storage

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// Circuit breaker for the S3/R2 backend. After too many consecutive failures the
// circuit opens and all ops fail fast for a cooldown, instead of hammering a dead
// endpoint and stacking up slow timeouts. A 404 (ErrNotFound) is a healthy
// response, not a failure. Ported from the legacy Koa server (same defaults).

// ErrCircuitOpen is returned while the breaker is open (backend presumed down).
var ErrCircuitOpen = errors.New("storage: circuit open (backend unavailable)")

const (
	breakerThreshold    = 5                // consecutive failures before opening
	breakerBackoff      = 60 * time.Second // cooldown while open
	breakerOpTimeout    = 5 * time.Second  // metadata ops (stat/has/delete/list)
	breakerWriteTimeout = 5 * time.Minute  // blob writes (covers large uploads)
)

type breaker struct {
	mu        sync.Mutex
	failures  int
	openUntil time.Time
	threshold int
	backoff   time.Duration
	now       func() time.Time // injectable for tests
}

func newBreaker() *breaker {
	return &breaker{threshold: breakerThreshold, backoff: breakerBackoff, now: time.Now}
}

// guard runs fn under a per-op timeout, fast-failing if the circuit is open and
// tracking consecutive failures. A nil error or ErrNotFound counts as healthy.
func (b *breaker) guard(ctx context.Context, timeout time.Duration, fn func(context.Context) error) error {
	b.mu.Lock()
	if b.now().Before(b.openUntil) {
		b.mu.Unlock()
		return ErrCircuitOpen
	}
	b.mu.Unlock()

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := fn(cctx)
	b.record(err)
	return err
}

func (b *breaker) record(err error) {
	healthy := err == nil || errors.Is(err, ErrNotFound)
	b.mu.Lock()
	defer b.mu.Unlock()
	if healthy {
		b.failures = 0
		return
	}
	b.failures++
	if b.failures >= b.threshold {
		b.openUntil = b.now().Add(b.backoff)
		b.failures = 0 // reset; the open window is the penalty
		slog.Warn("storage circuit opened",
			"threshold", b.threshold, "backoff", b.backoff.String(), "last_err", err)
	}
}
