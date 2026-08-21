package judge

import (
	"context"
	"log"
)

type SandboxPool interface {
	Acquire(ctx context.Context, limits Limits) (*Sandbox, error)
	Release(sandbox *Sandbox)
}

type ExecutorSandboxPool struct {
	executor  Executor
	available chan *Sandbox
}

func NewExecutorSandboxPool(executor Executor, sandboxes []*Sandbox) *ExecutorSandboxPool {
	available := make(chan *Sandbox, len(sandboxes))
	for _, sandbox := range sandboxes {
		available <- sandbox
	}

	return &ExecutorSandboxPool{
		executor:  executor,
		available: available,
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

	if err := p.executor.ResetSandbox(context.Background(), sandbox); err != nil {
		log.Println("failed to reset reusable sandbox, replacing it:", err)
		p.replace(sandbox)
		return
	}

	p.available <- sandbox
}

func (p *ExecutorSandboxPool) replace(sandbox *Sandbox) {
	p.executor.RemoveSandbox(sandbox)
	replacement, err := p.executor.StartSandbox(context.Background())
	if err != nil {
		log.Println("failed to replace reusable sandbox:", err)
		return
	}
	p.available <- replacement
}
