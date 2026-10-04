package judge

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	schema "yexjudge/db"
)

func TestPostgresPersistenceConcurrentMigrations(t *testing.T) {
	database := openPersistenceIntegrationDB(t, false)
	other := secondPersistenceIntegrationPool(t, database)
	for round := 0; round < 2; round++ {
		start := make(chan struct{})
		results := make(chan error, 2)
		var wait sync.WaitGroup
		for _, pool := range []*sql.DB{database, other} {
			wait.Add(1)
			go func(pool *sql.DB) {
				defer wait.Done()
				<-start
				results <- schema.Apply(context.Background(), pool)
			}(pool)
		}
		close(start)
		wait.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := schema.Check(context.Background(), database); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPostgresPersistenceDatabaseTimeouts(t *testing.T) {
	database := openPersistenceIntegrationDB(t, true)
	store := NewPostgresSubmissionStore(database)
	tx, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`LOCK TABLE submissions IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	operations := []struct {
		name string
		run  func() error
	}{
		{"Counts", func() error { _, err := store.Counts(); return err }},
		{"Update", func() error { return store.Update(Submission{ID: "blocked", Status: SubmissionFinished}) }},
		{"Get", func() error {
			if _, found := store.Get("blocked"); found {
				return errors.New("Get unexpectedly found a row")
			}
			return nil
		}},
		{"Save", func() error { return store.Save(integrationSubmission("blocked")) }},
	}
	type outcome struct {
		name    string
		err     error
		elapsed time.Duration
	}
	results := make(chan outcome, len(operations))
	for _, operation := range operations {
		go func(name string, run func() error) {
			start := time.Now()
			results <- outcome{name, run(), time.Since(start)}
		}(operation.name, operation.run)
	}
	for range operations {
		result := <-results
		if result.elapsed > postgresOperationTimeout+3*time.Second {
			t.Errorf("%s exceeded its timeout: %s", result.name, result.elapsed)
		}
		if result.name != "Get" && !errors.Is(result.err, context.DeadlineExceeded) {
			t.Errorf("%s error = %v, want context deadline exceeded", result.name, result.err)
		}
		if result.name == "Get" && result.err != nil {
			t.Error(result.err)
		}
	}
}
