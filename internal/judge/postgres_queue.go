package judge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

const (
	DefaultQueueLeaseDuration = 60 * time.Second
	DefaultQueueMaxAttempts   = 3
)

type SubmissionClaim struct {
	ID      string
	Attempt int
}

type SubmissionQueue interface {
	Enqueue(submissionID string) error
	Dequeue(ctx context.Context) (SubmissionClaim, error)
	RenewLease(ctx context.Context, claim SubmissionClaim) error
	RecoverExpired(ctx context.Context) (int, error)
	LeaseDuration() time.Duration
	Close()
}

type PostgresSubmissionQueue struct {
	db            *sql.DB
	pollInterval  time.Duration
	leaseDuration time.Duration
	maxAttempts   int
	closeOnce     sync.Once
	closed        chan struct{}
}

func NewPostgresSubmissionQueue(db *sql.DB, pollInterval time.Duration) *PostgresSubmissionQueue {
	return NewPostgresSubmissionQueueWithOptions(
		db,
		pollInterval,
		DefaultQueueLeaseDuration,
		DefaultQueueMaxAttempts,
	)
}

func NewPostgresSubmissionQueueWithOptions(
	db *sql.DB,
	pollInterval, leaseDuration time.Duration,
	maxAttempts int,
) *PostgresSubmissionQueue {
	if pollInterval <= 0 {
		pollInterval = 100 * time.Millisecond
	}
	if leaseDuration <= 0 {
		leaseDuration = DefaultQueueLeaseDuration
	}
	if maxAttempts <= 0 {
		maxAttempts = DefaultQueueMaxAttempts
	}
	return &PostgresSubmissionQueue{
		db:            db,
		pollInterval:  pollInterval,
		leaseDuration: leaseDuration,
		maxAttempts:   maxAttempts,
		closed:        make(chan struct{}),
	}
}

func (q *PostgresSubmissionQueue) LeaseDuration() time.Duration {
	return q.leaseDuration
}

func (q *PostgresSubmissionQueue) Enqueue(submissionID string) error {
	select {
	case <-q.closed:
		return fmt.Errorf("submission queue is closed")
	default:
		return nil
	}
}

func (q *PostgresSubmissionQueue) Dequeue(ctx context.Context) (SubmissionClaim, error) {
	for {
		select {
		case <-ctx.Done():
			return SubmissionClaim{}, ctx.Err()
		case <-q.closed:
			return SubmissionClaim{}, fmt.Errorf("submission queue is closed")
		default:
		}

		claim, found, err := q.claimNextSubmission(ctx)
		if err != nil {
			return SubmissionClaim{}, err
		}
		if found {
			return claim, nil
		}

		timer := time.NewTimer(q.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return SubmissionClaim{}, ctx.Err()
		case <-q.closed:
			timer.Stop()
			return SubmissionClaim{}, fmt.Errorf("submission queue is closed")
		case <-timer.C:
		}
	}
}

func (q *PostgresSubmissionQueue) RenewLease(ctx context.Context, claim SubmissionClaim) error {
	leaseExpiresAt := time.Now().Add(q.leaseDuration)
	result, err := q.db.ExecContext(
		ctx,
		`UPDATE submissions
		 SET lease_expires_at = $3,
		     updated_at = NOW()
		 WHERE id = $1
		   AND status = $2
		   AND attempt_count = $4
		   AND lease_expires_at > NOW()`,
		claim.ID,
		SubmissionRunning,
		leaseExpiresAt,
		claim.Attempt,
	)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("submission %q lease renewal lost attempt %d", claim.ID, claim.Attempt)
	}
	return nil
}

func (q *PostgresSubmissionQueue) RecoverExpired(ctx context.Context) (int, error) {
	tx, err := q.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(
		ctx,
		`SELECT id, attempt_count
		 FROM submissions
		 WHERE status = $1
		   AND lease_expires_at IS NOT NULL
		   AND lease_expires_at <= NOW()
		 ORDER BY lease_expires_at, created_at
		 FOR UPDATE SKIP LOCKED`,
		SubmissionRunning,
	)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type expiredAttempt struct {
		id      string
		attempt int
	}
	expired := make([]expiredAttempt, 0)
	for rows.Next() {
		var item expiredAttempt
		if err := rows.Scan(&item.id, &item.attempt); err != nil {
			return 0, err
		}
		expired = append(expired, item)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	for _, item := range expired {
		id := item.id
		attempt := item.attempt
		message := fmt.Sprintf("worker lease expired on attempt %d", attempt)
		if attempt < q.maxAttempts {
			message = fmt.Sprintf("%s; retrying attempt %d of %d", message, attempt+1, q.maxAttempts)
			if _, err := tx.ExecContext(
				ctx,
				`UPDATE submissions
				 SET status = $2,
				     result = NULL,
				     started_at = NULL,
				     lease_expires_at = NULL,
				     failure_message = $3,
				     updated_at = NOW()
				 WHERE id = $1`,
				id,
				SubmissionQueued,
				message,
			); err != nil {
				return 0, err
			}
		} else {
			resultJSON, marshalErr := json.Marshal(Result{
				Status:       InfrastructureError,
				ErrorMessage: fmt.Sprintf("%s; retry limit %d reached", message, q.maxAttempts),
			})
			if marshalErr != nil {
				return 0, marshalErr
			}
			if _, err := tx.ExecContext(
				ctx,
				`UPDATE submissions
				 SET status = $2,
				     result = $3,
				     lease_expires_at = NULL,
				     failure_message = $4,
				     updated_at = NOW()
				 WHERE id = $1`,
				id,
				SubmissionFailed,
				resultJSON,
				message,
			); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(expired), nil
}

func (q *PostgresSubmissionQueue) Close() {
	q.closeOnce.Do(func() {
		close(q.closed)
	})
}

func (q *PostgresSubmissionQueue) claimNextSubmission(ctx context.Context) (SubmissionClaim, bool, error) {
	tx, err := q.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return SubmissionClaim{}, false, err
	}
	defer tx.Rollback()

	var claim SubmissionClaim
	err = tx.QueryRowContext(
		ctx,
		`UPDATE submissions
		 SET status = $1,
		     attempt_count = attempt_count + 1,
		     started_at = NOW(),
		     lease_expires_at = NOW() + $3::interval,
		     updated_at = NOW()
		 WHERE id = (
		     SELECT id
		     FROM submissions
		     WHERE status = $2
		     ORDER BY created_at
		     FOR UPDATE SKIP LOCKED
		     LIMIT 1
		 )
		 RETURNING id, attempt_count`,
		SubmissionRunning,
		SubmissionQueued,
		leaseInterval(q.leaseDuration),
	).Scan(&claim.ID, &claim.Attempt)
	if err == sql.ErrNoRows {
		return SubmissionClaim{}, false, nil
	}
	if err != nil {
		return SubmissionClaim{}, false, err
	}

	if err := tx.Commit(); err != nil {
		return SubmissionClaim{}, false, err
	}

	return claim, true, nil
}

func leaseInterval(duration time.Duration) string {
	return fmt.Sprintf("%f seconds", duration.Seconds())
}
