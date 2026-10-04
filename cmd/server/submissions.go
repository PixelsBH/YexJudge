package main

import (
	"errors"
	"net/http"
	"strings"

	"yexjudge/internal/judge"
)

func submissionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "GET, DELETE")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/submissions/")
	if !validIdentity(id) {
		writeAPIError(w, http.StatusBadRequest, "invalid_submission_id", "a valid submission id is required")
		return
	}
	if r.Method == http.MethodDelete {
		identity, ok := requestPrincipal(r.Context())
		store, available := submissionStore.(admittedSubmissionStore)
		if !ok || !available {
			writeAPIError(w, http.StatusServiceUnavailable, "persistence_unavailable", "submission persistence is unavailable")
			return
		}
		deleted, err := store.DeleteOwned(r.Context(), id, identity.service, identity.user)
		if errors.Is(err, judge.ErrSubmissionActive) {
			writeAPIError(w, http.StatusConflict, "submission_active", "active submissions cannot be deleted")
			return
		}
		if err != nil {
			writeAPIError(w, http.StatusServiceUnavailable, "persistence_unavailable", "submission persistence is unavailable")
			return
		}
		if !deleted {
			writeAPIError(w, http.StatusNotFound, "not_found", "submission not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	submission, found, err := ownedSubmission(r.Context(), id)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "persistence_unavailable", "submission persistence is unavailable")
		return
	}
	if !found {
		writeAPIError(w, http.StatusNotFound, "not_found", "submission not found")
		return
	}
	writeJSON(w, http.StatusOK, judge.SubmissionResponse{ID: submission.ID, Status: submission.Status, Result: responseResult(r.Context(), submission.Result)})
}
