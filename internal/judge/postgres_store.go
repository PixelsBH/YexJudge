package judge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"yexjudge/internal/dbquery"
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
	db        *pgxpool.Pool
	queries   *dbquery.Queries
	maxQueued int
}

func NewPostgresSubmissionStore(db *pgxpool.Pool) *PostgresSubmissionStore {
	return NewPostgresSubmissionStoreWithQueueLimit(db, DefaultMaxQueued)
}

func NewPostgresSubmissionStoreWithQueueLimit(db *pgxpool.Pool, maxQueued int) *PostgresSubmissionStore {
	if maxQueued <= 0 {
		maxQueued = DefaultMaxQueued
	}
	return &PostgresSubmissionStore{db: db, queries: dbquery.New(db), maxQueued: maxQueued}
}

func (s *PostgresSubmissionStore) Save(sub Submission) error {
	ctx, cancel := context.WithTimeout(context.Background(), databaseOperationTimeout)
	defer cancel()
	return s.SaveContext(ctx, sub)
}

func (s *PostgresSubmissionStore) SaveContext(ctx context.Context, sub Submission) error {
	opCtx, cancel := context.WithTimeout(ctx, databaseOperationTimeout)
	defer cancel()
	return insertSubmission(opCtx, s.queries, sub)
}

// SaveAdmitted serializes queue admissions across server replicas before
// checking the durable queued-row count and inserting the new submission.
func (s *PostgresSubmissionStore) SaveAdmitted(ctx context.Context, sub Submission) error {
	opCtx, cancel := context.WithTimeout(ctx, databaseOperationTimeout)
	defer cancel()

	tx, err := s.db.BeginTx(opCtx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	queries := s.queries.WithTx(tx)
	if err := queries.AcquireSubmissionAdmissionLock(opCtx, submissionAdmissionLockKey); err != nil {
		return err
	}
	queued, err := queries.CountQueuedSubmissions(opCtx, string(SubmissionQueued))
	if err != nil {
		return err
	}
	if queued >= int64(s.maxQueued) {
		return ErrQueueFull
	}
	if err := insertSubmission(opCtx, queries, sub); err != nil {
		return err
	}
	return tx.Commit(opCtx)
}

func insertSubmission(ctx context.Context, queries *dbquery.Queries, sub Submission) error {
	jobJSON, err := json.Marshal(sub.Job)
	if err != nil {
		return err
	}
	resultJSON, err := marshalResult(sub.Result)
	if err != nil {
		return err
	}
	return queries.InsertSubmission(ctx, dbquery.InsertSubmissionParams{
		ID:             sub.ID,
		Status:         string(sub.Status),
		Job:            jobJSON,
		Result:         resultJSON,
		StartedAt:      nullableTimestamp(sub.StartedAt),
		AttemptCount:   int32(sub.AttemptCount),
		LeaseExpiresAt: nullableTimestamp(sub.LeaseExpiresAt),
		FailureMessage: nullableText(sub.FailureMessage),
	})
}

func (s *PostgresSubmissionStore) Ready(ctx context.Context) error {
	return s.db.Ping(ctx)
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

	row, err := s.queries.GetSubmission(opCtx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Submission{}, false, nil
	}
	if err != nil {
		return Submission{}, false, err
	}

	var sub Submission
	sub.ID = row.ID
	sub.Status = SubmissionStatus(row.Status)
	sub.AttemptCount = int(row.AttemptCount)
	if err := json.Unmarshal(row.Job, &sub.Job); err != nil {
		return Submission{}, false, err
	}
	if len(row.Result) > 0 {
		var result Result
		if err := json.Unmarshal(row.Result, &result); err != nil {
			return Submission{}, false, err
		}
		sub.Result = &result
	}
	if row.CreatedAt.Valid {
		createdAt := row.CreatedAt.Time
		sub.CreatedAt = &createdAt
	}
	if row.StartedAt.Valid {
		startedAt := row.StartedAt.Time
		sub.StartedAt = &startedAt
	}
	if row.LeaseExpiresAt.Valid {
		leaseExpiresAt := row.LeaseExpiresAt.Time
		sub.LeaseExpiresAt = &leaseExpiresAt
	}
	if row.FailureMessage.Valid {
		sub.FailureMessage = row.FailureMessage.String
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

	rows, err := s.queries.UpdateSubmission(opCtx, dbquery.UpdateSubmissionParams{
		NewStatus:      string(sub.Status),
		Result:         resultJSON,
		StartedAt:      nullableTimestamp(sub.StartedAt),
		LeaseExpiresAt: nullableTimestamp(sub.LeaseExpiresAt),
		FailureMessage: nullableText(sub.FailureMessage),
		ID:             sub.ID,
		AttemptCount:   int32(sub.AttemptCount),
	})
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
	counts, err := s.queries.CountSubmissionsByStatus(ctx, dbquery.CountSubmissionsByStatusParams{
		QueuedStatus:  string(SubmissionQueued),
		RunningStatus: string(SubmissionRunning),
		FailedStatus:  string(SubmissionFailed),
	})
	if err != nil {
		return SubmissionCounts{}, err
	}
	return SubmissionCounts{Queued: counts.Queued, Running: counts.Running, Failed: counts.Failed}, nil
}

func nullableTimestamp(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *value, Valid: true}
}

func nullableText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: true}
}

func marshalResult(result *Result) ([]byte, error) {
	if result == nil {
		return nil, nil
	}
	return json.Marshal(result)
}
