package judge

import (
	"context"
	"fmt"
	"sync"
)

type SubmissionQueue interface {
	Enqueue(submissionID string) error
	Dequeue(ctx context.Context) (string, error)
	Close()
}

type MemorySubmissionQueue struct {
	mu     sync.RWMutex
	ch     chan string
	closed bool
}

func NewMemorySubmissionQueue(buffer int) *MemorySubmissionQueue {
	return &MemorySubmissionQueue{
		ch: make(chan string, buffer),
	}
}

func (q *MemorySubmissionQueue) Enqueue(submissionID string) error {
	q.mu.RLock()
	defer q.mu.RUnlock()

	if q.closed {
		return fmt.Errorf("submission queue is closed")
	}

	select {
	case q.ch <- submissionID:
		return nil
	default:
		return fmt.Errorf("submission queue is full")
	}
}

func (q *MemorySubmissionQueue) Channel() <-chan string {
	return q.ch
}

func (q *MemorySubmissionQueue) Dequeue(ctx context.Context) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case submissionID, ok := <-q.ch:
		if !ok {
			return "", fmt.Errorf("submission queue is closed")
		}
		return submissionID, nil
	}
}

func (q *MemorySubmissionQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return
	}

	q.closed = true
	close(q.ch)
}
