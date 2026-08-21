package judge

import (
	"context"
	"errors"
	"testing"

	"yexjudge/internal/judge/languages"
	"yexjudge/internal/runner"
)

type poolExecutor struct {
	resetErr  error
	resets    int
	removed   int
	started   int
	configure int
}

func (e *poolExecutor) Compile(context.Context, string, languages.Spec, Limits) (*runner.RunResult, error) {
	return &runner.RunResult{}, nil
}
func (e *poolExecutor) StartSandbox(context.Context) (*Sandbox, error) {
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
