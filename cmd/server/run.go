package main

import (
	"encoding/json"
	"log"
	"net/http"

	"yexjudge/internal/judge"
)

type runRequest struct {
	Language   string       `json:"language"`
	SourceCode string       `json:"sourceCode"`
	Limits     judge.Limits `json:"limits"`
}

func runHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var request runRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "Invalid request payload", http.StatusBadRequest)
		return
	}

	job := judge.Job{
		Language:   request.Language,
		SourceCode: request.SourceCode,
		Limits:     request.Limits,
		TestCases: []judge.TestCase{{
			ID: 1,
		}},
	}
	if err := judge.ValidateJob(job); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	result, err := judgeService.RunCode(r.Context(), job)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		log.Println("failed to run code:", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		log.Println("failed to encode run response:", err)
	}
}
