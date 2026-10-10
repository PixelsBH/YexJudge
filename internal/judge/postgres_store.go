package judge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	DefaultMaxQueued                 = 100
	databaseOperationTimeout         = 5 * time.Second
	submissionAdmissionLockKey int64 = 824739106
)

var ErrQueueFull = errors.New("submission queue is full")

type SubmissionStore interface {
	Save(sub Submission) error
	Get(id string) (Submission, bool)
	Update(sub Submission) error
}

type ContextSubmissionStore interface {
	SaveContext(ctx context.Context, sub Submission) error
	GetContext(ctx context.Context, id string) (Submission, bool, error)
	UpdateContext(ctx context.Context, sub Submission) error
}

type AdmittingSubmissionStore interface {
	SaveAdmitted(ctx context.Context, sub Submission) error
}

type SubmissionCounts struct {
	Queued  int64 `json:"queued"`
	Running int64 `json:"running"`
	Failed  int64 `json:"failed"`
}

type SubmissionStatsProvider interface {
	Counts() (SubmissionCounts, error)
}

type PostgresSubmissionStore struct {
	db        *sql.DB
	maxQueued int
}

func NewPostgresSubmissionStore(db *sql.DB) *PostgresSubmissionStore {
	return NewPostgresSubmissionStoreWithQueueLimit(db, DefaultMaxQueued)
}

func NewPostgresSubmissionStoreWithQueueLimit(db *sql.DB, maxQueued int) *PostgresSubmissionStore {
	if maxQueued <= 0 {
		maxQueued = DefaultMaxQueued
	}
	return &PostgresSubmissionStore{db: db, maxQueued: maxQueued}
}

func (s *PostgresSubmissionStore) Save(sub Submission) error {
	ctx, cancel := context.WithTimeout(context.Background(), databaseOperationTimeout)
	defer cancel()
	return s.SaveContext(ctx, sub)
}

func (s *PostgresSubmissionStore) SaveContext(ctx context.Context, sub Submission) error {
	opCtx, cancel := context.WithTimeout(ctx, databaseOperationTimeout)
	defer cancel()
	return insertSubmission(opCtx, s.db, sub)
}

