package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"yexjudge/internal/judge"
)

type admittedSubmissionStore interface {
	SaveAdmitted(context.Context, judge.Submission, judge.AdmissionLimits) error
	GetOwned(context.Context, string, string, string) (judge.Submission, bool, error)
	DeleteOwned(context.Context, string, string, string) (bool, error)
	DeleteExpired(context.Context, time.Time, int) (int64, error)
}

func createAndQueueSubmission(r *http.Request, job judge.Job) (judge.Submission, error) {
	identity, ok := requestPrincipal(r.Context())
	if !ok || identity.service == "" || identity.user == "" {
		return judge.Submission{}, fmt.Errorf("missing authenticated submission owner")
	}
	store, ok := submissionStore.(admittedSubmissionStore)
	if !ok || runtimeSecurity == nil {
		return judge.Submission{}, fmt.Errorf("secure admission is unavailable")
	}
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return judge.Submission{}, fmt.Errorf("submission ID generation failed")
	}
	submission := judge.Submission{
		ID: hex.EncodeToString(bytes[:]), Job: job, Status: judge.SubmissionQueued,
		OwnerService: identity.service, OwnerUser: identity.user,
	}
	if err := store.SaveAdmitted(r.Context(), submission, runtimeSecurity.cfg.admission); err != nil {
		return judge.Submission{}, err
	}
	// PostgreSQL admission is itself durable queue insertion. Enqueue remains
	// the existing queue validation/wakeup contract; it never creates a row.
	if err := submissionQueue.Enqueue(submission.ID); err != nil {
		submission.Status = judge.SubmissionFailed
		if updateErr := submissionStore.Update(submission); updateErr != nil {
			return judge.Submission{}, fmt.Errorf("submission enqueue and failure update failed")
		}
		return judge.Submission{}, fmt.Errorf("submission enqueue failed")
	}
	slog.Info("submission queued", "submission_id", submission.ID, "language", job.Language, "status", submission.Status)
	return submission, nil
}

func writeAdmissionError(w http.ResponseWriter, err error) {
	if errors.Is(err, judge.ErrQueueFull) {
		if runtimeSecurity != nil {
			runtimeSecurity.capacityRejected.Add(1)
		}
		w.Header().Set("Retry-After", "5")
		writeAPIError(w, http.StatusServiceUnavailable, "queue_full", "submission queue is full")
		return
	}
	if errors.Is(err, judge.ErrServiceLimit) || errors.Is(err, judge.ErrUserLimit) {
		if runtimeSecurity != nil {
			runtimeSecurity.capacityRejected.Add(1)
		}
		w.Header().Set("Retry-After", "5")
		writeAPIError(w, http.StatusTooManyRequests, "active_submission_limit", "active submission limit reached")
		return
	}
	// PostgreSQL errors may include credentials, SQL values, or protected data.
	slog.Error("submission admission failed", "category", "persistence_or_queue")
	writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal error")
}

func ownedSubmission(ctx context.Context, id string) (judge.Submission, bool, error) {
	identity, ok := requestPrincipal(ctx)
	if !ok {
		return judge.Submission{}, false, fmt.Errorf("missing authenticated submission owner")
	}
	store, ok := submissionStore.(admittedSubmissionStore)
	if !ok {
		return judge.Submission{}, false, fmt.Errorf("scoped persistence is unavailable")
	}
	return store.GetOwned(ctx, id, identity.service, identity.user)
}

// Defaults to withholding protected inputs/expected values and raw compiler or
// runtime diagnostics even if invoked without middleware. Never mutate storage.
func responseResult(ctx context.Context, result *judge.Result) *judge.Result {
	if result == nil {
		return nil
	}
	copy := *result
	identity, ok := requestPrincipal(ctx)
	if ok && identity.exposeDetails {
		return &copy
	}
	copy.FailedTestCase = nil
	copy.ErrorMessage = ""
	switch copy.Status {
	case judge.CompilationError:
		copy.ErrorMessage = "compilation failed"
	case judge.RuntimeError:
		copy.ErrorMessage = "execution failed"
	case judge.InfrastructureError:
		copy.ErrorMessage = "judge infrastructure is unavailable"
	case judge.ValidationError:
		copy.ErrorMessage = "submission validation failed"
	case judge.OutputLimitExceeded:
		copy.ErrorMessage = "execution output exceeded the allowed limit"
	}
	return &copy
}

func startRetention(ctx context.Context, days int) {
	store, ok := submissionStore.(admittedSubmissionStore)
	if !ok {
		return
	}
	go func() {
		cleanup := func() {
			// Bound each pass, including on startup, so retention cannot monopolize
			// database connections after prolonged downtime.
			for batch := 0; batch < 10 && ctx.Err() == nil; batch++ {
				deleted, err := store.DeleteExpired(ctx, time.Now().Add(-time.Duration(days)*24*time.Hour), 1000)
				if err != nil {
					if ctx.Err() == nil {
						if runtimeSecurity != nil {
							runtimeSecurity.retentionFailures.Add(1)
						}
						slog.Error("submission retention failed", "category", "persistence")
					}
					return
				}
				if runtimeSecurity != nil {
					runtimeSecurity.retentionDeleted.Add(deleted)
				}
				if deleted > 0 {
					slog.Info("expired terminal submissions deleted", "count", deleted)
				}
				if deleted < 1000 {
					return
				}
			}
		}
		cleanup()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanup()
			}
		}
	}()
}
