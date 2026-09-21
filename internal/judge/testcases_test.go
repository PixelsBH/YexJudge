package judge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"yexjudge/internal/judge/languages"
	"yexjudge/internal/runner"
)

type testcaseExecutor struct {
	compileResult *runner.RunResult
	prepareErr    error
	runs          []*runner.RunResult
	runIndex      int
}

func (e *testcaseExecutor) Compile(context.Context, string, languages.Spec, Limits) (*runner.RunResult, error) {
	if e.compileResult == nil {
		return &runner.RunResult{ExitCode: 0}, nil
	}
	return e.compileResult, nil
}
func (e *testcaseExecutor) StartSandbox(context.Context) (*Sandbox, error) {
	return &Sandbox{ContainerName: "test"}, nil
}
func (e *testcaseExecutor) ConfigureSandbox(context.Context, *Sandbox, Limits) error {
	return nil
}
func (e *testcaseExecutor) PrepareSandbox(context.Context, *Sandbox, string) error {
	return e.prepareErr
}
func (e *testcaseExecutor) ResetSandbox(context.Context, *Sandbox) error {
	return nil
}
func (e *testcaseExecutor) RemoveSandbox(*Sandbox) {}
func (e *testcaseExecutor) RunTestCase(context.Context, *Sandbox, string, languages.Spec) (*runner.RunResult, error) {
	if e.runIndex >= len(e.runs) {
		return nil, errors.New("unexpected testcase execution")
	}
	result := e.runs[e.runIndex]
	e.runIndex++
	return result, nil
}

type testcasePool struct{}

func (testcasePool) Acquire(context.Context, Limits) (*Sandbox, error) {
	return &Sandbox{ContainerName: "test"}, nil
}
func (testcasePool) Release(*Sandbox) {}

type testcaseStore struct {
	submission Submission
	updates    int
}

func (s *testcaseStore) Save(submission Submission) error {
	s.submission = submission
	return nil
}
func (s *testcaseStore) Get(string) (Submission, bool) {
	return s.submission, s.submission.ID != ""
}
func (s *testcaseStore) Update(submission Submission) error {
	s.submission = submission
	s.updates++
	return nil
}

func testCaseJob(expected string) Job {
	return Job{
		Language:   "python",
		SourceCode: "print('test')",
		TestCases: []TestCase{{
			ID:             1,
			ExpectedOutput: expected,
		}},
		Limits: Limits{TimeLimitMs: 1000, MemoryLimitMb: 128},
	}
}

