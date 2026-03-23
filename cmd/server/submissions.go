package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"yexjudge/internal/judge"
)

func submissionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/submissions/")
	if id == "" || id == r.URL.Path {
		http.Error(w, "submission id is required", http.StatusBadRequest)
		return
	}

	submission, ok := submissionStore.Get(id)
	if !ok {
		http.Error(w, "submission not found", http.StatusNotFound)
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
