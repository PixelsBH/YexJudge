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
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	defer r.Body.Close()

	job, ok := decodeJudgeJob(w, r)
	if !ok {
		return
	}

	if err := judge.ValidateJob(job); err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), submitTimeout)
	defer cancel()
	submission, err := createAndQueueSubmission(ctx, job)
	if err != nil {
		writeSubmissionAdmissionError(w, err)
		log.Println("failed to create submission:", err)
		return
	}

	w.Header().Set("Location", "/submissions/"+submission.ID)
	result, completed := waitForSubmission(ctx, submission.ID, submitTimeout)
	if result.ID == "" {
		result = submission
	}
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
		if ctx.Err() != nil {
			return judge.Submission{}, false
		}
		submission, ok, err := getSubmission(ctx, id)
		if err != nil {
			return judge.Submission{}, false
		}
		if ok && (submission.Status == judge.SubmissionFinished || submission.Status == judge.SubmissionFailed) {
			return submission, true
		}

		select {
		case <-ctx.Done():
			return submission, false
		case <-deadline.C:
			latest, ok, err := getSubmission(ctx, id)
			if err == nil && ok {
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
