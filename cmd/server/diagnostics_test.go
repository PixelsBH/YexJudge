package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"yexjudge/internal/judge"
	"yexjudge/internal/observability"
)

type diagnosticsStore struct{}

func (diagnosticsStore) Save(judge.Submission) error         { return nil }
func (diagnosticsStore) Get(string) (judge.Submission, bool) { return judge.Submission{}, false }
func (diagnosticsStore) Update(judge.Submission) error       { return nil }
func (diagnosticsStore) Counts() (judge.SubmissionCounts, error) {
	return judge.SubmissionCounts{Queued: 2, Running: 1, Failed: 3}, nil
}

func TestDiagnosticsHandlerReturnsOperationalSnapshot(t *testing.T) {
	previousStore := submissionStore
	previousMetrics := runtimeMetrics
	previousPool := runtimePool
	defer func() {
		submissionStore = previousStore
		runtimeMetrics = previousMetrics
		runtimePool = previousPool
	}()

	runtimeMetrics = observability.NewMetrics()
	runtimeMetrics.WorkerStarted()
	runtimeMetrics.ObserveCompile(25 * time.Millisecond)
	submissionStore = diagnosticsStore{}
	runtimePool = nil

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/diagnostics", nil)
	diagnosticsHandler(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var response diagnosticsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode diagnostics response: %v", err)
	}
	if response.Submissions.Failed != 3 || response.Workers.Busy != 1 {
		t.Fatalf("diagnostics response = %+v, want counts and worker busy state", response)
	}
	if response.Timings["compile"].Count != 1 {
		t.Fatalf("compile timing = %+v, want one observation", response.Timings["compile"])
	}
}