func TestRunTestCasesMapsVerdicts(t *testing.T) {
	tests := []struct {
		name          string
		run           *runner.RunResult
		wantStatus    Status
		wantActual    string
		wantError     string
		wantRuntimeMs int
	}{
		{
			name: "accepted",
			run: &runner.RunResult{
				Stdout:     " expected \n",
				ExitCode:   0,
				TimeUsed:   7 * time.Millisecond,
				MemoryUsed: 2 * 1024 * 1024,
			},
			wantStatus:    Accepted,
			wantRuntimeMs: 7,
		},
		{
			name: "wrong answer",
			run: &runner.RunResult{
				Stdout:   " actual \n",
				ExitCode: 0,
			},
			wantStatus: WrongAnswer,
			wantActual: "actual",
		},
		{
			name: "runtime error",
			run: &runner.RunResult{
				Stdout:   "partial",
				Stderr:   "panic: bad input",
				ExitCode: 1,
			},
			wantStatus: RuntimeError,
			wantActual: "partial",
			wantError:  "panic: bad input",
		},
		{
			name: "timeout",
			run: &runner.RunResult{
				Stdout:   "partial",
				TimedOut: true,
			},
			wantStatus: TimeLimitExceeded,
			wantActual: "partial",
		},
		{
			name: "output limit",
			run: &runner.RunResult{
				Stdout:              "partial",
				OutputLimitExceeded: true,
			},
			wantStatus: OutputLimitExceeded,
			wantActual: "partial",
			wantError:  "program output exceeded the allowed limit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := testCaseJob("expected")
			executor := &testcaseExecutor{runs: []*runner.RunResult{tt.run}}
			result, err := runTestCases(context.Background(), executor, &Sandbox{ContainerName: "test"}, job, languages.Python{})
			if err != nil {
				t.Fatalf("runTestCases() error = %v", err)
			}
			if result.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", result.Status, tt.wantStatus)
			}
			if tt.wantStatus == Accepted {
				if result.RuntimeMs != tt.wantRuntimeMs {
					t.Fatalf("runtimeMs = %d, want %d", result.RuntimeMs, tt.wantRuntimeMs)
				}
				if result.PassedTestCases != 1 || result.TotalTestCases != 1 {
					t.Fatalf("passed/total = %d/%d, want 1/1", result.PassedTestCases, result.TotalTestCases)
				}
				if result.MemoryMb != 2 {
					t.Fatalf("memoryMb = %d, want 2", result.MemoryMb)
				}
				return
			}
			if result.FailedTestCase == nil {
				t.Fatal("expected failed testcase")
			}
			if result.FailedTestCase.ActualOutput != tt.wantActual {
				t.Fatalf("actualOutput = %q, want %q", result.FailedTestCase.ActualOutput, tt.wantActual)
			}
			if result.ErrorMessage != tt.wantError {
				t.Fatalf("errorMessage = %q, want %q", result.ErrorMessage, tt.wantError)
			}
			if result.PassedTestCases != 0 || result.TotalTestCases != 1 {
				t.Fatalf("passed/total = %d/%d, want 0/1", result.PassedTestCases, result.TotalTestCases)
			}
		})
	}
}

func TestRunTestCasesUsesMaximumRuntime(t *testing.T) {
	job := testCaseJob("ok")
	job.TestCases = []TestCase{{ID: 1, ExpectedOutput: "ok"}, {ID: 2, ExpectedOutput: "ok"}}
	executor := &testcaseExecutor{runs: []*runner.RunResult{
		{Stdout: "ok", ExitCode: 0, TimeUsed: 4 * time.Millisecond, MemoryUsed: 1024 * 1024},
		{Stdout: "ok", ExitCode: 0, TimeUsed: 12 * time.Millisecond, MemoryUsed: 3 * 1024 * 1024},
	}}

	result, err := runTestCases(context.Background(), executor, &Sandbox{ContainerName: "test"}, job, languages.Python{})
	if err != nil {
		t.Fatalf("runTestCases() error = %v", err)
	}
	if result.Status != Accepted || result.RuntimeMs != 12 {
		t.Fatalf("result = %+v, want accepted with 12ms", result)
	}
	if result.PassedTestCases != 2 || result.TotalTestCases != 2 {
		t.Fatalf("passed/total = %d/%d, want 2/2", result.PassedTestCases, result.TotalTestCases)
	}
	if result.MemoryMb != 3 {
		t.Fatalf("memoryMb = %d, want 3", result.MemoryMb)
	}
}

func TestProcessSubmissionMapsCompilationError(t *testing.T) {
	store := &testcaseStore{}
	executor := &testcaseExecutor{compileResult: &runner.RunResult{
		ExitCode: 1,
		Stderr:   "missing semicolon",
	}}
	registry := languages.NewRegistry(languages.Cpp{})
	service := NewService(executor, testcasePool{}, store, registry)
	defer service.Close()
	submission := Submission{
		ID:     "compile-error",
		Status: SubmissionQueued,
		Job: Job{
			Language:   "cpp",
			SourceCode: "class Solution {};",
			TestCases:  []TestCase{{ID: 1, Input: "", ExpectedOutput: ""}},
			Limits:     Limits{TimeLimitMs: 1000, MemoryLimitMb: 128},
		},
	}

	result, err := service.ProcessSubmission(context.Background(), submission)
	if err != nil {
		t.Fatalf("ProcessSubmission() error = %v", err)
	}
	if result.Status != CompilationError || result.ErrorMessage != "missing semicolon" {
		t.Fatalf("result = %+v, want compilation error", result)
	}
	if result.TotalTestCases != 1 {
		t.Fatalf("totalTestCases = %d, want 1", result.TotalTestCases)
	}
	if store.submission.Status != SubmissionFinished || store.submission.Result == nil {
		t.Fatalf("stored submission = %+v, want finished result", store.submission)
	}
	if store.updates != 2 {
		t.Fatalf("store updates = %d, want running plus final persistence updates", store.updates)
	}
}

