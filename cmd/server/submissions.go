package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"yexjudge/internal/judge"
)

func submissionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/submissions/")
	if id == "" || id == r.URL.Path {
		writeAPIError(w, http.StatusBadRequest, "invalid_submission_id", "submission id is required")
		return
	}

	submission, ok, err := getSubmission(r.Context(), id)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "persistence_unavailable", "submission storage is temporarily unavailable")
		return
	}
	if !ok {
		writeAPIError(w, http.StatusNotFound, "not_found", "submission not found")
		return
	}

	response := judge.SubmissionResponse{
		ID:     submission.ID,
		Status: submission.Status,
		Result: submission.Result,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
