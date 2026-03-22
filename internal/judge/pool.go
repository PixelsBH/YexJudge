package judge

import "context"

type SandboxPool interface {
	Acquire(ctx context.Context, workspace string, limits Limits) (*Sandbox, error)
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

func (p *ExecutorSandboxPool) Acquire(ctx context.Context, workspace string, limits Limits) (*Sandbox, error) {
	return p.executor.StartSandbox(ctx, workspace, limits)
}

func (p *ExecutorSandboxPool) Release(sandbox *Sandbox) {
	p.executor.ReleaseSandbox(sandbox)
}
