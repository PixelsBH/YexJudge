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
	defaultQueueSize       = 100
	defaultSandboxPoolSize = 4
	defaultQueuePollMs     = 500
)

type config struct {
	port            string
	workerCount     int
	queueSize       int
	sandboxPoolSize int
	databaseURL     string
	queuePoll       time.Duration
}

func loadConfig() config {
	return config{
		port:            getEnv("PORT", defaultPort),
		workerCount:     getEnvInt("WORKER_COUNT", defaultWorkerCount),
		queueSize:       getEnvInt("QUEUE_SIZE", defaultQueueSize),
		sandboxPoolSize: getEnvInt("SANDBOX_POOL_SIZE", defaultSandboxPoolSize),
		databaseURL:     getEnv("DATABASE_URL", ""),
		queuePoll:       time.Duration(getEnvInt("QUEUE_POLL_INTERVAL_MS", defaultQueuePollMs)) * time.Millisecond,
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
