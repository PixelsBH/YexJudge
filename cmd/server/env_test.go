package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvFileLoadsValuesWithoutOverridingProcessEnvironment(t *testing.T) {
	t.Setenv("YEXJUDGE_TEST_PRESET", "from-process")
	for _, key := range []string{"YEXJUDGE_TEST_URL", "YEXJUDGE_TEST_QUOTED", "YEXJUDGE_TEST_EMPTY"} {
		_ = os.Unsetenv(key)
		t.Cleanup(func() { _ = os.Unsetenv(key) })
	}

	path := filepath.Join(t.TempDir(), ".env")
	content := "# comment\nexport YEXJUDGE_TEST_URL=postgres://localhost/yexjudge\nYEXJUDGE_TEST_QUOTED=\"quoted value\"\nYEXJUDGE_TEST_EMPTY=\nYEXJUDGE_TEST_PRESET=from-file\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	if err := loadEnvFile(path); err != nil {
		t.Fatalf("loadEnvFile() error = %v", err)
	}
	if got := os.Getenv("YEXJUDGE_TEST_URL"); got != "postgres://localhost/yexjudge" {
		t.Fatalf("loaded URL = %q, want URL from file", got)
	}
	if got := os.Getenv("YEXJUDGE_TEST_QUOTED"); got != "quoted value" {
		t.Fatalf("loaded quoted value = %q, want unquoted value", got)
	}
	if got := os.Getenv("YEXJUDGE_TEST_EMPTY"); got != "" {
		t.Fatalf("loaded empty value = %q, want empty value", got)
	}
	if got := os.Getenv("YEXJUDGE_TEST_PRESET"); got != "from-process" {
		t.Fatalf("preset value = %q, want process environment to win", got)
	}
}

func TestLoadEnvFileRejectsMalformedEntries(t *testing.T) {
	for _, content := range []string{
		"not-an-assignment\n",
		"1INVALID=value\n",
		"YEXJUDGE_TEST_UNTERMINATED=\"value\n",
	} {
		path := filepath.Join(t.TempDir(), ".env")
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if err := loadEnvFile(path); err == nil {
			t.Fatalf("loadEnvFile(%q) succeeded, want malformed-entry error", content)
		}
	}
}

func TestLoadEnvFileAllowsMissingFile(t *testing.T) {
	if err := loadEnvFile(filepath.Join(t.TempDir(), ".env-missing")); err != nil {
		t.Fatalf("loadEnvFile() missing file error = %v, want nil", err)
	}
}