func TestProcessSubmissionPersistsInfrastructureResult(t *testing.T) {
	store := &testcaseStore{}
	executor := &testcaseExecutor{prepareErr: errors.New("docker exec exited with code 126: permission denied")}
	registry := languages.NewRegistry(languages.Python{})
	service := NewService(executor, testcasePool{}, store, registry)
	defer service.Close()
	submission := Submission{
		ID:     "infrastructure-error",
		Status: SubmissionQueued,
		Job: Job{
			Language:   "python",
			SourceCode: "print('test')",
			TestCases:  []TestCase{{ID: 1, ExpectedOutput: "test"}},
			Limits:     Limits{TimeLimitMs: 1000, MemoryLimitMb: 128},
		},
	}

	result, err := service.ProcessSubmission(context.Background(), submission)
	if err != nil {
		t.Fatalf("ProcessSubmission() error = %v", err)
	}
	if result.Status != InfrastructureError {
		t.Fatalf("result status = %q, want infrastructure_error", result.Status)
	}
	if store.submission.Status != SubmissionFailed || store.submission.Result == nil {
		t.Fatalf("stored submission = %+v, want failed infrastructure result", store.submission)
	}
	if store.submission.Result.Status != InfrastructureError || !strings.Contains(store.submission.Result.ErrorMessage, "permission denied") {
		t.Fatalf("stored result = %+v, want diagnostic", store.submission.Result)
	}
	if result.TotalTestCases != 1 {
		t.Fatalf("totalTestCases = %d, want 1", result.TotalTestCases)
	}
}

func TestRunTestCasesPassedCountOnFailure(t *testing.T) {
	job := testCaseJob("ok")
	job.TestCases = []TestCase{
		{ID: 1, ExpectedOutput: "ok"},
		{ID: 2, ExpectedOutput: "ok"},
		{ID: 3, ExpectedOutput: "ok"},
	}
	executor := &testcaseExecutor{runs: []*runner.RunResult{
		{Stdout: "ok", ExitCode: 0, TimeUsed: 2 * time.Millisecond, MemoryUsed: 1024 * 1024},
		{Stdout: "ok", ExitCode: 0, TimeUsed: 3 * time.Millisecond, MemoryUsed: 5 * 1024 * 1024},
		{Stdout: "wrong", ExitCode: 0, TimeUsed: 1 * time.Millisecond, MemoryUsed: 2 * 1024 * 1024},
	}}

	result, err := runTestCases(context.Background(), executor, &Sandbox{ContainerName: "test"}, job, languages.Python{})
	if err != nil {
		t.Fatalf("runTestCases() error = %v", err)
	}
	if result.Status != WrongAnswer {
		t.Fatalf("status = %q, want wrong_answer", result.Status)
	}
	if result.PassedTestCases != 2 {
		t.Fatalf("passedTestCases = %d, want 2", result.PassedTestCases)
	}
	if result.TotalTestCases != 3 {
		t.Fatalf("totalTestCases = %d, want 3", result.TotalTestCases)
	}
	if result.MemoryMb != 5 {
		t.Fatalf("memoryMb = %d, want 5 (max across all executed cases)", result.MemoryMb)
	}
	if result.FailedTestCase == nil || result.FailedTestCase.ID != 3 {
		t.Fatalf("expected failed test case ID 3, got %+v", result.FailedTestCase)
	}
}
