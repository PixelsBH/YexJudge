package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"
	"yexjudge/internal/judge"
	"yexjudge/internal/runner"
)

type HealthResponse struct {
	Status string `json:"status"`
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	resp := HealthResponse{Status: "ok"}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Println("failed to encode health response:", err)
	}
}

func judgeHandler(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var job judge.Job

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	err := json.NewDecoder(r.Body).Decode(&job)
	if err != nil {
		http.Error(w, "Invalid request payload", http.StatusBadRequest)
		log.Println("failed to decode job:", err)
		return
	}

	runner := &runner.DockerRunner{}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	runRes, err := runner.Run(ctx, "cmd", "echo", "hello")

	//Temporary test
	log.Println("stdout:", runRes.Stdout)
	log.Println("stderr:", runRes.Stderr)
	log.Println("exit:", runRes.ExitCode)
	log.Println("timeout:", runRes.TimedOut)

	if err != nil {
		http.Error(w, "Execution failed", http.StatusInternalServerError)
		log.Println("runner error:", err)
		return
	}

	result := judge.Result{
		Status:       judge.Accepted,
		ErrorMessage: runRes.Stderr,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		log.Println("failed to encode result:", err)
	}
}

func main() {
	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/judge", judgeHandler)

	log.Println("YexJudge server running on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
