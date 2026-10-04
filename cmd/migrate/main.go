package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	schema "yexjudge/db"
)

const (
	migrationTimeout  = time.Minute
	connectionTimeout = 5 * time.Second
	maxSecretBytes    = 64 * 1024
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// This process intentionally reads its own DATABASE_URL[_FILE]. Deploy it with
// a migration credential, not the production application's restricted role.
func run(ctx context.Context, getenv func(string) string, output io.Writer) error {
	config, err := loadMigrationConfig(getenv, readSecretFile)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, migrationTimeout)
	defer cancel()
	database := stdlib.OpenDB(*config)
	defer database.Close()
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	database.SetConnMaxLifetime(migrationTimeout)
	database.SetConnMaxIdleTime(connectionTimeout)

	pingCtx, cancelPing := context.WithTimeout(ctx, connectionTimeout)
	err = database.PingContext(pingCtx)
	cancelPing()
	if err != nil {
		// Driver errors can include connection strings, usernames, file paths,
		// or server-supplied secrets. Never print or wrap them in this command.
		return fmt.Errorf("migration database connection failed")
	}
	if err := schema.Apply(ctx, database); err != nil {
		return fmt.Errorf("database migration failed")
	}
	if err := schema.Check(ctx, database); err != nil {
		return fmt.Errorf("migration verification failed")
	}
	_, err = fmt.Fprintln(output, "database migrations applied")
	return err
}

func loadMigrationConfig(getenv func(string) string, readFile func(string) ([]byte, error)) (*pgx.ConnConfig, error) {
	connectionString := strings.TrimSpace(getenv("DATABASE_URL"))
	secretPath := strings.TrimSpace(getenv("DATABASE_URL_FILE"))
	if connectionString != "" && secretPath != "" {
		return nil, fmt.Errorf("set only one of DATABASE_URL or DATABASE_URL_FILE")
	}
	if secretPath != "" {
		contents, err := readFile(secretPath)
		if err != nil {
			return nil, fmt.Errorf("cannot read DATABASE_URL_FILE")
		}
		if len(contents) > maxSecretBytes {
			return nil, fmt.Errorf("DATABASE_URL_FILE is too large")
		}
		connectionString = strings.TrimSpace(string(contents))
	}
	if connectionString == "" {
		return nil, fmt.Errorf("DATABASE_URL or DATABASE_URL_FILE is required")
	}
	config, err := pgx.ParseConfig(connectionString)
	if err != nil {
		return nil, fmt.Errorf("invalid migration database configuration")
	}
	if strings.EqualFold(strings.TrimSpace(getenv("APP_ENV")), "production") {
		// Validate the actual pgx configuration (including DSNs, PGSSLMODE,
		// and every multi-host fallback), not just a URL query parameter.
		if !verifiedTLS(config.TLSConfig) {
			return nil, fmt.Errorf("production migration database requires sslmode=verify-full")
		}
		for _, fallback := range config.Fallbacks {
			if !verifiedTLS(fallback.TLSConfig) {
				return nil, fmt.Errorf("production migration database requires sslmode=verify-full for every host")
			}
		}
	}
	config.ConnectTimeout = connectionTimeout
	config.RuntimeParams["statement_timeout"] = "5000"
	config.RuntimeParams["lock_timeout"] = "5000"
	return config, nil
}

func verifiedTLS(config *tls.Config) bool {
	return config != nil && !config.InsecureSkipVerify && config.ServerName != ""
}

func readSecretFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, maxSecretBytes+1))
}
