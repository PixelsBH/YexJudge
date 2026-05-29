package main

import (
	"log"
	"os"
	"strconv"
)

const (
	defaultPort            = "8080"
	defaultWorkerCount     = 4
	defaultQueueSize       = 100
	defaultSandboxPoolSize = 4
)

type config struct {
	port            string
	workerCount     int
	queueSize       int
	sandboxPoolSize int
}

func loadConfig() config {
	return config{
		port:            getEnv("PORT", defaultPort),
		workerCount:     getEnvInt("WORKER_COUNT", defaultWorkerCount),
		queueSize:       getEnvInt("QUEUE_SIZE", defaultQueueSize),
		sandboxPoolSize: getEnvInt("SANDBOX_POOL_SIZE", defaultSandboxPoolSize),
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
