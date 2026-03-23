package judge

import "fmt"

type SubmissionQueue interface {
	Enqueue(submissionID string) error
}

type MemorySubmissionQueue struct {
	ch chan string
}

func NewMemorySubmissionQueue(buffer int) *MemorySubmissionQueue {
	return &MemorySubmissionQueue{
		ch: make(chan string, buffer),
	}
}

func (q *MemorySubmissionQueue) Enqueue(submissionID string) error {
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
