package judge

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func openIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	url := os.Getenv("YEXJUDGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("YEXJUDGE_TEST_DATABASE_URL is not set")
	}

	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("database ping error = %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS submissions (
			id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			job JSONB NOT NULL,
			result JSONB,
			started_at TIMESTAMPTZ,
			attempt_count INTEGER NOT NULL DEFAULT 0,
			lease_expires_at TIMESTAMPTZ,
			failure_message TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		ALTER TABLE submissions ADD COLUMN IF NOT EXISTS started_at TIMESTAMPTZ;
		ALTER TABLE submissions ADD COLUMN IF NOT EXISTS attempt_count INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE submissions ADD COLUMN IF NOT EXISTS lease_expires_at TIMESTAMPTZ;
		ALTER TABLE submissions ADD COLUMN IF NOT EXISTS failure_message TEXT;
		CREATE INDEX IF NOT EXISTS submissions_status_created_at_idx
			ON submissions (status, created_at);`); err != nil {
		t.Fatalf("create integration schema error = %v", err)
	}
	return db
}

func integrationSubmission(id string) Submission {
	return Submission{
		ID:     id,
		Status: SubmissionQueued,
		Job: Job{
			Language:   "python",
			SourceCode: "print(1)",
			TestCases:  []TestCase{{ID: 1, ExpectedOutput: "1"}},
			Limits:     Limits{TimeLimitMs: 1000, MemoryLimitMb: 128},
		},
	}
}

func TestPostgresSubmissionStoreIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	store := NewPostgresSubmissionStore(db)
	id := fmt.Sprintf("integration-store-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM submissions WHERE id = $1`, id) })

	submission := integrationSubmission(id)
	if err := store.Save(submission); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, ok := store.Get(id)
	if !ok {
		t.Fatal("Get() did not find saved submission")
	}
	if got.ID != id || got.Status != SubmissionQueued || got.Job.SourceCode != submission.Job.SourceCode {
		t.Fatalf("saved submission = %+v, want %+v", got, submission)
	}
	if got.CreatedAt == nil {
		t.Fatal("saved submission has no created timestamp")
	}

	result := Result{Status: Accepted, RuntimeMs: 4}
	submission.Status = SubmissionFinished
	submission.Result = &result
	if err := store.Update(submission); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	got, ok = store.Get(id)
	if !ok || got.Status != SubmissionFinished || got.Result == nil || got.Result.Status != Accepted {
		t.Fatalf("updated submission = %+v, want finished accepted result", got)
	}
}

func TestPostgresQueueClaimsEachSubmissionOnce(t *testing.T) {
	db := openIntegrationDB(t)
	store := NewPostgresSubmissionStore(db)
	queue := NewPostgresSubmissionQueue(db, 5*time.Millisecond)
	t.Cleanup(func() { queue.Close() })

	const submissionCount = 8
	ids := make([]string, 0, submissionCount)
	for i := 0; i < submissionCount; i++ {
		id := fmt.Sprintf("integration-queue-%d-%d", time.Now().UnixNano(), i)
		ids = append(ids, id)
		if err := store.Save(integrationSubmission(id)); err != nil {
			t.Fatalf("Save(%q) error = %v", id, err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM submissions WHERE id LIKE 'integration-queue-%'`)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	claimed := make(chan SubmissionClaim, submissionCount)
	errors := make(chan error, submissionCount)
	var wait sync.WaitGroup
	for i := 0; i < submissionCount; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			claim, err := queue.Dequeue(ctx)
			if err != nil {
				errors <- err
				return
			}
			claimed <- claim
		}()
	}
	wait.Wait()
	close(claimed)
	close(errors)

	for err := range errors {
		t.Fatalf("Dequeue() error = %v", err)
	}
	seen := make(map[string]struct{}, submissionCount)
	for claim := range claimed {
		if claim.Attempt != 1 {
			t.Fatalf("claim %q attempt = %d, want first attempt", claim.ID, claim.Attempt)
		}
		if _, exists := seen[claim.ID]; exists {
			t.Fatalf("submission %q was claimed more than once", claim.ID)
		}
		seen[claim.ID] = struct{}{}
	}
	if len(seen) != submissionCount {
		t.Fatalf("claimed %d submissions, want %d", len(seen), submissionCount)
	}
}

func TestPostgresQueueRecoversExpiredAttempts(t *testing.T) {
	db := openIntegrationDB(t)
	store := NewPostgresSubmissionStore(db)
	queue := NewPostgresSubmissionQueueWithOptions(db, 5*time.Millisecond, time.Second, 2)
	t.Cleanup(func() { queue.Close() })

	retryID := fmt.Sprintf("integration-retry-%d", time.Now().UnixNano())
	finalID := fmt.Sprintf("integration-final-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM submissions WHERE id IN ($1, $2)`, retryID, finalID)
	})
	if err := store.Save(integrationSubmission(retryID)); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(integrationSubmission(finalID)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		UPDATE submissions
		SET status = $2, attempt_count = $3, started_at = NOW() - INTERVAL '2 minutes',
		    lease_expires_at = NOW() - INTERVAL '1 second'
		WHERE id = $1`, retryID, SubmissionRunning, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		UPDATE submissions
		SET status = $2, attempt_count = $3, started_at = NOW() - INTERVAL '2 minutes',
		    lease_expires_at = NOW() - INTERVAL '1 second'
		WHERE id = $1`, finalID, SubmissionRunning, 2); err != nil {
		t.Fatal(err)
	}

	recovered, err := queue.RecoverExpired(context.Background())
	if err != nil {
		t.Fatalf("RecoverExpired() error = %v", err)
	}
	if recovered != 2 {
		t.Fatalf("recovered = %d, want 2", recovered)
	}

	retry, ok := store.Get(retryID)
	if !ok || retry.Status != SubmissionQueued || retry.AttemptCount != 1 || retry.Result != nil {
		t.Fatalf("retry submission = %+v, want queued attempt 1 without result", retry)
	}
	final, ok := store.Get(finalID)
	if !ok || final.Status != SubmissionFailed || final.Result == nil || final.Result.Status != InfrastructureError {
		t.Fatalf("final submission = %+v, want failed infrastructure result", final)
	}

	claim, err := queue.Dequeue(context.Background())
	if err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}
	if claim.ID != retryID || claim.Attempt != 2 {
		t.Fatalf("claim = %+v, want retry attempt 2", claim)
	}
	if err := queue.RenewLease(context.Background(), claim); err != nil {
		t.Fatalf("RenewLease() error = %v", err)
	}
	owned, ok := store.Get(retryID)
	if !ok || owned.LeaseExpiresAt == nil || owned.AttemptCount != claim.Attempt {
		t.Fatalf("renewed submission = %+v, want active lease", owned)
	}
}

func TestPostgresStoreRejectsStaleAttemptUpdate(t *testing.T) {
	db := openIntegrationDB(t)
	store := NewPostgresSubmissionStore(db)
	id := fmt.Sprintf("integration-fence-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM submissions WHERE id = $1`, id) })

	submission := integrationSubmission(id)
	if err := store.Save(submission); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE submissions SET attempt_count = 2 WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(submission); err == nil {
		t.Fatal("Update() accepted a stale attempt")
	}
}
