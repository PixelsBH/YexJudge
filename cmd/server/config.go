package main

import (
	"log"
	"os"
	"strconv"
	"time"
)

const (
	defaultPort            = "8080"
	defaultWorkerCount     = 4
	defaultSandboxPoolSize = 4
	defaultQueuePollMs     = 500
	defaultSubmitTimeoutMs = 10000
	defaultRunConcurrency  = 2
)

type config struct {
	port            string
	workerCount     int
	sandboxPoolSize int
	databaseURL     string
	queuePoll       time.Duration
	submitTimeout   time.Duration
	runConcurrency  int
}

func loadConfig() config {
	return config{
		port:            getEnv("PORT", defaultPort),
		workerCount:     getEnvInt("WORKER_COUNT", defaultWorkerCount),
		sandboxPoolSize: getEnvInt("SANDBOX_POOL_SIZE", defaultSandboxPoolSize),
		databaseURL:     getEnv("DATABASE_URL", ""),
		queuePoll:       time.Duration(getEnvInt("QUEUE_POLL_INTERVAL_MS", defaultQueuePollMs)) * time.Millisecond,
		submitTimeout:   time.Duration(getEnvInt("SUBMIT_TIMEOUT_MS", defaultSubmitTimeoutMs)) * time.Millisecond,
		runConcurrency:  getEnvInt("RUN_CONCURRENCY", defaultRunConcurrency),
	}
}

func getEnv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func getEnvInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		log.Printf("invalid %s=%q, using default %d", key, value, fallback)
		return fallback
	}

	return parsed
}
