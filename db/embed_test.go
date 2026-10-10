package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var migrationTestSchemaID atomic.Uint64

type migrationTestSchema struct {
	url    string
	quoted string
}

func newMigrationTestSchema(t *testing.T) *migrationTestSchema {
	t.Helper()
	url := os.Getenv("YEXJUDGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("YEXJUDGE_TEST_DATABASE_URL is not set")
	}

	admin, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if err := admin.Ping(); err != nil {
		t.Fatalf("ping test database: %v", err)
	}

	name := fmt.Sprintf("yexjudge_migrations_%d_%d", time.Now().UnixNano(), migrationTestSchemaID.Add(1))
	quoted := `"` + name + `"`
	if _, err := admin.Exec(`CREATE SCHEMA ` + quoted); err != nil {
		t.Fatalf("create disposable schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + quoted + ` CASCADE`)
	})

	return &migrationTestSchema{url: url, quoted: quoted}
}

func (s *migrationTestSchema) openDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := sql.Open("pgx", s.url)
	if err != nil {
		t.Fatalf("open schema database: %v", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Exec(`SET search_path TO ` + s.quoted); err != nil {
		t.Fatalf("set schema search path: %v", err)
	}
	return database
}

func assertMigrationVersions(t *testing.T, database *sql.DB) {
	t.Helper()
	rows, err := database.Query(`SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("query migration versions: %v", err)
	}
	defer rows.Close()

	var versions []string
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			t.Fatalf("scan migration version: %v", err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migration versions: %v", err)
	}
	want := []string{"001_submissions.sql", "002_queue_leases.sql"}
	if fmt.Sprint(versions) != fmt.Sprint(want) {
		t.Fatalf("migration versions = %v, want %v", versions, want)
	}
}

func assertIndexExists(t *testing.T, database *sql.DB, name string) {
	t.Helper()
	var exists bool
	if err := database.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM pg_indexes
			WHERE schemaname = current_schema() AND indexname = $1
		)`, name).Scan(&exists); err != nil {
		t.Fatalf("check index %s: %v", name, err)
	}
	if !exists {
		t.Errorf("index %s was not created", name)
	}
}

func TestApplyFreshSchema(t *testing.T) {
	schema := newMigrationTestSchema(t)
	database := schema.openDB(t)

	if err := Apply(context.Background(), database); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if err := Apply(context.Background(), database); err != nil {
		t.Fatalf("second Apply() error = %v", err)
	}

	var hasLeaseColumn bool
	if err := database.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema()
			  AND table_name = 'submissions'
			  AND column_name = 'lease_expires_at'
		)`).Scan(&hasLeaseColumn); err != nil {
		t.Fatalf("check lease column: %v", err)
	}
	if !hasLeaseColumn {
		t.Fatal("fresh submissions schema has no lease_expires_at column")
	}

	assertIndexExists(t, database, "submissions_status_created_at_idx")
	assertIndexExists(t, database, "submissions_running_lease_idx")
	assertMigrationVersions(t, database)
}

func TestApplyUpgradesPreLeaseSchema(t *testing.T) {
	schema := newMigrationTestSchema(t)
	database := schema.openDB(t)
	if _, err := database.Exec(`
		CREATE TABLE submissions (
			id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			job JSONB NOT NULL,
			result JSONB,
			started_at TIMESTAMPTZ,
			attempt_count INTEGER NOT NULL DEFAULT 0,
			failure_message TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX submissions_status_created_at_idx
			ON submissions (status, created_at);
		INSERT INTO submissions (id, status, job, updated_at)
		VALUES ('legacy-running', 'running', '{}'::jsonb, '2025-01-01 00:00:00+00');`); err != nil {
		t.Fatalf("create pre-lease schema: %v", err)
	}

	if err := Apply(context.Background(), database); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	var attemptCount int
	var startedAt, leaseExpiresAt, updatedAt time.Time
	if err := database.QueryRow(`
		SELECT attempt_count, started_at, lease_expires_at, updated_at
		FROM submissions WHERE id = 'legacy-running'`).Scan(
		&attemptCount, &startedAt, &leaseExpiresAt, &updatedAt); err != nil {
		t.Fatalf("read upgraded legacy submission: %v", err)
	}
	if attemptCount != 1 {
		t.Errorf("legacy attempt_count = %d, want 1", attemptCount)
	}
	if !startedAt.Equal(updatedAt) {
		t.Errorf("legacy started_at = %s, want updated_at %s", startedAt, updatedAt)
	}
	if got := leaseExpiresAt.Sub(updatedAt); got != time.Minute {
		t.Errorf("legacy lease duration = %s, want %s", got, time.Minute)
	}

	assertIndexExists(t, database, "submissions_status_created_at_idx")
	assertIndexExists(t, database, "submissions_running_lease_idx")
	assertMigrationVersions(t, database)
}

func TestApplySerializesConcurrentBootstrap(t *testing.T) {
	schema := newMigrationTestSchema(t)
	databases := []*sql.DB{schema.openDB(t), schema.openDB(t)}

	start := make(chan struct{})
	errors := make(chan error, len(databases))
	var wait sync.WaitGroup
	for _, database := range databases {
		wait.Add(1)
		go func(database *sql.DB) {
			defer wait.Done()
			<-start
			errors <- Apply(context.Background(), database)
		}(database)
	}
	close(start)
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent Apply() error = %v", err)
		}
	}

	assertIndexExists(t, databases[0], "submissions_status_created_at_idx")
	assertIndexExists(t, databases[0], "submissions_running_lease_idx")
	assertMigrationVersions(t, databases[0])
}
