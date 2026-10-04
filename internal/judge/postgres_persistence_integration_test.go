package judge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	schema "yexjudge/db"
)

// Use the existing opt-in environment convention, but isolate each test's
// schema so admission/retention counts do not depend on other test rows.
func openPersistenceIntegrationDB(t *testing.T, migrate bool) *sql.DB {
	t.Helper()
	url := os.Getenv("YEXJUDGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("YEXJUDGE_TEST_DATABASE_URL is not set")
	}
	config, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal("invalid integration database configuration")
	}
	config.ConnectTimeout = 5 * time.Second
	admin := stdlib.OpenDB(*config)
	admin.SetMaxOpenConns(1)
	name := fmt.Sprintf("persistence_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+identifier); err != nil {
		admin.Close()
		t.Fatalf("create integration schema: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Errorf("drop integration schema: %v", err)
		}
		admin.Close()
	})
	config.RuntimeParams["search_path"] = name
	database := stdlib.OpenDB(*config)
	database.SetMaxOpenConns(16)
	database.SetMaxIdleConns(4)
	t.Cleanup(func() { database.Close() })
	if migrate {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := schema.Apply(ctx, database); err != nil {
			t.Fatalf("apply integration migrations: %v", err)
		}
	}
	return database
}

func secondPersistenceIntegrationPool(t *testing.T, database *sql.DB) *sql.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var name string
	if err := database.QueryRowContext(ctx, "SELECT current_schema()").Scan(&name); err != nil {
		t.Fatal(err)
	}
	config, err := pgx.ParseConfig(os.Getenv("YEXJUDGE_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal("invalid integration database configuration")
	}
	config.ConnectTimeout = 5 * time.Second
	config.RuntimeParams["search_path"] = name
	other := stdlib.OpenDB(*config)
	other.SetMaxOpenConns(16)
	t.Cleanup(func() { other.Close() })
	return other
}

func ownedIntegrationSubmission(id, service, user string) Submission {
	sub := integrationSubmission(id)
	sub.OwnerService, sub.OwnerUser = service, user
	return sub
}

