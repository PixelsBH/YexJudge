package judge

import (
	"context"
	"fmt"
	"sync"

	"yexjudge/internal/judge/languages"
	"yexjudge/internal/runner"
)

// CompileFunc is the isolated compilation operation used by a compile worker.
// The worker pool owns scheduling and cancellation; the function owns the
// compiler environment and remains free to use a disposable container.
type CompileFunc func(context.Context, string, languages.Spec, Limits) (*runner.RunResult, error)

type compileRequest struct {
	ctx       context.Context
	workspace string
	spec      languages.Spec
	limits    Limits
	result    chan compileResult
}

type compileResult struct {
	result *runner.RunResult
	err    error
}

// CompileWorkerPool bounds compilation independently from submission workers
// and runtime sandboxes. Every request is handled by one dedicated worker,
// while the CompileFunc can preserve per-job isolation.
type CompileWorkerPool struct {
	compile  CompileFunc
	jobs     chan compileRequest
	ctx      context.Context
	cancel   context.CancelFunc
	capacity int
	workers  sync.WaitGroup
	close    sync.Once
}

func NewCompileWorkerPool(capacity int, compile CompileFunc) *CompileWorkerPool {
	if capacity < 1 {
		capacity = 1
	}
	if compile == nil {
		compile = func(context.Context, string, languages.Spec, Limits) (*runner.RunResult, error) {
			return nil, fmt.Errorf("compile worker has no compiler function")
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	pool := &CompileWorkerPool{
		compile:  compile,
		jobs:     make(chan compileRequest, capacity),
		ctx:      ctx,
		cancel:   cancel,
		capacity: capacity,
	}
	pool.workers.Add(capacity)
	for i := 0; i < capacity; i++ {
		go pool.worker()
	}
	return pool
}

func (p *CompileWorkerPool) Capacity() int {
	return p.capacity
}

func (p *CompileWorkerPool) Compile(
	ctx context.Context,
	workspace string,
	spec languages.Spec,
	limits Limits,
) (*runner.RunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	request := compileRequest{
		ctx:       ctx,
		workspace: workspace,
		spec:      spec,
		limits:    limits,
		result:    make(chan compileResult, 1),
	}

	select {
	case <-p.ctx.Done():
		return nil, fmt.Errorf("compile worker pool is closed: %w", context.Canceled)
	case <-ctx.Done():
		return nil, ctx.Err()
	case p.jobs <- request:
	}

	select {
	case <-p.ctx.Done():
		return nil, fmt.Errorf("compile worker pool is closed: %w", context.Canceled)
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-request.result:
		return result.result, result.err
	}
}

func (p *CompileWorkerPool) worker() {
	defer p.workers.Done()
	for {
		select {
		case <-p.ctx.Done():
			return
		case request := <-p.jobs:
			p.process(request)
		}
	}
}

func (p *CompileWorkerPool) process(request compileRequest) {
	select {
	case <-request.ctx.Done():
		request.result <- compileResult{err: request.ctx.Err()}
		return
	default:
	}

	jobCtx, cancel := context.WithCancel(p.ctx)
	stop := context.AfterFunc(request.ctx, cancel)
	result, err := p.compile(jobCtx, request.workspace, request.spec, request.limits)
	stop()
	cancel()
	request.result <- compileResult{result: result, err: err}
}

func (p *CompileWorkerPool) Close() {
	p.close.Do(func() {
		p.cancel()
		p.workers.Wait()
	})
}
