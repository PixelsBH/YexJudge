package judge

import "context"

// CompileLimiter bounds the number of compile containers that may exist at
// once. Runtime sandboxes have their own capacity limit in SandboxPool.
type CompileLimiter struct {
	slots chan struct{}
}

func NewCompileLimiter(capacity int) *CompileLimiter {
	if capacity < 1 {
		capacity = 1
	}
	return &CompileLimiter{slots: make(chan struct{}, capacity)}
}

func (l *CompileLimiter) Acquire(ctx context.Context) error {
	select {
	case l.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *CompileLimiter) Release() {
	select {
	case <-l.slots:
	default:
		panic("judge: compile limiter released without an acquired slot")
	}
}

func (l *CompileLimiter) Capacity() int {
	return cap(l.slots)
}
