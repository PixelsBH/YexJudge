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
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
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
	claimed := make(chan string, submissionCount)
	errors := make(chan error, submissionCount)
	var wait sync.WaitGroup
	for i := 0; i < submissionCount; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			id, err := queue.Dequeue(ctx)
			if err != nil {
				errors <- err
				return
			}
			claimed <- id
		}()
	}
	wait.Wait()
	close(claimed)
	close(errors)

	for err := range errors {
		t.Fatalf("Dequeue() error = %v", err)
	}
	seen := make(map[string]struct{}, submissionCount)
	for id := range claimed {
		if _, exists := seen[id]; exists {
			t.Fatalf("submission %q was claimed more than once", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != submissionCount {
		t.Fatalf("claimed %d submissions, want %d", len(seen), submissionCount)
	}
}
