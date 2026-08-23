package judge

import (
	"context"
	"log/slog"
	"time"
	"yexjudge/internal/observability"
)

type SandboxPool interface {
	Acquire(ctx context.Context, limits Limits) (*Sandbox, error)
	Release(sandbox *Sandbox)
}

type ExecutorSandboxPool struct {
	executor  Executor
	available chan *Sandbox
	metrics   *observability.Metrics
}

func NewExecutorSandboxPool(executor Executor, sandboxes []*Sandbox) *ExecutorSandboxPool {
	return NewExecutorSandboxPoolWithMetrics(executor, sandboxes, nil)
}

func NewExecutorSandboxPoolWithMetrics(executor Executor, sandboxes []*Sandbox, metrics *observability.Metrics) *ExecutorSandboxPool {
	available := make(chan *Sandbox, len(sandboxes))
	for _, sandbox := range sandboxes {
		available <- sandbox
	}

	return &ExecutorSandboxPool{
		executor:  executor,
		available: available,
		metrics:   metrics,
	}
}

type PoolStats struct {
	Total     int `json:"total"`
	Available int `json:"available"`
	Busy      int `json:"busy"`
}

func (p *ExecutorSandboxPool) Stats() PoolStats {
	available := len(p.available)
	return PoolStats{
		Total:     cap(p.available),
		Available: available,
		Busy:      cap(p.available) - available,
	}
}

func (p *ExecutorSandboxPool) Acquire(ctx context.Context, limits Limits) (*Sandbox, error) {
	var sandbox *Sandbox
	select {
	case sandbox = <-p.available:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if err := p.executor.ConfigureSandbox(ctx, sandbox, limits); err != nil {
		p.Release(sandbox)
		return nil, err
	}

	return sandbox, nil
}

func (p *ExecutorSandboxPool) Release(sandbox *Sandbox) {
	if sandbox.needsReplace {
		sandbox.needsReplace = false
		p.replace(sandbox)
		return
	}

	if sandbox.restarted {
		sandbox.restarted = false
		p.available <- sandbox
		return
	}

	resetStarted := time.Now()
	if err := p.executor.ResetSandbox(context.Background(), sandbox); err != nil {
		if p.metrics != nil {
			p.metrics.ObserveReset(time.Since(resetStarted))
		}
		slog.Error("failed to reset reusable sandbox, replacing it", "sandbox", sandbox.ContainerName, "error", err)
		p.replace(sandbox)
		return
	}
	if p.metrics != nil {
		p.metrics.ObserveReset(time.Since(resetStarted))
	}

	p.available <- sandbox
}

func (p *ExecutorSandboxPool) replace(sandbox *Sandbox) {
	p.executor.RemoveSandbox(sandbox)
	replacement, err := p.executor.StartSandbox(context.Background())
	if err != nil {
		slog.Error("failed to replace reusable sandbox", "sandbox", sandbox.ContainerName, "error", err)
		return
	}
	p.available <- replacement
}
