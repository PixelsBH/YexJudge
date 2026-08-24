package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"yexjudge/internal/judge"
	"yexjudge/internal/judge/languages"
	"yexjudge/internal/runner"
)

type readinessStore struct {
	err error
}

func (readinessStore) Save(judge.Submission) error         { return nil }
func (readinessStore) Get(string) (judge.Submission, bool) { return judge.Submission{}, false }
func (readinessStore) Update(judge.Submission) error       { return nil }
func (s readinessStore) Ready(context.Context) error       { return s.err }

type readinessExecutor struct{}

func (readinessExecutor) Compile(context.Context, string, languages.Spec, judge.Limits) (*runner.RunResult, error) {
	return &runner.RunResult{ExitCode: 0}, nil
}
func (readinessExecutor) StartSandbox(context.Context) (*judge.Sandbox, error) {
	return &judge.Sandbox{ContainerName: "readiness"}, nil
}
func (readinessExecutor) ConfigureSandbox(context.Context, *judge.Sandbox, judge.Limits) error {
	return nil
}
func (readinessExecutor) PrepareSandbox(context.Context, *judge.Sandbox, string) error { return nil }
func (readinessExecutor) ResetSandbox(context.Context, *judge.Sandbox) error           { return nil }
func (readinessExecutor) RemoveSandbox(*judge.Sandbox)                                 {}
func (readinessExecutor) RunTestCase(context.Context, *judge.Sandbox, string, languages.Spec) (*runner.RunResult, error) {
	return &runner.RunResult{ExitCode: 0}, nil
}

func TestReadinessHandlerReturnsReadyWhenDependenciesAreAvailable(t *testing.T) {
	previousStore := submissionStore
	previousPool := runtimePool
	previousDraining := serverDraining.Load()
	defer func() {
		submissionStore = previousStore
		runtimePool = previousPool
		serverDraining.Store(previousDraining)
	}()

	submissionStore = readinessStore{}
	runtimePool = judge.NewExecutorSandboxPool(readinessExecutor{}, []*judge.Sandbox{{ContainerName: "ready"}})
	serverDraining.Store(false)

	recorder := httptest.NewRecorder()
	readinessHandler(recorder, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var response ReadinessResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode readiness response: %v", err)
	}
	if response.Status != "ready" || response.Checks["postgres"] != "ready" || response.Checks["runtime"] != "ready" {
		t.Fatalf("readiness response = %+v, want all dependencies ready", response)
	}
}

func TestReadinessHandlerReturnsUnavailableWhenDependencyFails(t *testing.T) {
	previousStore := submissionStore
	previousPool := runtimePool
	previousDraining := serverDraining.Load()
	defer func() {
		submissionStore = previousStore
		runtimePool = previousPool
		serverDraining.Store(previousDraining)
	}()

	submissionStore = readinessStore{err: errors.New("database unavailable")}
	runtimePool = nil
	serverDraining.Store(false)

	recorder := httptest.NewRecorder()
	readinessHandler(recorder, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", recorder.Code, recorder.Body.String())
	}
	var response ReadinessResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode readiness response: %v", err)
	}
	if response.Status != "not_ready" || response.Checks["postgres"] != "unavailable" || response.Checks["runtime"] != "unavailable" {
		t.Fatalf("readiness response = %+v, want unavailable dependencies", response)
	}
}
