package judge

import (
	"context"
	"errors"
	"testing"
	"time"

	"yexjudge/internal/judge/languages"
	"yexjudge/internal/runner"
)

type poolExecutor struct {
	resetErr  error
	resets    int
	removed   int
	started   int
	configure int
	startErrs int
}

func (e *poolExecutor) Compile(context.Context, string, languages.Spec, Limits) (*runner.RunResult, error) {
	return &runner.RunResult{}, nil
}
func (e *poolExecutor) StartSandbox(context.Context) (*Sandbox, error) {
	if e.startErrs > 0 {
		e.startErrs--
		return nil, errors.New("sandbox startup failed")
	}
	e.started++
	return &Sandbox{ContainerName: "replacement"}, nil
}
func (e *poolExecutor) ConfigureSandbox(context.Context, *Sandbox, Limits) error {
	e.configure++
	return nil
}
func (e *poolExecutor) PrepareSandbox(context.Context, *Sandbox, string) error { return nil }
func (e *poolExecutor) ResetSandbox(context.Context, *Sandbox) error {
	e.resets++
	return e.resetErr
}
func (e *poolExecutor) RemoveSandbox(*Sandbox) { e.removed++ }
func (e *poolExecutor) RunTestCase(context.Context, *Sandbox, string, languages.Spec) (*runner.RunResult, error) {
	return &runner.RunResult{}, nil
}

func TestSandboxPoolDoesNotDoubleResetRestartedSandbox(t *testing.T) {
	executor := &poolExecutor{}
	sandbox := &Sandbox{ContainerName: "sandbox", restarted: true}
	pool := NewExecutorSandboxPool(executor, []*Sandbox{sandbox})

	borrowed, err := pool.Acquire(context.Background(), Limits{MemoryLimitMb: 128})
	if err != nil {
		t.Fatalf("initial Acquire() error = %v", err)
	}
	if borrowed != sandbox {
		t.Fatalf("initial sandbox = %p, want %p", borrowed, sandbox)
	}
	pool.Release(sandbox)
	borrowed, err = pool.Acquire(context.Background(), Limits{MemoryLimitMb: 128})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if borrowed != sandbox {
		t.Fatalf("borrowed sandbox = %p, want restarted sandbox %p", borrowed, sandbox)
	}
	if executor.resets != 0 {
		t.Fatalf("reset calls = %d, want 0 after execution restart", executor.resets)
	}
}

func TestSandboxPoolReplacesSandboxWhenResetOrReadinessFails(t *testing.T) {
	executor := &poolExecutor{resetErr: errors.New("sandbox did not become ready")}
	sandbox := &Sandbox{ContainerName: "sandbox"}
	pool := NewExecutorSandboxPool(executor, []*Sandbox{sandbox})

	if _, err := pool.Acquire(context.Background(), Limits{MemoryLimitMb: 128}); err != nil {
		t.Fatalf("initial Acquire() error = %v", err)
	}
	pool.Release(sandbox)
	borrowed, err := pool.Acquire(context.Background(), Limits{MemoryLimitMb: 128})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if borrowed.ContainerName != "replacement" {
		t.Fatalf("borrowed sandbox = %q, want replacement", borrowed.ContainerName)
	}
	if executor.removed != 1 || executor.started != 1 || executor.resets != 1 {
		t.Fatalf("pool lifecycle = removed %d, started %d, resets %d; want 1, 1, 1", executor.removed, executor.started, executor.resets)
	}
}

func TestSandboxPoolReplacesSandboxMarkedUnhealthy(t *testing.T) {
	executor := &poolExecutor{}
	sandbox := &Sandbox{ContainerName: "sandbox", needsReplace: true}
	pool := NewExecutorSandboxPool(executor, []*Sandbox{sandbox})

	if _, err := pool.Acquire(context.Background(), Limits{MemoryLimitMb: 128}); err != nil {
		t.Fatalf("initial Acquire() error = %v", err)
	}
	pool.Release(sandbox)
	borrowed, err := pool.Acquire(context.Background(), Limits{MemoryLimitMb: 128})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if borrowed.ContainerName != "replacement" {
		t.Fatalf("borrowed sandbox = %q, want replacement", borrowed.ContainerName)
	}
	if executor.resets != 0 {
		t.Fatalf("reset calls = %d, want 0 for unhealthy sandbox replacement", executor.resets)
	}
}

func TestSandboxPoolCloseRemovesAvailableSandboxes(t *testing.T) {
	executor := &poolExecutor{}
	first := &Sandbox{ContainerName: "first"}
	second := &Sandbox{ContainerName: "second"}
	p := NewExecutorSandboxPool(executor, []*Sandbox{first, second})

	p.Close()
	p.Close()

	if executor.removed != 2 {
		t.Fatalf("removed sandboxes = %d, want 2", executor.removed)
	}
	if stats := p.Stats(); stats.Total != 2 || stats.Available != 2 {
		t.Fatalf("pool stats after close = %+v, want retained capacity accounting", stats)
	}
}

func TestSandboxPoolRetriesReplacementWithoutLosingCapacity(t *testing.T) {
	executor := &poolExecutor{resetErr: errors.New("sandbox did not become ready"), startErrs: 1}
	sandbox := &Sandbox{ContainerName: "sandbox"}
	p := NewExecutorSandboxPool(executor, []*Sandbox{sandbox})

	if _, err := p.Acquire(context.Background(), Limits{MemoryLimitMb: 128}); err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	p.Release(sandbox)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	replacement, err := p.Acquire(ctx, Limits{MemoryLimitMb: 128})
	if err != nil {
		t.Fatalf("Acquire() after replacement retry error = %v", err)
	}
	if replacement.ContainerName != "replacement" {
		t.Fatalf("replacement = %q, want replacement sandbox", replacement.ContainerName)
	}
	if stats := p.Stats(); stats.Total != 1 || stats.Starting != 0 {
		t.Fatalf("pool stats after replacement = %+v, want one restored capacity slot", stats)
	}
	p.Release(replacement)
}
