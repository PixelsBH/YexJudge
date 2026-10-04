package judge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AdmissionLimits bounds new admissions. Nonpositive limits are disabled;
// user limits are scoped to the combination of owner service and owner user.
type AdmissionLimits struct {
	MaxQueued           int
	MaxActivePerService int
	MaxActivePerUser    int
}

var (
	ErrQueueFull        = errors.New("submission queue is full")
	ErrServiceLimit     = errors.New("service active submission limit reached")
	ErrUserLimit        = errors.New("user active submission limit reached")
	ErrSubmissionActive = errors.New("submission is still active")
)

const (
	postgresOperationTimeout = 5 * time.Second
	// Shared by every admission/insert and lease recovery, across all replicas.
	// This must remain distinct from the schema migration advisory lock.
	submissionAdmissionLockKey int64 = 824739106
)

func lockSubmissionAdmission(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", submissionAdmissionLockKey)
	return err
}

// SaveAdmitted checks capacity and inserts a queued submission in one transaction.
// Unowned submissions are permitted for unsecured callers but cannot be accessed
// through the owner-scoped methods. Use Save only for trusted compatibility paths.
func (s *PostgresSubmissionStore) SaveAdmitted(ctx context.Context, sub Submission, limits AdmissionLimits) error {
	if sub.Status != SubmissionQueued {
		return fmt.Errorf("admission requires a queued submission")
	}
	ctx, cancel := context.WithTimeout(ctx, postgresOperationTimeout)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockSubmissionAdmission(ctx, tx); err != nil {
		return err
	}

	// Read in a separate statement AFTER acquiring the lock. At READ COMMITTED
	// this sees admissions committed while we waited, unlike a combined
	// lock/count statement or a repeatable-read transaction.
	var queued, serviceActive, userActive int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FILTER (WHERE status = 'queued'),
		       COUNT(*) FILTER (WHERE owner_service = $1),
		       COUNT(*) FILTER (WHERE owner_service = $1 AND owner_user = $2)
		FROM submissions
		WHERE status IN ('queued', 'running')`, sub.OwnerService, sub.OwnerUser).
		Scan(&queued, &serviceActive, &userActive); err != nil {
		return err
	}
	if limits.MaxQueued > 0 && queued >= int64(limits.MaxQueued) {
		return ErrQueueFull
	}
	if limits.MaxActivePerService > 0 && serviceActive >= int64(limits.MaxActivePerService) {
		return ErrServiceLimit
	}
	if limits.MaxActivePerUser > 0 && userActive >= int64(limits.MaxActivePerUser) {
		return ErrUserLimit
	}
	if err := insertSubmission(ctx, tx, sub); err != nil {
		return err
	}
	return tx.Commit()
}

// GetOwned never loads an unscoped row, including when a caller supplies an
// empty identity. Legacy/unowned rows are deliberately inaccessible here.
func (s *PostgresSubmissionStore) GetOwned(ctx context.Context, id, service, user string) (Submission, bool, error) {
	if service == "" || user == "" {
		return Submission{}, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, postgresOperationTimeout)
	defer cancel()
	return scanSubmission(s.db.QueryRowContext(ctx,
		`SELECT `+submissionColumns+` FROM submissions
		 WHERE id = $1 AND owner_service = $2 AND owner_user = $3`,
		id, service, user))
}

// DeleteOwned locks only the scoped row so checking its state and deleting it
// cannot race a worker claim or recovery. Nonowners cannot distinguish an
// active submission from a nonexistent submission.
func (s *PostgresSubmissionStore) DeleteOwned(ctx context.Context, id, service, user string) (bool, error) {
	if service == "" || user == "" {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, postgresOperationTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var status SubmissionStatus
	err = tx.QueryRowContext(ctx, `SELECT status FROM submissions
		WHERE id = $1 AND owner_service = $2 AND owner_user = $3 FOR UPDATE`,
		id, service, user).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if status == SubmissionQueued || status == SubmissionRunning {
		return false, ErrSubmissionActive
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM submissions
		WHERE id = $1 AND owner_service = $2 AND owner_user = $3`, id, service, user)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return rows == 1, nil
}

// DeleteExpired removes at most limit terminal rows last updated before before.
// SKIP LOCKED allows independent retention jobs without blocking active workers.
func (s *PostgresSubmissionStore) DeleteExpired(ctx context.Context, before time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(ctx, postgresOperationTimeout)
	defer cancel()
	result, err := s.db.ExecContext(ctx, `
		WITH expired AS (
			SELECT id FROM submissions
			WHERE status IN ('finished', 'failed') AND updated_at < $1
			ORDER BY updated_at, id
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		DELETE FROM submissions AS s USING expired
		WHERE s.id = expired.id`, before, limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
