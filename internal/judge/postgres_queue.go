package judge

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

type SubmissionQueue interface {
	Enqueue(submissionID string) error
	Dequeue(ctx context.Context) (string, error)
	Close()
}

type PostgresSubmissionQueue struct {
	db           *sql.DB
	pollInterval time.Duration
	closeOnce    sync.Once
	closed       chan struct{}
}

func NewPostgresSubmissionQueue(db *sql.DB, pollInterval time.Duration) *PostgresSubmissionQueue {
	return &PostgresSubmissionQueue{
		db:           db,
		pollInterval: pollInterval,
		closed:       make(chan struct{}),
	}
}

func (q *PostgresSubmissionQueue) Enqueue(submissionID string) error {
	select {
	case <-q.closed:
		return fmt.Errorf("submission queue is closed")
	default:
		return nil
	}
}

func (q *PostgresSubmissionQueue) Dequeue(ctx context.Context) (string, error) {
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-q.closed:
			return "", fmt.Errorf("submission queue is closed")
		default:
		}

		submissionID, found, err := q.claimNextSubmission(ctx)
		if err != nil {
			return "", err
		}
		if found {
			return submissionID, nil
		}

		timer := time.NewTimer(q.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-q.closed:
			timer.Stop()
			return "", fmt.Errorf("submission queue is closed")
		case <-timer.C:
		}
	}
}

func (q *PostgresSubmissionQueue) Close() {
	q.closeOnce.Do(func() {
		close(q.closed)
	})
}

func (q *PostgresSubmissionQueue) claimNextSubmission(ctx context.Context) (string, bool, error) {
	tx, err := q.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()

	var submissionID string
	err = tx.QueryRowContext(
		ctx,
		`UPDATE submissions
		 SET status = $1,
		     updated_at = NOW()
		 WHERE id = (
		     SELECT id
		     FROM submissions
		     WHERE status = $2
		     ORDER BY created_at
		     FOR UPDATE SKIP LOCKED
		     LIMIT 1
		 )
		 RETURNING id`,
		SubmissionRunning,
		SubmissionQueued,
	).Scan(&submissionID)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}

	if err := tx.Commit(); err != nil {
		return "", false, err
	}

	return submissionID, true, nil
}
