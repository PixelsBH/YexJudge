package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestLoadMigrationConfig(t *testing.T) {
	const url = "postgres://migration:secret@example.invalid/judge?sslmode=verify-full"
	for _, test := range []struct {
		name      string
		env       map[string]string
		file      string
		fileError error
		wantError string
	}{
		{name: "environment", env: map[string]string{"DATABASE_URL": url}},
		{name: "file", env: map[string]string{"DATABASE_URL_FILE": "/run/secrets/migration"}, file: " \n" + url + "\n"},
		{name: "production", env: map[string]string{"DATABASE_URL": url, "APP_ENV": "production"}},
		{name: "keyword DSN", env: map[string]string{"DATABASE_URL": "host=example.invalid user=migration password='secret' dbname=judge sslmode=verify-full", "APP_ENV": "production"}},
		{name: "missing", wantError: "is required"},
		{name: "ambiguous", env: map[string]string{"DATABASE_URL": url, "DATABASE_URL_FILE": "secret-path"}, wantError: "set only one"},
		{name: "empty file", env: map[string]string{"DATABASE_URL_FILE": "secret-path"}, file: " \n", wantError: "is required"},
		{name: "unreadable file", env: map[string]string{"DATABASE_URL_FILE": "secret-path"}, fileError: errors.New("secret error"), wantError: "cannot read"},
		{name: "large file", env: map[string]string{"DATABASE_URL_FILE": "secret-path"}, file: strings.Repeat("x", maxSecretBytes+1), wantError: "too large"},
		{name: "invalid URL", env: map[string]string{"DATABASE_URL": "postgres://migration:secret@example.invalid/judge?sslmode=invalid"}, wantError: "invalid migration database configuration"},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, err := loadMigrationConfig(func(key string) string { return test.env[key] }, func(string) ([]byte, error) { return []byte(test.file), test.fileError })
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("error = %v, want %q", err, test.wantError)
				}
				for _, secret := range []string{"secret-path", "secret error", "migration:secret"} {
					if strings.Contains(err.Error(), secret) {
						t.Fatalf("error exposed sensitive configuration: %v", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("loadMigrationConfig() error = %v", err)
			}
			if config.ConnectTimeout != connectionTimeout || config.RuntimeParams["statement_timeout"] != "5000" || config.RuntimeParams["lock_timeout"] != "5000" {
				t.Fatal("database operations are not bounded")
			}
			if config.User != "migration" || config.Password != "secret" {
				t.Fatal("migration credential was not preserved")
			}
		})
	}
}

func TestProductionMigrationRequiresVerifiedTLS(t *testing.T) {
	for _, mode := range []string{"disable", "allow", "prefer", "require", "verify-ca"} {
		t.Run(mode, func(t *testing.T) {
			_, err := loadMigrationConfig(func(key string) string {
				if key == "APP_ENV" {
					return "production"
				}
				if key == "DATABASE_URL" {
					return "postgres://user:secret@example.invalid/judge?sslmode=" + mode
				}
				return ""
			}, readSecretFile)
			if err == nil || !strings.Contains(err.Error(), "verify-full") {
				t.Fatalf("mode %q error = %v, want verify-full requirement", mode, err)
			}
		})
	}
	for _, url := range []string{
		"postgres://user:secret@/judge?host=/tmp&sslmode=verify-full",
		"host=example.invalid,/tmp user=migration password=secret dbname=judge sslmode=verify-full",
	} {
		_, err := loadMigrationConfig(func(key string) string {
			if key == "APP_ENV" {
				return "production"
			}
			if key == "DATABASE_URL" {
				return url
			}
			return ""
		}, readSecretFile)
		if err == nil {
			t.Fatal("production configuration permitted a non-TLS host")
		}
	}
}

func TestMigrationConnectionErrorsAreRedacted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	err := run(ctx, func(key string) string {
		if key == "DATABASE_URL" {
			return "postgres://private-user:private-password@127.0.0.1:1/private-database?sslmode=disable"
		}
		return ""
	}, &output)
	if err == nil || err.Error() != "migration database connection failed" {
		t.Fatalf("run() error = %v, want redacted connection failure", err)
	}
	if output.Len() != 0 {
		t.Fatal("failed migration reported success")
	}
}
