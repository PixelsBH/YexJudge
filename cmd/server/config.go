package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"yexjudge/internal/judge"
)

const (
	defaultPort             = "8080"
	defaultWorkerCount      = 4
	defaultSandboxPoolSize  = 4
	defaultCompileSlots     = 2
	defaultQueuePollMs      = 500
	defaultQueueLeaseMs     = 60000
	defaultQueueRecoveryMs  = 1000
	defaultQueueMaxAttempts = 3
	defaultSubmitTimeoutMs  = 10000
	minWorkerCount          = 1
	maxWorkerCount          = 64
	minSandboxPoolSize      = 1
	maxSandboxPoolSize      = 64
	minCompileSlots         = 1
	maxCompileSlots         = 16
	capacityMemoryBudgetMb  = 8192
)

type config struct {
	port             string
	workerCount      int
	sandboxPoolSize  int
	compileSlots     int
	databaseURL      string
	queuePoll        time.Duration
	queueLease       time.Duration
	queueRecovery    time.Duration
	queueMaxAttempts int
	submitTimeout    time.Duration
}

func loadConfig() (config, error) {
	workerCount, err := getEnvIntStrict("WORKER_COUNT", defaultWorkerCount)
	if err != nil {
		return config{}, err
	}
	sandboxPoolSize, err := getEnvIntStrict("SANDBOX_POOL_SIZE", defaultSandboxPoolSize)
	if err != nil {
		return config{}, err
	}
	compileSlots, err := getEnvIntStrict("COMPILE_SLOTS", defaultCompileSlots)
	if err != nil {
		return config{}, err
	}
	cfg := config{
		port:            getEnv("PORT", defaultPort),
		workerCount:     workerCount,
		sandboxPoolSize: sandboxPoolSize,
		compileSlots:    compileSlots,
		databaseURL:     getEnv("DATABASE_URL", ""),
	}
	queuePollMs, err := getEnvIntStrict("QUEUE_POLL_INTERVAL_MS", defaultQueuePollMs)
	if err != nil {
		return config{}, err
	}
	queueLeaseMs, err := getEnvIntStrict("QUEUE_LEASE_MS", defaultQueueLeaseMs)
	if err != nil {
		return config{}, err
	}
	queueRecoveryMs, err := getEnvIntStrict("QUEUE_RECOVERY_INTERVAL_MS", defaultQueueRecoveryMs)
	if err != nil {
		return config{}, err
	}
	queueMaxAttempts, err := getEnvIntStrict("QUEUE_MAX_ATTEMPTS", defaultQueueMaxAttempts)
	if err != nil {
		return config{}, err
	}
	submitTimeoutMs, err := getEnvIntStrict("SUBMIT_TIMEOUT_MS", defaultSubmitTimeoutMs)
	if err != nil {
		return config{}, err
	}
	cfg.queuePoll = time.Duration(queuePollMs) * time.Millisecond
	cfg.queueLease = time.Duration(queueLeaseMs) * time.Millisecond
	cfg.queueRecovery = time.Duration(queueRecoveryMs) * time.Millisecond
	cfg.queueMaxAttempts = queueMaxAttempts
	cfg.submitTimeout = time.Duration(submitTimeoutMs) * time.Millisecond
	return cfg, validateConfig(cfg)
}

func getEnv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func getEnvIntStrict(key string, fallback int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}

	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", key, value)
	}

	return parsed, nil
}

func validateConfig(cfg config) error {
	if cfg.workerCount < minWorkerCount || cfg.workerCount > maxWorkerCount {
		return fmt.Errorf("WORKER_COUNT must be between %d and %d", minWorkerCount, maxWorkerCount)
	}
	if cfg.sandboxPoolSize < minSandboxPoolSize || cfg.sandboxPoolSize > maxSandboxPoolSize {
		return fmt.Errorf("SANDBOX_POOL_SIZE must be between %d and %d", minSandboxPoolSize, maxSandboxPoolSize)
	}
	if cfg.compileSlots < minCompileSlots || cfg.compileSlots > maxCompileSlots {
		return fmt.Errorf("COMPILE_SLOTS must be between %d and %d", minCompileSlots, maxCompileSlots)
	}
	if cfg.queueMaxAttempts < 1 {
		return fmt.Errorf("QUEUE_MAX_ATTEMPTS must be at least 1")
	}
	if cfg.queueLease <= 0 || cfg.queuePoll <= 0 || cfg.queueRecovery <= 0 || cfg.submitTimeout <= 0 {
		return fmt.Errorf("queue and submit durations must be positive")
	}
	reservedMemoryMb := cfg.sandboxPoolSize*judge.MaxMemoryLimitMb + cfg.compileSlots*judge.MinCompileMemoryLimitMb
	if reservedMemoryMb > capacityMemoryBudgetMb {
		return fmt.Errorf("SANDBOX_POOL_SIZE and COMPILE_SLOTS reserve %d MiB, exceeding the %d MiB capacity budget", reservedMemoryMb, capacityMemoryBudgetMb)
	}
	return nil
}
