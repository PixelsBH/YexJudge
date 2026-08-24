package judge

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"yexjudge/internal/judge/languages"
	"yexjudge/internal/runner"
)

func TestCompileWorkerPoolBoundsConcurrentCompiles(t *testing.T) {
	poolStarted := make(chan struct{}, 2)
	release := make(chan struct{})
	var active atomic.Int32
	var maximum atomic.Int32

	pool := NewCompileWorkerPool(2, func(ctx context.Context, _ string, _ languages.Spec, _ Limits) (*runner.RunResult, error) {
		current := active.Add(1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		poolStarted <- struct{}{}
		select {
		case <-release:
			active.Add(-1)
			return &runner.RunResult{ExitCode: 0}, nil
		case <-ctx.Done():
			active.Add(-1)
			return nil, ctx.Err()
		}
	})
	defer pool.Close()

	results := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() {
			_, err := pool.Compile(context.Background(), "/workspace", languages.Cpp{}, Limits{})
			results <- err
		}()
	}

	for i := 0; i < 2; i++ {
		select {
		case <-poolStarted:
		case <-time.After(time.Second):
			t.Fatal("compile worker did not start")
		}
	}
	select {
	case <-poolStarted:
		t.Fatal("third compile started before a worker was released")
	case <-time.After(20 * time.Millisecond):
	}
	if got := maximum.Load(); got != 2 {
		t.Fatalf("maximum concurrent compiles = %d, want 2", got)
	}

	close(release)
	for i := 0; i < 3; i++ {
		if err := <-results; err != nil {
			t.Fatalf("Compile() error = %v", err)
		}
	}
}

func TestCompileWorkerPoolQueuedCompileHonorsCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	pool := NewCompileWorkerPool(1, func(ctx context.Context, _ string, _ languages.Spec, _ Limits) (*runner.RunResult, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return &runner.RunResult{ExitCode: 0}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	defer pool.Close()

	firstDone := make(chan error, 1)
	go func() {
		_, err := pool.Compile(context.Background(), "/workspace", languages.Cpp{}, Limits{})
		firstDone <- err
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		_, err := pool.Compile(ctx, "/workspace", languages.Cpp{}, Limits{})
		secondDone <- err
	}()
	cancel()

	if err := <-secondDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued Compile() error = %v, want context.Canceled", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("compiler calls = %d, want queued canceled request to be skipped", got)
	}

	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Compile() error = %v", err)
	}
}

func TestCompileWorkerPoolCloseCancelsActiveCompile(t *testing.T) {
	started := make(chan struct{})
	pool := NewCompileWorkerPool(1, func(ctx context.Context, _ string, _ languages.Spec, _ Limits) (*runner.RunResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})

	result := make(chan error, 1)
	go func() {
		_, err := pool.Compile(context.Background(), "/workspace", languages.Cpp{}, Limits{})
		result <- err
	}()
	<-started

	pool.Close()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Compile() error after Close() = %v, want context.Canceled", err)
	}
}
