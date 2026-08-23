package judge

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
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
	mu        sync.Mutex
	all       map[*Sandbox]struct{}
	capacity  int
	starting  int
	closed    chan struct{}
	closeOnce sync.Once
	replaceWG sync.WaitGroup
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
		all:       makeSandboxSet(sandboxes),
		capacity:  len(sandboxes),
		closed:    make(chan struct{}),
	}
}

func makeSandboxSet(sandboxes []*Sandbox) map[*Sandbox]struct{} {
	all := make(map[*Sandbox]struct{}, len(sandboxes))
	for _, sandbox := range sandboxes {
		if sandbox != nil {
			all[sandbox] = struct{}{}
		}
	}
	return all
}

type PoolStats struct {
	Total     int `json:"total"`
	Available int `json:"available"`
	Busy      int `json:"busy"`
	Starting  int `json:"starting"`
}

func (p *ExecutorSandboxPool) Stats() PoolStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	available := len(p.available)
	total := p.capacity
	return PoolStats{
		Total:     total,
		Available: available,
		Busy:      total - available - p.starting,
		Starting:  p.starting,
	}
}

func (p *ExecutorSandboxPool) Acquire(ctx context.Context, limits Limits) (*Sandbox, error) {
	var sandbox *Sandbox
	select {
	case <-p.closed:
		return nil, fmt.Errorf("sandbox pool is closed")
	case sandbox = <-p.available:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if sandbox == nil {
		return nil, fmt.Errorf("sandbox pool returned an empty slot")
	}
	select {
	case <-p.closed:
		p.removeSandbox(sandbox)
		return nil, fmt.Errorf("sandbox pool is closed")
	default:
	}

	if err := p.executor.ConfigureSandbox(ctx, sandbox, limits); err != nil {
		p.Release(sandbox)
		return nil, err
	}

	return sandbox, nil
}

func (p *ExecutorSandboxPool) Release(sandbox *Sandbox) {
	if sandbox == nil {
		return
	}
	select {
	case <-p.closed:
		p.removeSandbox(sandbox)
		return
	default:
	}
	if sandbox.needsReplace {
		sandbox.needsReplace = false
		p.replace(sandbox)
		return
	}

	if sandbox.restarted {
		sandbox.restarted = false
		p.returnSandbox(sandbox)
		return
	}

	resetStarted := time.Now()
	resetCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := p.executor.ResetSandbox(resetCtx, sandbox)
	cancel()
	if err != nil {
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

	p.returnSandbox(sandbox)
}

func (p *ExecutorSandboxPool) replace(sandbox *Sandbox) {
	p.removeSandbox(sandbox)
	if replacement, err := p.startReplacement(); err == nil {
		p.addSandbox(replacement)
		p.returnSandbox(replacement)
		return
	} else {
		slog.Error("failed to replace reusable sandbox; retrying", "sandbox", sandbox.ContainerName, "error", err)
	}

	p.mu.Lock()
	if p.isClosingLocked() {
		p.mu.Unlock()
		return
	}
	p.starting++
	p.replaceWG.Add(1)
	p.mu.Unlock()
	go func() {
		decrementStarting := true
		defer p.replaceWG.Done()
		defer func() {
			if decrementStarting {
				p.mu.Lock()
				p.starting--
				p.mu.Unlock()
			}
		}()

		for {
			select {
			case <-p.closed:
				return
			case <-time.After(500 * time.Millisecond):
			}

			replacement, err := p.startReplacement()
			if err != nil {
				slog.Error("failed to retry reusable sandbox replacement", "error", err)
				continue
			}
			p.mu.Lock()
			p.starting--
			decrementStarting = false
			p.mu.Unlock()
			p.addSandbox(replacement)
			p.returnSandbox(replacement)
			return
		}
	}()
}

func (p *ExecutorSandboxPool) startReplacement() (*Sandbox, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return p.executor.StartSandbox(ctx)
}

func (p *ExecutorSandboxPool) addSandbox(sandbox *Sandbox) {
	if sandbox == nil {
		return
	}
	p.mu.Lock()
	p.all[sandbox] = struct{}{}
	p.mu.Unlock()
}

func (p *ExecutorSandboxPool) returnSandbox(sandbox *Sandbox) {
	select {
	case <-p.closed:
		p.removeSandbox(sandbox)
	case p.available <- sandbox:
	}
}

func (p *ExecutorSandboxPool) isClosingLocked() bool {
	select {
	case <-p.closed:
		return true
	default:
		return false
	}
}

func (p *ExecutorSandboxPool) removeSandbox(sandbox *Sandbox) {
	p.mu.Lock()
	delete(p.all, sandbox)
	p.mu.Unlock()
	p.executor.RemoveSandbox(sandbox)
}

// Close removes every sandbox currently owned by the pool, including
// replacements that were still being provisioned. It is safe to call more
// than once.
func (p *ExecutorSandboxPool) Close() {
	p.closeOnce.Do(func() {
		close(p.closed)
		p.replaceWG.Wait()

		p.mu.Lock()
		sandboxes := make([]*Sandbox, 0, len(p.all))
		for sandbox := range p.all {
			sandboxes = append(sandboxes, sandbox)
		}
		p.all = make(map[*Sandbox]struct{})
		p.mu.Unlock()

		for _, sandbox := range sandboxes {
			p.executor.RemoveSandbox(sandbox)
		}
	})
}
