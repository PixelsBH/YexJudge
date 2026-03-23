package judge

import (
	"context"
	"os"
	"yexjudge/internal/judge/languages"
)

type Service struct {
	executor Executor
	pool     SandboxPool
	store    SubmissionStore
	registry *languages.Registry
}

func NewService(executor Executor, pool SandboxPool, store SubmissionStore, registry *languages.Registry) *Service {
	return &Service{
		executor: executor,
		pool:     pool,
		store:    store,
		registry: registry,
	}
}

func (s *Service) ProcessSubmission(ctx context.Context, submission Submission) (Result, error) {
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

	workspace, err := createWorkspace(submission.Job, spec)
	if err != nil {
		submission.Status = SubmissionFailed
		if updateErr := s.store.Update(submission); updateErr != nil {
			return Result{}, updateErr
		}
		return Result{}, err
	}
	defer os.RemoveAll(workspace)

	if spec.NeedsCompile() {
		compileRes, err := s.executor.Compile(ctx, workspace, spec)
		if err != nil {
			submission.Status = SubmissionFailed
			if updateErr := s.store.Update(submission); updateErr != nil {
				return Result{}, updateErr
			}
			return Result{}, err
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

	sandbox, err := s.pool.Acquire(ctx, workspace, submission.Job.Limits, spec)
	if err != nil {
		submission.Status = SubmissionFailed
		if updateErr := s.store.Update(submission); updateErr != nil {
			return Result{}, updateErr
		}
		return Result{}, err
	}
	defer s.pool.Release(sandbox)

	result, err := runTestCases(ctx, s.executor, sandbox, submission.Job, spec)
	if err != nil {
		submission.Status = SubmissionFailed
		if updateErr := s.store.Update(submission); updateErr != nil {
			return Result{}, updateErr
		}
		return Result{}, err
	}

	submission.Status = SubmissionFinished
	submission.Result = &result
	if err := s.store.Update(submission); err != nil {
		return Result{}, err
	}

	return result, nil
}
