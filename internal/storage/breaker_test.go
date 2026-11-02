package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBreaker_OpensAfterThresholdAndCoolsDown(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	b := newBreaker()
	b.now = func() time.Time { return now }

	fail := func(context.Context) error { return errors.New("r2 down") }

	// threshold-1 failures: still closed (real errors propagate).
	for i := 0; i < breakerThreshold-1; i++ {
		if err := b.guard(context.Background(), time.Second, fail); err == nil {
			t.Fatalf("call %d: want error, got nil", i)
		}
	}
	// The threshold-th failure opens the circuit.
	if err := b.guard(context.Background(), time.Second, fail); err == nil {
		t.Fatal("threshold failure: want error")
	}

	// Now open: calls fail fast with ErrCircuitOpen without invoking fn.
	called := false
	err := b.guard(context.Background(), time.Second, func(context.Context) error { called = true; return nil })
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("want ErrCircuitOpen, got %v", err)
	}
	if called {
		t.Fatal("fn ran while circuit was open")
	}

	// After the cooldown, the circuit half-opens and a success closes it.
	now = now.Add(breakerBackoff + time.Second)
	if err := b.guard(context.Background(), time.Second, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("after cooldown: want success, got %v", err)
	}
}

func TestBreaker_NotFoundIsHealthy(t *testing.T) {
	b := newBreaker()
	// Many ErrNotFound results must never open the circuit (404 = backend is fine).
	for i := 0; i < breakerThreshold*3; i++ {
		if err := b.guard(context.Background(), time.Second, func(context.Context) error { return ErrNotFound }); !errors.Is(err, ErrNotFound) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
	}
	if b.failures != 0 {
		t.Fatalf("ErrNotFound counted as failure: failures=%d", b.failures)
	}
}

func TestBreaker_SuccessResetsFailures(t *testing.T) {
	b := newBreaker()
	fail := func(context.Context) error { return errors.New("x") }
	_ = b.guard(context.Background(), time.Second, fail)
	_ = b.guard(context.Background(), time.Second, fail)
	if b.failures != 2 {
		t.Fatalf("failures=%d, want 2", b.failures)
	}
	_ = b.guard(context.Background(), time.Second, func(context.Context) error { return nil })
	if b.failures != 0 {
		t.Fatalf("success did not reset: failures=%d", b.failures)
	}
}
