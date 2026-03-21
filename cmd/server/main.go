package main

import (
	"encoding/json"
	"log"
	"net/http"
	"yexjudge/internal/judge"
	"yexjudge/internal/runner"
)

func judgeHandler(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	job, ok := decodeJudgeJob(w, r)

	if !ok {
		return
	}

	cmdRunner := &runner.DockerRunner{}
	service := judge.NewService(cmdRunner)

	result, err := service.Judge(r.Context(), job)
	if err != nil {
		http.Error(w, "Execution failed", http.StatusInternalServerError)
		log.Println("judge service error:", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func main() {
	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/judge", judgeHandler)

	log.Println("YexJudge server running on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
