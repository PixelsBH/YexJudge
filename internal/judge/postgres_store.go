package judge

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

type SubmissionStore interface {
	Save(sub Submission) error
	Get(id string) (Submission, bool)
	Update(sub Submission) error
}

type PostgresSubmissionStore struct {
	db *sql.DB
}

func NewPostgresSubmissionStore(db *sql.DB) *PostgresSubmissionStore {
	return &PostgresSubmissionStore{db: db}
}

func (s *PostgresSubmissionStore) Save(sub Submission) error {
	jobJSON, err := json.Marshal(sub.Job)
	if err != nil {
		return err
	}

	resultJSON, err := marshalResult(sub.Result)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(
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

func (s *PostgresSubmissionStore) Get(id string) (Submission, bool) {
	row := s.db.QueryRow(
		`SELECT id, status, job, result, started_at, attempt_count,
		        lease_expires_at, failure_message
		 FROM submissions
		 WHERE id = $1`,
		id,
	)

	var sub Submission
	var jobJSON []byte
	var resultJSON sql.NullString
	var startedAt sql.NullTime
	var leaseExpiresAt sql.NullTime
	var failureMessage sql.NullString

	err := row.Scan(
		&sub.ID,
		&sub.Status,
		&jobJSON,
		&resultJSON,
		&startedAt,
		&sub.AttemptCount,
		&leaseExpiresAt,
		&failureMessage,
	)
	if err == sql.ErrNoRows {
		return Submission{}, false
	}
	if err != nil {
		return Submission{}, false
	}

	if err := json.Unmarshal(jobJSON, &sub.Job); err != nil {
		return Submission{}, false
	}

	if resultJSON.Valid {
		var result Result
		if err := json.Unmarshal([]byte(resultJSON.String), &result); err != nil {
			return Submission{}, false
		}
		sub.Result = &result
	}
	if startedAt.Valid {
		sub.StartedAt = &startedAt.Time
	}
	if leaseExpiresAt.Valid {
		sub.LeaseExpiresAt = &leaseExpiresAt.Time
	}
	if failureMessage.Valid {
		sub.FailureMessage = failureMessage.String
	}

	return sub, true
}

func (s *PostgresSubmissionStore) Update(sub Submission) error {
	resultJSON, err := marshalResult(sub.Result)
	if err != nil {
		return err
	}

	leaseExpiresAt := sub.LeaseExpiresAt
	if sub.Status == SubmissionFinished || sub.Status == SubmissionFailed {
		leaseExpiresAt = nil
	}

	result, err := s.db.Exec(
		`UPDATE submissions
		 SET status = $2,
		     result = $3,
		     started_at = $4,
		     lease_expires_at = $5,
		     failure_message = $6,
		     updated_at = NOW()
		 WHERE id = $1
		   AND attempt_count = $7`,
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

func marshalResult(result *Result) ([]byte, error) {
	if result == nil {
		return nil, nil
	}

	return json.Marshal(result)
}
