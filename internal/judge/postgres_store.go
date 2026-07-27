package judge

import (
	"database/sql"
	"encoding/json"
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
		`INSERT INTO submissions (id, status, job, result)
		 VALUES ($1, $2, $3, $4)`,
		sub.ID,
		sub.Status,
		jobJSON,
		resultJSON,
	)
	return err
}

func (s *PostgresSubmissionStore) Get(id string) (Submission, bool) {
	row := s.db.QueryRow(
		`SELECT id, status, job, result
		 FROM submissions
		 WHERE id = $1`,
		id,
	)

	var sub Submission
	var jobJSON []byte
	var resultJSON sql.NullString

	err := row.Scan(&sub.ID, &sub.Status, &jobJSON, &resultJSON)
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

	return sub, true
}

func (s *PostgresSubmissionStore) Update(sub Submission) error {
	resultJSON, err := marshalResult(sub.Result)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(
		`UPDATE submissions
		 SET status = $2,
		     result = $3,
		     updated_at = NOW()
		 WHERE id = $1`,
		sub.ID,
		sub.Status,
		resultJSON,
	)
	return err
}

func marshalResult(result *Result) ([]byte, error) {
	if result == nil {
		return nil, nil
	}

	return json.Marshal(result)
}
