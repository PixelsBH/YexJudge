package judge

import (
	"context"
	"log/slog"
	"os"
	"time"
	"yexjudge/internal/judge/languages"
	"yexjudge/internal/observability"
)

type Service struct {
	executor Executor
	pool     SandboxPool
	store    SubmissionStore
	registry *languages.Registry
	metrics  *observability.Metrics
	compile  *CompileLimiter
}

func NewService(executor Executor, pool SandboxPool, store SubmissionStore, registry *languages.Registry) *Service {
	return NewServiceWithMetrics(executor, pool, store, registry, observability.NewMetrics())
}

func NewServiceWithMetrics(executor Executor, pool SandboxPool, store SubmissionStore, registry *languages.Registry, metrics *observability.Metrics) *Service {
	return NewServiceWithMetricsAndCompileSlots(executor, pool, store, registry, metrics, 1)
}

func NewServiceWithMetricsAndCompileSlots(executor Executor, pool SandboxPool, store SubmissionStore, registry *languages.Registry, metrics *observability.Metrics, compileSlots int) *Service {
	if metrics == nil {
		metrics = observability.NewMetrics()
	}
	return &Service{
		executor: executor,
		pool:     pool,
		store:    store,
		registry: registry,
		metrics:  metrics,
		compile:  NewCompileLimiter(compileSlots),
	}
}

func (s *Service) Metrics() *observability.Metrics { return s.metrics }

func (s *Service) ProcessSubmission(ctx context.Context, submission Submission) (Result, error) {
	processingStarted := time.Now()
	workerID := WorkerID(ctx)
	slog.Info("submission processing started",
		"submission_id", submission.ID,
		"language", submission.Job.Language,
		"worker_id", workerID,
		"attempt", submission.AttemptCount,
		"status", submission.Status,
	)
	defer func() {
		slog.Info("submission processing finished",
			"submission_id", submission.ID,
			"language", submission.Job.Language,
			"worker_id", workerID,
			"attempt", submission.AttemptCount,
			"status", submission.Status,
			"duration_ms", time.Since(processingStarted).Milliseconds(),
		)
	}()
	if err := ValidateJob(submission.Job); err != nil {
		result := Result{
			Status:       ValidationError,
			ErrorMessage: err.Error(),
		}
		submission.Status = SubmissionFinished
		submission.Result = &result
		if updateErr := s.store.Update(submission); updateErr != nil {
			return Result{}, updateErr
		}
		return result, nil
	}

	spec, ok := s.registry.Get(submission.Job.Language)
	if !ok {
		result := Result{
			Status:       ValidationError,
			ErrorMessage: "unsupported language",
		}
		submission.Status = SubmissionFinished
		submission.Result = &result
		if updateErr := s.store.Update(submission); updateErr != nil {
			return Result{}, updateErr
		}
		return result, nil
	}

	submission.Status = SubmissionRunning
	if err := s.store.Update(submission); err != nil {
		return Result{}, err
	}
	infrastructureFailure := func(message string) (Result, error) {
		result := Result{
			Status:       InfrastructureError,
			ErrorMessage: message,
		}
		submission.Status = SubmissionFailed
		submission.Result = &result
		submission.FailureMessage = message
		slog.Error("submission infrastructure failure",
			"submission_id", submission.ID,
			"language", submission.Job.Language,
			"worker_id", workerID,
			"attempt", submission.AttemptCount,
			"error", message,
		)
		if err := s.store.Update(submission); err != nil {
			return Result{}, err
		}
		return result, nil
	}

	workspace, err := createWorkspace(submission.Job, spec)
	if err != nil {
		return infrastructureFailure("create workspace: " + err.Error())
	}
	defer os.RemoveAll(workspace)

	if spec.NeedsCompile() {
		if err := s.compile.Acquire(ctx); err != nil {
			return infrastructureFailure("acquire compile slot: " + err.Error())
		}
		defer s.compile.Release()

		compileStarted := time.Now()
		compileRes, err := s.executor.Compile(ctx, workspace, spec, submission.Job.Limits)
		compileDuration := time.Since(compileStarted)
		s.metrics.ObserveCompile(compileDuration)
		slog.Info("submission compile finished",
			"submission_id", submission.ID,
			"language", submission.Job.Language,
			"worker_id", workerID,
			"duration_ms", compileDuration.Milliseconds(),
		)
		if err != nil {
			return infrastructureFailure("compile execution: " + err.Error())
		}
		if ctx.Err() != nil {
			return infrastructureFailure("compile execution was canceled")
		}

		if compileRes.TimedOut {
			return infrastructureFailure("compile container exceeded the compiler time limit")
		}

		if compileRes.OutputLimitExceeded {
			result := Result{
				Status:       OutputLimitExceeded,
				ErrorMessage: "compiler output exceeded the allowed limit",
			}

			submission.Status = SubmissionFinished
			submission.Result = &result
			if err := s.store.Update(submission); err != nil {
				return Result{}, err
			}
			return result, nil
		}

		if compileRes.ExitCode == 125 {
			return infrastructureFailure("compile container failed to start")
		}

		if compileRes.ExitCode != 0 {
			result := Result{
				Status:       CompilationError,
				ErrorMessage: compileRes.Stderr,
			}

			submission.Status = SubmissionFinished
			submission.Result = &result
			if err := s.store.Update(submission); err != nil {
				return Result{}, err
			}
			return result, nil
		}
	}

	acquireStarted := time.Now()
	sandbox, err := s.pool.Acquire(ctx, submission.Job.Limits)
	acquireDuration := time.Since(acquireStarted)
	s.metrics.ObserveAcquire(acquireDuration)
	slog.Info("sandbox acquired",
		"submission_id", submission.ID,
		"language", submission.Job.Language,
		"worker_id", workerID,
		"duration_ms", acquireDuration.Milliseconds(),
	)
	if err != nil {
		return infrastructureFailure("acquire sandbox: " + err.Error())
	}
	defer s.pool.Release(sandbox)

	stagingStarted := time.Now()
	if err := s.executor.PrepareSandbox(ctx, sandbox, workspace); err != nil {
		s.metrics.ObserveStaging(time.Since(stagingStarted))
		return infrastructureFailure("prepare sandbox: " + err.Error())
	}
	stagingDuration := time.Since(stagingStarted)
	s.metrics.ObserveStaging(stagingDuration)
	slog.Info("sandbox staging finished",
		"submission_id", submission.ID,
		"language", submission.Job.Language,
		"worker_id", workerID,
		"duration_ms", stagingDuration.Milliseconds(),
	)

	testcaseStarted := time.Now()
	result, err := runTestCases(ctx, s.executor, sandbox, submission.Job, spec)
	testcaseDuration := time.Since(testcaseStarted)
	s.metrics.ObserveTestcase(testcaseDuration)
	s.metrics.ObserveRuntime(time.Duration(result.RuntimeMs) * time.Millisecond)
	slog.Info("submission testcases finished",
		"submission_id", submission.ID,
		"language", submission.Job.Language,
		"worker_id", workerID,
		"duration_ms", testcaseDuration.Milliseconds(),
		"runtime_ms", result.RuntimeMs,
	)
	if err != nil {
		return infrastructureFailure("run test cases: " + err.Error())
	}
	if result.Status == InfrastructureError {
		return infrastructureFailure(result.ErrorMessage)
	}

	submission.Status = SubmissionFinished
	submission.Result = &result
	if err := s.store.Update(submission); err != nil {
		return Result{}, err
	}

	return result, nil
}
