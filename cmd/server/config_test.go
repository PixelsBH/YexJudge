package main

import (
	"os"
	"strings"
	"testing"
)

func TestValidateConfigDefaults(t *testing.T) {
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.workerCount != defaultWorkerCount || cfg.sandboxPoolSize != defaultSandboxPoolSize || cfg.compileSlots != defaultCompileSlots {
		t.Fatalf("capacity defaults = workers %d, sandboxes %d, compile slots %d", cfg.workerCount, cfg.sandboxPoolSize, cfg.compileSlots)
	}
}

func TestLoadConfigRejectsInvalidCapacity(t *testing.T) {
	t.Setenv("WORKER_COUNT", "0")
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "WORKER_COUNT") {
		t.Fatalf("loadConfig() error = %v, want WORKER_COUNT validation", err)
	}
}

func TestValidateConfigRejectsUnsafeMemoryReservation(t *testing.T) {
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	cfg.sandboxPoolSize = maxSandboxPoolSize
	cfg.compileSlots = maxCompileSlots
	if err := validateConfig(cfg); err == nil || !strings.Contains(err.Error(), "capacity budget") {
		t.Fatalf("validateConfig() error = %v, want capacity-budget validation", err)
	}
}

func TestLoadConfigRejectsInvalidInteger(t *testing.T) {
	for _, key := range []string{"WORKER_COUNT", "SANDBOX_POOL_SIZE", "COMPILE_SLOTS"} {
		t.Run(key, func(t *testing.T) {
			_ = os.Setenv(key, "not-a-number")
			t.Cleanup(func() { _ = os.Unsetenv(key) })
			if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("loadConfig() error = %v, want %s validation", err, key)
			}
		})
	}
}
