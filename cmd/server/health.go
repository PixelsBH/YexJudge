package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

type HealthResponse struct {
	Status string `json:"status"`
}

type ReadinessResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

var serverDraining atomic.Bool

func healthHandler(w http.ResponseWriter, r *http.Request) {
	resp := HealthResponse{Status: "ok"}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Println("failed to encode health response:", err)
	}
}

func readinessHandler(w http.ResponseWriter, r *http.Request) {
	checks := make(map[string]string, 3)
	ready := true

	if serverDraining.Load() {
		checks["server"] = "draining"
		ready = false
	} else {
		checks["server"] = "ready"
	}

	if provider, ok := submissionStore.(interface{ Ready(context.Context) error }); ok {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		err := provider.Ready(ctx)
		cancel()
		if err != nil {
			checks["postgres"] = "unavailable"
			ready = false
		} else {
			checks["postgres"] = "ready"
		}
	} else {
		checks["postgres"] = "unconfigured"
		ready = false
	}

	if runtimePool == nil || !runtimePool.Ready() {
		checks["runtime"] = "unavailable"
		ready = false
	} else {
		checks["runtime"] = "ready"
	}

	response := ReadinessResponse{Status: "ready", Checks: checks}
	statusCode := http.StatusOK
	if !ready {
		response.Status = "not_ready"
		statusCode = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Println("failed to encode readiness response:", err)
	}
}
