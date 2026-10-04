package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var schemaFiles embed.FS

const migrationLockKey int64 = 824739105

// Apply runs every numbered migration, including the baseline, under one
// transaction lock. Use a separate migration credential; production application
// startup should call Check instead, without requiring any DDL privileges.
func Apply(ctx context.Context, database *sql.DB) error {
	if database == nil {
		return fmt.Errorf("database is nil")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	migrations, err := numberedMigrations()
	if err != nil {
		return err
	}
	tx, err := database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin migrations: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockKey); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		return fmt.Errorf("create schema migrations table: %w", err)
	}
	for _, migration := range migrations {
		if err := applyMigration(ctx, tx, migration); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

// Check verifies that every embedded numbered migration was applied. It performs
// only SELECTs and is safe for a production application credential without DDL
// permissions. Unknown newer versions do not prevent a rolling deployment.
func Check(ctx context.Context, database *sql.DB) error {
	if database == nil {
		return fmt.Errorf("database is nil")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	migrations, err := numberedMigrations()
	if err != nil {
		return err
	}
	rows, err := database.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read applied migrations (run cmd/migrate first): %w", err)
	}
	defer rows.Close()
	applied := make(map[string]bool, len(migrations))
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return fmt.Errorf("read migration version: %w", err)
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}
	var missing []string
	for _, migration := range migrations {
		version := path.Base(migration)
		if !applied[version] {
			missing = append(missing, version)
		}
	}
	if len(missing) != 0 {
		return fmt.Errorf("missing migrations: %s (run cmd/migrate first)", strings.Join(missing, ", "))
	}
	return nil
}

func numberedMigrations() ([]string, error) {
	migrations, err := fs.Glob(schemaFiles, "migrations/[0-9]*.sql")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(migrations)
	return migrations, nil
}

func applyMigration(ctx context.Context, tx *sql.Tx, migrationPath string) error {
	version := path.Base(migrationPath)
	var applied bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&applied); err != nil {
		return fmt.Errorf("check migration %s: %w", version, err)
	}
	if applied {
		return nil
	}
	contents, err := fs.ReadFile(schemaFiles, migrationPath)
	if err != nil {
		return fmt.Errorf("read migration %s: %w", version, err)
	}
	if strings.TrimSpace(string(contents)) == "" {
		return fmt.Errorf("migration %s is empty", version)
	}
	if _, err := tx.ExecContext(ctx, string(contents)); err != nil {
		return fmt.Errorf("apply migration %s: %w", version, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
		return fmt.Errorf("record migration %s: %w", version, err)
	}
	return nil
}
