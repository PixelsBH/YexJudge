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
)

//go:embed migrations/*.sql
var schemaFiles embed.FS

const migrationLockKey int64 = 824739105

// Apply ensures the baseline schema and every numbered migration are applied.
// Bootstrap and migrations share one transaction-scoped advisory lock so
// concurrent server processes cannot race while creating migration metadata.
func Apply(ctx context.Context, database *sql.DB) error {
	if database == nil {
		return fmt.Errorf("database is nil")
	}

	migrations, err := fs.Glob(schemaFiles, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(migrations)

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schema migrations: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockKey); err != nil {
		return fmt.Errorf("lock schema migrations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		return fmt.Errorf("create schema migrations table: %w", err)
	}

	for _, migrationPath := range migrations {
		version := path.Base(migrationPath)
		contents, err := fs.ReadFile(schemaFiles, migrationPath)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", version, err)
		}
		if strings.TrimSpace(string(contents)) == "" {
			return fmt.Errorf("migration %s is empty", version)
		}

		var applied bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", version, err)
		}
		if applied {
			continue
		}

		if _, err := tx.ExecContext(ctx, string(contents)); err != nil {
			return fmt.Errorf("apply migration %s: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			return fmt.Errorf("record migration %s: %w", version, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema migrations: %w", err)
	}
	return nil
}
