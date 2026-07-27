package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"yexjudge/internal/judge"
)

const submitPollInterval = 100 * time.Millisecond

// submitHandler provides a synchronous convenience API over the normal async flow.
func submitHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()

	job, ok := decodeJudgeJob(w, r)
	if !ok {
		return
	}

	if err := judge.ValidateJob(job); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	submission, err := createAndQueueSubmission(job)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		log.Println("failed to create submission:", err)
		return
	}

	w.Header().Set("Location", "/submissions/"+submission.ID)
	result, completed := waitForSubmission(r.Context(), submission.ID, submitTimeout)
	if !completed {
		writeJSON(w, http.StatusAccepted, judge.SubmissionAcceptedResponse{
			SubmissionID: submission.ID,
			Status:       result.Status,
		})
		return
	}

	writeJSON(w, http.StatusOK, judge.SubmissionResponse{
		ID:     result.ID,
		Status: result.Status,
		Result: result.Result,
	})
}

func waitForSubmission(ctx context.Context, id string, timeout time.Duration) (judge.Submission, bool) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	ticker := time.NewTicker(submitPollInterval)
	defer ticker.Stop()

	for {
		submission, ok := submissionStore.Get(id)
		if ok && (submission.Status == judge.SubmissionFinished || submission.Status == judge.SubmissionFailed) {
			return submission, true
		}

		select {
		case <-ctx.Done():
			return submission, false
		case <-deadline.C:
			if latest, ok := submissionStore.Get(id); ok {
				return latest, false
			}
			return submission, false
		case <-ticker.C:
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Println("failed to encode JSON response:", err)
	}
}