// SaveAdmitted serializes queue admissions across server replicas before
// checking the durable queued-row count and inserting the new submission.
func (s *PostgresSubmissionStore) SaveAdmitted(ctx context.Context, sub Submission) error {
	opCtx, cancel := context.WithTimeout(ctx, databaseOperationTimeout)
	defer cancel()

	tx, err := s.db.BeginTx(opCtx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(opCtx, `SELECT pg_advisory_xact_lock($1)`, submissionAdmissionLockKey); err != nil {
		return err
	}

	var queued int
	if err := tx.QueryRowContext(opCtx, `SELECT COUNT(*) FROM submissions WHERE status = $1`, SubmissionQueued).Scan(&queued); err != nil {
		return err
	}
	if queued >= s.maxQueued {
		return ErrQueueFull
	}
	if err := insertSubmission(opCtx, tx, sub); err != nil {
		return err
	}
	return tx.Commit()
}

func insertSubmission(ctx context.Context, executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, sub Submission) error {
	jobJSON, err := json.Marshal(sub.Job)
	if err != nil {
		return err
	}
	resultJSON, err := marshalResult(sub.Result)
	if err != nil {
		return err
	}
	_, err = executor.ExecContext(
		ctx,
		`INSERT INTO submissions
		 (id, status, job, result, started_at, attempt_count, lease_expires_at, failure_message)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		sub.ID,
		sub.Status,
		jobJSON,
		resultJSON,
		sub.StartedAt,
		sub.AttemptCount,
		sub.LeaseExpiresAt,
		sub.FailureMessage,
	)
	return err
}

func (s *PostgresSubmissionStore) Ready(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *PostgresSubmissionStore) Get(id string) (Submission, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), databaseOperationTimeout)
	defer cancel()
	submission, found, _ := s.GetContext(ctx, id)
	return submission, found
}

func (s *PostgresSubmissionStore) GetContext(ctx context.Context, id string) (Submission, bool, error) {
	opCtx, cancel := context.WithTimeout(ctx, databaseOperationTimeout)
	defer cancel()
	row := s.db.QueryRowContext(
		opCtx,
		`SELECT id, status, job, result, created_at, started_at, attempt_count,
		        lease_expires_at, failure_message
		 FROM submissions
		 WHERE id = $1`,
		id,
	)

	var sub Submission
	var jobJSON []byte
	var resultJSON sql.NullString
	var createdAt sql.NullTime
	var startedAt sql.NullTime
	var leaseExpiresAt sql.NullTime
	var failureMessage sql.NullString

	err := row.Scan(
		&sub.ID,
		&sub.Status,
		&jobJSON,
		&resultJSON,
		&createdAt,
		&startedAt,
		&sub.AttemptCount,
		&leaseExpiresAt,
		&failureMessage,
	)
	if err == sql.ErrNoRows {
		return Submission{}, false, nil
	}
	if err != nil {
		return Submission{}, false, err
	}

	if err := json.Unmarshal(jobJSON, &sub.Job); err != nil {
		return Submission{}, false, err
	}

	if resultJSON.Valid {
		var result Result
		if err := json.Unmarshal([]byte(resultJSON.String), &result); err != nil {
			return Submission{}, false, err
		}
		sub.Result = &result
	}
	if startedAt.Valid {
		sub.StartedAt = &startedAt.Time
	}
	if createdAt.Valid {
		sub.CreatedAt = &createdAt.Time
	}
	if leaseExpiresAt.Valid {
		sub.LeaseExpiresAt = &leaseExpiresAt.Time
	}
	if failureMessage.Valid {
		sub.FailureMessage = failureMessage.String
	}

	return sub, true, nil
}

func (s *PostgresSubmissionStore) Update(sub Submission) error {
	ctx, cancel := context.WithTimeout(context.Background(), databaseOperationTimeout)
	defer cancel()
	return s.UpdateContext(ctx, sub)
}

func (s *PostgresSubmissionStore) UpdateContext(ctx context.Context, sub Submission) error {
	opCtx, cancel := context.WithTimeout(ctx, databaseOperationTimeout)
	defer cancel()
	resultJSON, err := marshalResult(sub.Result)
	if err != nil {
		return err
	}

	result, err := s.db.ExecContext(
		opCtx,
		`UPDATE submissions
		 SET status = $2,
		     result = $3,
		     started_at = $4,
		     lease_expires_at = CASE
		         WHEN $2 IN ('finished', 'failed') THEN NULL
		         WHEN $2 = 'running' THEN lease_expires_at
		         ELSE $5
		     END,
		     failure_message = $6,
		     updated_at = NOW()
		 WHERE id = $1
		   AND attempt_count = $7
		   AND (
		       (status = 'running' AND lease_expires_at > NOW())
		       OR (status = 'queued' AND $2 = 'failed' AND attempt_count = 0)
		   )`,
		sub.ID,
		sub.Status,
		resultJSON,
		sub.StartedAt,
		sub.LeaseExpiresAt,
		sub.FailureMessage,
		sub.AttemptCount,
	)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("submission %q lease or attempt is no longer owned", sub.ID)
	}
	return nil
}

func (s *PostgresSubmissionStore) Counts() (SubmissionCounts, error) {
	ctx, cancel := context.WithTimeout(context.Background(), databaseOperationTimeout)
	defer cancel()
	var counts SubmissionCounts
	err := s.db.QueryRowContext(
		ctx,
		`SELECT
			COUNT(*) FILTER (WHERE status = $1),
			COUNT(*) FILTER (WHERE status = $2),
			COUNT(*) FILTER (WHERE status = $3)
		 FROM submissions`,
		SubmissionQueued,
		SubmissionRunning,
		SubmissionFailed,
	).Scan(&counts.Queued, &counts.Running, &counts.Failed)
	return counts, err
}

func marshalResult(result *Result) ([]byte, error) {
	if result == nil {
		return nil, nil
	}

	return json.Marshal(result)
}