func TestPostgresPersistenceOwnerIsolation(t *testing.T) {
	database := openPersistenceIntegrationDB(t, true)
	store := NewPostgresSubmissionStore(database)
	ctx := context.Background()
	sub := ownedIntegrationSubmission("owned", "service-a", "user-a")
	if err := store.SaveAdmitted(ctx, sub, AdmissionLimits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(integrationSubmission("legacy")); err != nil {
		t.Fatal(err)
	}
	for _, identity := range [][2]string{{"service-b", "user-a"}, {"service-a", "user-b"}, {"service-b", "user-b"}, {"", "user-a"}, {"service-a", ""}, {"", ""}} {
		if _, found, err := store.GetOwned(ctx, sub.ID, identity[0], identity[1]); found || err != nil {
			t.Fatalf("GetOwned(%v) = %v, %v; want not found", identity, found, err)
		}
		if deleted, err := store.DeleteOwned(ctx, sub.ID, identity[0], identity[1]); deleted || err != nil {
			t.Fatalf("DeleteOwned(%v) = %v, %v; want not found", identity, deleted, err)
		}
	}
	for _, id := range []string{"legacy", "missing"} {
		if _, found, err := store.GetOwned(ctx, id, "service-a", "user-a"); found || err != nil {
			t.Fatalf("GetOwned(%q) = %v, %v; want not found", id, found, err)
		}
		if deleted, err := store.DeleteOwned(ctx, id, "service-a", "user-a"); deleted || err != nil {
			t.Fatalf("DeleteOwned(%q) = %v, %v; want not found", id, deleted, err)
		}
	}
	if deleted, err := store.DeleteOwned(ctx, sub.ID, sub.OwnerService, sub.OwnerUser); deleted || !errors.Is(err, ErrSubmissionActive) {
		t.Fatalf("delete queued = %v, %v; want ErrSubmissionActive", deleted, err)
	}
	got, found, err := store.GetOwned(ctx, sub.ID, sub.OwnerService, sub.OwnerUser)
	if err != nil || !found || got.OwnerService != sub.OwnerService || got.OwnerUser != sub.OwnerUser {
		t.Fatalf("GetOwned() = %+v, %v, %v", got, found, err)
	}

	// A malformed foreign job must not be read/decoded by a scoped lookup.
	if _, err := database.ExecContext(ctx, `UPDATE submissions SET job = '{"testCases":"invalid"}' WHERE id = $1`, sub.ID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetOwned(ctx, sub.ID, "other", sub.OwnerUser); found || err != nil {
		t.Fatalf("foreign malformed job lookup = %v, %v; want not found", found, err)
	}
	if _, found, err := store.GetOwned(ctx, sub.ID, sub.OwnerService, sub.OwnerUser); found || err == nil {
		t.Fatal("owned malformed job did not return a decoding error")
	}
	if _, err := database.ExecContext(ctx, `UPDATE submissions SET job = '{}' WHERE id = $1`, sub.ID); err != nil {
		t.Fatal(err)
	}
	queue := NewPostgresSubmissionQueueWithOptions(database, time.Millisecond, time.Minute, 2)
	t.Cleanup(queue.Close)
	claim, found, err := queue.claimNextSubmission(ctx)
	if err != nil || !found || claim.ID != sub.ID {
		t.Fatalf("claim = %+v, %v, %v", claim, found, err)
	}
	if deleted, err := store.DeleteOwned(ctx, sub.ID, sub.OwnerService, sub.OwnerUser); deleted || !errors.Is(err, ErrSubmissionActive) {
		t.Fatalf("delete running = %v, %v; want ErrSubmissionActive", deleted, err)
	}
	workerSub, found := store.Get(sub.ID)
	if !found || workerSub.OwnerService != sub.OwnerService || workerSub.OwnerUser != sub.OwnerUser {
		t.Fatal("worker Get did not preserve ownership")
	}
	workerSub.OwnerService, workerSub.OwnerUser = "forged-service", "forged-user"
	workerSub.Status, workerSub.Result = SubmissionFinished, &Result{Status: Accepted}
	if err := store.Update(workerSub); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetOwned(ctx, sub.ID, sub.OwnerService, sub.OwnerUser); !found || err != nil {
		t.Fatal("worker Update changed immutable ownership")
	}
	if deleted, err := store.DeleteOwned(ctx, sub.ID, sub.OwnerService, sub.OwnerUser); !deleted || err != nil {
		t.Fatalf("delete finished = %v, %v; want deleted", deleted, err)
	}
	if deleted, err := store.DeleteOwned(ctx, sub.ID, sub.OwnerService, sub.OwnerUser); deleted || err != nil {
		t.Fatalf("repeat delete = %v, %v; want not found", deleted, err)
	}
}

func TestPostgresPersistenceAdmissionRaces(t *testing.T) {
	for _, test := range []struct {
		name         string
		limits       AdmissionLimits
		wantError    error
		wantAccepted int
	}{
		{"global queue", AdmissionLimits{MaxQueued: 4}, ErrQueueFull, 4},
		{"service active", AdmissionLimits{MaxActivePerService: 4}, ErrServiceLimit, 3},
		{"user active", AdmissionLimits{MaxActivePerUser: 4}, ErrUserLimit, 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := openPersistenceIntegrationDB(t, true)
			stores := []*PostgresSubmissionStore{NewPostgresSubmissionStore(database), NewPostgresSubmissionStore(secondPersistenceIntegrationPool(t, database))}
			seed := ownedIntegrationSubmission("seed-running", "service", "user")
			seed.Status = SubmissionRunning
			if err := stores[0].Save(seed); err != nil {
				t.Fatal(err)
			}
			// Identical user IDs in other services and terminal rows must not
			// consume this service/user's quota (nor global queued capacity).
			for _, sub := range []Submission{
				{ID: "foreign-running", Status: SubmissionRunning, OwnerService: "other-service", OwnerUser: "user"},
				{ID: "terminal-finished", Status: SubmissionFinished, OwnerService: "service", OwnerUser: "user"},
				{ID: "terminal-failed", Status: SubmissionFailed, OwnerService: "service", OwnerUser: "user"},
			} {
				if err := stores[0].Save(sub); err != nil {
					t.Fatal(err)
				}
			}
			const attempts = 24
			start := make(chan struct{})
			results := make(chan error, attempts)
			var wait sync.WaitGroup
			for i := 0; i < attempts; i++ {
				wait.Add(1)
				go func(i int) {
					defer wait.Done()
					<-start
					user := "user"
					if test.name == "service active" {
						user = fmt.Sprintf("user-%d", i)
					}
					results <- stores[i%len(stores)].SaveAdmitted(context.Background(), ownedIntegrationSubmission(fmt.Sprintf("race-%d", i), "service", user), test.limits)
				}(i)
			}
			close(start)
			wait.Wait()
			close(results)
			accepted := 0
			for err := range results {
				if err == nil {
					accepted++
					continue
				}
				if !errors.Is(err, test.wantError) {
					t.Fatalf("admission error = %v, want %v", err, test.wantError)
				}
			}
			if accepted != test.wantAccepted {
				t.Fatalf("accepted %d, want %d", accepted, test.wantAccepted)
			}
			var persisted int
			if err := database.QueryRow(`SELECT COUNT(*) FROM submissions WHERE id LIKE 'race-%'`).Scan(&persisted); err != nil {
				t.Fatal(err)
			}
			if persisted != accepted {
				t.Fatalf("persisted %d, accepted %d", persisted, accepted)
			}
		})
	}
}

func TestPostgresPersistenceAdmissionRollbackAndLimits(t *testing.T) {
	database := openPersistenceIntegrationDB(t, true)
	store := NewPostgresSubmissionStore(database)
	ctx := context.Background()
	sub := ownedIntegrationSubmission("one", "service", "user")
	limits := AdmissionLimits{MaxQueued: 1, MaxActivePerService: 1, MaxActivePerUser: 1}
	if err := store.SaveAdmitted(ctx, sub, limits); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAdmitted(ctx, ownedIntegrationSubmission("rejected", "other", "other"), limits); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("admission error = %v", err)
	}
	if _, found := store.Get("rejected"); found {
		t.Fatal("rejected admission was persisted")
	}
	if err := store.SaveAdmitted(ctx, sub, AdmissionLimits{}); err == nil {
		t.Fatal("duplicate admission succeeded")
	}
	if err := store.SaveAdmitted(ctx, ownedIntegrationSubmission("two", "service", "user"), AdmissionLimits{MaxQueued: -1, MaxActivePerService: -1, MaxActivePerUser: -1}); err != nil {
		t.Fatalf("disabled limits error = %v", err)
	}
	terminal := ownedIntegrationSubmission("terminal", "service", "user")
	terminal.Status = SubmissionFinished
	if err := store.SaveAdmitted(ctx, terminal, AdmissionLimits{}); err == nil {
		t.Fatal("admission accepted a terminal submission")
	}
}

