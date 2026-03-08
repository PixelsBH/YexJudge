package main

import (
	"encoding/json"
	"log"
	"net/http"
	"yexjudge/internal/judge"
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

	var job judge.Job
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	err := json.NewDecoder(r.Body).Decode(&job)
	if err != nil {
		http.Error(w, "Invalid request payload", http.StatusBadRequest)
		log.Println("failed to decode job:", err)
		return
	}
	result := judge.Result{
		Status: judge.Accepted,
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
