package judge

import (
	"context"
	"yexjudge/internal/judge/languages"
)

type SandboxPool interface {
	Acquire(ctx context.Context, workspace string, limits Limits, spec languages.Spec) (*Sandbox, error)
	Release(sandbox *Sandbox)
}

type ExecutorSandboxPool struct {
	executor Executor
}

func NewExecutorSandboxPool(executor Executor) *ExecutorSandboxPool {
	return &ExecutorSandboxPool{
		executor: executor,
	}
}

func (p *ExecutorSandboxPool) Acquire(ctx context.Context, workspace string, limits Limits, spec languages.Spec) (*Sandbox, error) {
	return p.executor.StartSandbox(ctx, workspace, limits, spec)
}

func (p *ExecutorSandboxPool) Release(sandbox *Sandbox) {
	p.executor.ReleaseSandbox(sandbox)
}
