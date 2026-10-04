package judge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

type SubmissionStore interface {
	Save(sub Submission) error
	Get(id string) (Submission, bool)
	Update(sub Submission) error
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
	db *sql.DB
}

func NewPostgresSubmissionStore(db *sql.DB) *PostgresSubmissionStore {
	return &PostgresSubmissionStore{db: db}
}

func (s *PostgresSubmissionStore) Save(sub Submission) error {
	ctx, cancel := context.WithTimeout(context.Background(), postgresOperationTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockSubmissionAdmission(ctx, tx); err != nil {
		return err
	}
	if err := insertSubmission(ctx, tx, sub); err != nil {
		return err
	}
	return tx.Commit()
}

func insertSubmission(ctx context.Context, tx *sql.Tx, sub Submission) error {
	jobJSON, err := json.Marshal(sub.Job)
	if err != nil {
		return err
	}

	resultJSON, err := marshalResult(sub.Result)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO submissions
		 (id, status, job, result, started_at, attempt_count, lease_expires_at, failure_message,
		  owner_service, owner_user)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, ''), NULLIF($10, ''))`,
		sub.ID,
		sub.Status,
		jobJSON,
		resultJSON,
		sub.StartedAt,
		sub.AttemptCount,
		sub.LeaseExpiresAt,
		sub.FailureMessage,
		sub.OwnerService,
		sub.OwnerUser,
	)
	return err
}

func (s *PostgresSubmissionStore) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, postgresOperationTimeout)
	defer cancel()
	return s.db.PingContext(ctx)
}

const submissionColumns = `id, status, job, result, created_at, started_at, attempt_count,
	lease_expires_at, failure_message, owner_service, owner_user`

func (s *PostgresSubmissionStore) Get(id string) (Submission, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), postgresOperationTimeout)
	defer cancel()
	sub, found, err := scanSubmission(s.db.QueryRowContext(ctx,
		`SELECT `+submissionColumns+` FROM submissions WHERE id = $1`, id))
	return sub, found && err == nil
}

func scanSubmission(row *sql.Row) (Submission, bool, error) {
	var sub Submission
	var jobJSON []byte
	var resultJSON sql.NullString
	var createdAt sql.NullTime
	var startedAt sql.NullTime
	var leaseExpiresAt sql.NullTime
	var failureMessage sql.NullString
	var ownerService, ownerUser sql.NullString

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
		&ownerService,
		&ownerUser,
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

	sub.OwnerService = ownerService.String
	sub.OwnerUser = ownerUser.String
	return sub, true, nil
}

func (s *PostgresSubmissionStore) Update(sub Submission) error {
	ctx, cancel := context.WithTimeout(context.Background(), postgresOperationTimeout)
	defer cancel()
	resultJSON, err := marshalResult(sub.Result)
	if err != nil {
		return err
	}

	leaseExpiresAt := sub.LeaseExpiresAt
	if sub.Status == SubmissionFinished || sub.Status == SubmissionFailed {
		leaseExpiresAt = nil
	}

	result, err := s.db.ExecContext(
		ctx,
		`UPDATE submissions
		 SET status = $2,
		     result = $3,
		     started_at = $4,
		     lease_expires_at = $5,
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
		leaseExpiresAt,
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
	ctx, cancel := context.WithTimeout(context.Background(), postgresOperationTimeout)
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
