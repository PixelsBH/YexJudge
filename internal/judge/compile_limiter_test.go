package judge

import (
	"context"
	"testing"
)

func TestCompileLimiterBoundsConcurrentAcquisitions(t *testing.T) {
	limiter := NewCompileLimiter(1)
	if got := limiter.Capacity(); got != 1 {
		t.Fatalf("Capacity() = %d, want 1", got)
	}

	if err := limiter.Acquire(context.Background()); err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	blocked := make(chan error, 1)
	go func() { blocked <- limiter.Acquire(context.Background()) }()
	select {
	case err := <-blocked:
		t.Fatalf("second Acquire() completed early with %v", err)
	default:
	}

	limiter.Release()
	if err := <-blocked; err != nil {
		t.Fatalf("second Acquire() error = %v", err)
	}
	limiter.Release()
}

func TestCompileLimiterAcquireHonorsCancellation(t *testing.T) {
	limiter := NewCompileLimiter(1)
	if err := limiter.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	defer limiter.Release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := limiter.Acquire(ctx); err != context.Canceled {
		t.Fatalf("Acquire() error = %v, want context.Canceled", err)
	}
}
