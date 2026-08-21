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

var runAdmission = make(chan struct{}, defaultRunConcurrency)

func runHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	defer r.Body.Close()

	var request runRequest
	if !decodeJSONBody(w, r, &request) {
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
		writeAPIError(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}

	select {
	case runAdmission <- struct{}{}:
		defer func() { <-runAdmission }()
	default:
		writeAPIError(w, http.StatusTooManyRequests, "run_capacity_exhausted", "direct run capacity is currently exhausted")
		return
	}

	result, err := judgeService.RunCode(r.Context(), job)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal error")
		log.Println("failed to run code:", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		log.Println("failed to encode run response:", err)
	}
}
