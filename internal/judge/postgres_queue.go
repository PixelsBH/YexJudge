package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"yexjudge/internal/dbquery"
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
	db            *pgxpool.Pool
	queries       *dbquery.Queries
	pollInterval  time.Duration
	leaseDuration time.Duration
	maxAttempts   int
	closeOnce     sync.Once
	closed        chan struct{}
}

func NewPostgresSubmissionQueue(db *pgxpool.Pool, pollInterval time.Duration) *PostgresSubmissionQueue {
	return NewPostgresSubmissionQueueWithOptions(
		db,
		pollInterval,
		DefaultQueueLeaseDuration,
		DefaultQueueMaxAttempts,
	)
}

func NewPostgresSubmissionQueueWithOptions(
	db *pgxpool.Pool,
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
		queries:       dbquery.New(db),
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
	opCtx, cancel := context.WithTimeout(ctx, databaseOperationTimeout)
	defer cancel()
	rows, err := q.queries.RenewSubmissionLease(opCtx, dbquery.RenewSubmissionLeaseParams{
		LeaseSeconds: q.leaseDuration.Seconds(),
		ID:           claim.ID,
		Status:       string(SubmissionRunning),
		AttemptCount: int32(claim.Attempt),
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("submission %q lease renewal lost attempt %d", claim.ID, claim.Attempt)
	}
	return nil
}

func (q *PostgresSubmissionQueue) RecoverExpired(ctx context.Context) (int, error) {
	opCtx, cancel := context.WithTimeout(ctx, databaseOperationTimeout)
	defer cancel()
	tx, err := q.db.BeginTx(opCtx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	queries := q.queries.WithTx(tx)
	expired, err := queries.LockExpiredSubmissions(opCtx, string(SubmissionRunning))
	if err != nil {
		return 0, err
	}
	for _, item := range expired {
		attempt := int(item.AttemptCount)
		message := fmt.Sprintf("worker lease expired on attempt %d", attempt)
		var rows int64
		if attempt < q.maxAttempts {
			message = fmt.Sprintf("%s; retrying attempt %d of %d", message, attempt+1, q.maxAttempts)
			rows, err = queries.RequeueExpiredSubmission(opCtx, dbquery.RequeueExpiredSubmissionParams{
				Status:         string(SubmissionQueued),
				FailureMessage: nullableText(message),
				ID:             item.ID,
			})
		} else {
			// Operators get the detail in the log; the submission row keeps a
			// generic message so internal queue state is not exposed to users.
			slog.Error("submission lease expired without completion",
				"submission_id", item.ID,
				"attempt", attempt,
				"max_attempts", q.maxAttempts,
			)
			userMessage := "The judge timed out while processing this submission. Please retry."
			resultJSON, marshalErr := json.Marshal(Result{
				Status:       InfrastructureError,
				ErrorMessage: userMessage,
			})
			if marshalErr != nil {
				return 0, marshalErr
			}
			rows, err = queries.FailExpiredSubmission(opCtx, dbquery.FailExpiredSubmissionParams{
				Status:         string(SubmissionFailed),
				Result:         resultJSON,
				FailureMessage: nullableText(userMessage),
				ID:             item.ID,
			})
		}
		if err != nil {
			return 0, err
		}
		if rows != 1 {
			return 0, fmt.Errorf("locked expired submission %q was not updated", item.ID)
		}
	}
	if err := tx.Commit(opCtx); err != nil {
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
	opCtx, cancel := context.WithTimeout(ctx, databaseOperationTimeout)
	defer cancel()
	row, err := q.queries.ClaimNextSubmission(opCtx, dbquery.ClaimNextSubmissionParams{
		RunningStatus: string(SubmissionRunning),
		LeaseSeconds:  q.leaseDuration.Seconds(),
		QueuedStatus:  string(SubmissionQueued),
	})
	if err == pgx.ErrNoRows {
		return SubmissionClaim{}, false, nil
	}
	if err != nil {
		return SubmissionClaim{}, false, err
	}
	return SubmissionClaim{ID: row.ID, Attempt: int(row.AttemptCount)}, true, nil
}
