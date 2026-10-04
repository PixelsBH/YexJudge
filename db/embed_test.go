package db

import (
	"context"
	"path"
	"reflect"
	"testing"
)

func TestNumberedMigrations(t *testing.T) {
	migrations, err := numberedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var versions []string
	for _, migration := range migrations {
		versions = append(versions, path.Base(migration))
	}
	want := []string{"001_submissions.sql", "002_queue_leases.sql", "003_submission_ownership.sql"}
	if !reflect.DeepEqual(versions, want) {
		t.Fatalf("migration versions = %v, want %v", versions, want)
	}
}

func TestSchemaRejectsNilDatabase(t *testing.T) {
	if err := Check(context.Background(), nil); err == nil {
		t.Fatal("Check accepted a nil database")
	}
	if err := Apply(context.Background(), nil); err == nil {
		t.Fatal("Apply accepted a nil database")
	}
}