func TestPostgresPersistenceRetention(t *testing.T) {
	database := openPersistenceIntegrationDB(t, true)
	store := NewPostgresSubmissionStore(database)
	ctx := context.Background()
	before := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	for _, row := range []struct {
		id      string
		status  SubmissionStatus
		updated time.Time
	}{
		{"locked", SubmissionFinished, before.Add(-4 * time.Hour)},
		{"finished", SubmissionFinished, before.Add(-3 * time.Hour)},
		{"failed", SubmissionFailed, before.Add(-2 * time.Hour)},
		{"boundary", SubmissionFinished, before},
		{"recent", SubmissionFinished, before.Add(time.Minute)},
		{"queued", SubmissionQueued, before.Add(-time.Hour)},
		{"running", SubmissionRunning, before.Add(-time.Hour)},
	} {
		sub := integrationSubmission(row.id)
		sub.Status = row.status
		if err := store.Save(sub); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`UPDATE submissions SET created_at = $2::timestamptz - INTERVAL '1 day', updated_at = $2 WHERE id = $1`, row.id, row.updated); err != nil {
			t.Fatal(err)
		}
	}
	for _, limit := range []int{0, -1} {
		if count, err := store.DeleteExpired(ctx, before, limit); count != 0 || err != nil {
			t.Fatalf("nonpositive batch = %d, %v", count, err)
		}
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SELECT id FROM submissions WHERE id = 'locked' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	if count, err := store.DeleteExpired(ctx, before, 1); count != 1 || err != nil {
		t.Fatalf("first batch = %d, %v; want 1", count, err)
	}
	if _, found := store.Get("finished"); found {
		t.Fatal("oldest unlocked row was not deleted")
	}
	if count, err := store.DeleteExpired(ctx, before, 10); count != 1 || err != nil {
		t.Fatalf("second batch = %d, %v; want 1", count, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if count, err := store.DeleteExpired(ctx, before, 10); count != 1 || err != nil {
		t.Fatalf("unlocked batch = %d, %v; want 1", count, err)
	}
	for _, id := range []string{"boundary", "recent", "queued", "running"} {
		if _, found := store.Get(id); !found {
			t.Fatalf("retention deleted %q", id)
		}
	}
}

func TestPostgresPersistenceMigrationChecking(t *testing.T) {
	database := openPersistenceIntegrationDB(t, false)
	ctx := context.Background()
	if err := schema.Check(ctx, database); err == nil {
		t.Fatal("Check accepted an unmigrated schema")
	}
	var ledgerExists bool
	if err := database.QueryRow(`SELECT to_regclass('schema_migrations') IS NOT NULL`).Scan(&ledgerExists); err != nil || ledgerExists {
		t.Fatalf("Check created migration ledger: %v, %v", ledgerExists, err)
	}
	// Upgrade a pre-lease baseline, preserving legacy ownership and recovery.
	if _, err := database.Exec(`CREATE TABLE submissions (
		id TEXT PRIMARY KEY, status TEXT NOT NULL, job JSONB NOT NULL, result JSONB,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
		INSERT INTO submissions (id, status, job) VALUES ('legacy', 'running', '{}')`); err != nil {
		t.Fatal(err)
	}
	if err := schema.Apply(ctx, database); err != nil {
		t.Fatal(err)
	}
	if err := schema.Check(ctx, database); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresSubmissionStore(database)
	legacy, found := store.Get("legacy")
	if !found || legacy.OwnerService != "" || legacy.OwnerUser != "" || legacy.AttemptCount != 1 || legacy.LeaseExpiresAt == nil {
		t.Fatalf("migrated legacy = %+v, %v", legacy, found)
	}
	if _, found, err := store.GetOwned(ctx, "legacy", "service", "user"); found || err != nil {
		t.Fatalf("legacy scoped read = %v, %v", found, err)
	}
	if _, err := database.Exec(`DELETE FROM schema_migrations WHERE version = '003_submission_ownership.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := schema.Check(ctx, database); err == nil || !strings.Contains(err.Error(), "003_submission_ownership.sql") {
		t.Fatalf("missing migration error = %v", err)
	}
	if err := schema.Apply(ctx, database); err != nil {
		t.Fatal(err)
	}
	// Read-only transactions cannot execute DDL. Check must still succeed.
	config, err := pgx.ParseConfig(os.Getenv("YEXJUDGE_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal("invalid integration database configuration")
	}
	var searchPath string
	if err := database.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&searchPath); err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = searchPath
	config.RuntimeParams["default_transaction_read_only"] = "on"
	readOnly := stdlib.OpenDB(*config)
	defer readOnly.Close()
	if err := schema.Check(ctx, readOnly); err != nil {
		t.Fatalf("read-only Check: %v", err)
	}
	// Migration ledger already applied: concurrent runs must remain idempotent.
	other := secondPersistenceIntegrationPool(t, database)
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for _, pool := range []*sql.DB{database, other} {
		wait.Add(1)
		go func(pool *sql.DB) { defer wait.Done(); results <- schema.Apply(ctx, pool) }(pool)
	}
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestPostgresPersistenceRespectsCancellation(t *testing.T) {
	database := openPersistenceIntegrationDB(t, true)
	store := NewPostgresSubmissionStore(database)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.SaveAdmitted(ctx, ownedIntegrationSubmission("cancelled", "service", "user"), AdmissionLimits{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled admission = %v", err)
	}
	if _, _, err := store.GetOwned(ctx, "id", "service", "user"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read = %v", err)
	}
	if _, err := store.DeleteOwned(ctx, "id", "service", "user"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled delete = %v", err)
	}
	if _, err := store.DeleteExpired(ctx, time.Now(), 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled retention = %v", err)
	}
	if err := schema.Check(ctx, database); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled schema check = %v", err)
	}
}
