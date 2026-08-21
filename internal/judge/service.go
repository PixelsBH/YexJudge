package judge

import (
	"context"
	"os"
	"strings"
	"time"
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

func (s *Service) RunCode(ctx context.Context, job Job) (RunOutput, error) {
	if job.Function != nil || job.Class != nil {
		return RunOutput{
			Status:       ValidationError,
			ErrorMessage: "driver modes are not supported by /run; use /submit",
		}, nil
	}

	spec, ok := s.registry.Get(job.Language)
	if !ok {
		return RunOutput{
			Status:       ValidationError,
			ErrorMessage: "unsupported language",
		}, nil
	}

	workspace, err := createWorkspace(job, spec)
	if err != nil {
		return RunOutput{}, err
	}
	defer os.RemoveAll(workspace)

	if spec.NeedsCompile() {
		compileRes, err := s.executor.Compile(ctx, workspace, spec, job.Limits)
		if err != nil {
			return RunOutput{}, err
		}
		if ctx.Err() != nil {
			return RunOutput{
				Status:       InfrastructureError,
				ErrorMessage: "compile execution was canceled",
			}, nil
		}
		if compileRes.TimedOut {
			return RunOutput{
				Status:       InfrastructureError,
				ErrorOutput:  compileRes.Stderr,
				ErrorMessage: "compile container exceeded the compiler time limit",
			}, nil
		}
		if compileRes.OutputLimitExceeded {
			return RunOutput{
				Status:       OutputLimitExceeded,
				ErrorOutput:  compileRes.Stderr,
				ErrorMessage: "compiler output exceeded the allowed limit",
			}, nil
		}
		if compileRes.ExitCode == 125 {
			return RunOutput{
				Status:       InfrastructureError,
				ErrorOutput:  compileRes.Stderr,
				ErrorMessage: "compile container failed to start",
			}, nil
		}
		if compileRes.ExitCode != 0 {
			return RunOutput{
				Status:       CompilationError,
				ErrorOutput:  compileRes.Stderr,
				ErrorMessage: compileRes.Stderr,
			}, nil
		}
	}

	sandbox, err := s.pool.Acquire(ctx, job.Limits)
	if err != nil {
		return RunOutput{}, err
	}
	defer s.pool.Release(sandbox)

	if err := s.executor.PrepareSandbox(ctx, sandbox, workspace); err != nil {
		return RunOutput{}, err
	}

	ctxRun, cancelRun := context.WithTimeout(ctx, time.Duration(job.Limits.TimeLimitMs)*time.Millisecond)
	defer cancelRun()

	runRes, err := s.executor.RunTestCase(ctxRun, sandbox, "", spec)
	if err != nil {
		return RunOutput{}, err
	}
	if ctx.Err() != nil {
		return RunOutput{
			Status:       InfrastructureError,
			ErrorMessage: "program execution was canceled",
		}, nil
	}

	output := RunOutput{
		Status:    Accepted,
		Output:    strings.TrimSpace(runRes.Stdout),
		RuntimeMs: int(runRes.TimeUsed.Milliseconds()),
	}
	if runRes.OutputLimitExceeded {
		output.Status = OutputLimitExceeded
		output.ErrorMessage = "program output exceeded the allowed limit"
	} else if runRes.TimedOut {
		output.Status = TimeLimitExceeded
	} else if runRes.ExitCode != 0 {
		output.Status = RuntimeError
		output.ErrorOutput = strings.TrimSpace(runRes.Stderr)
		output.ErrorMessage = output.ErrorOutput
	}

	return output, nil
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
	infrastructureFailure := func(message string) (Result, error) {
		result := Result{
			Status:       InfrastructureError,
			ErrorMessage: message,
		}
		submission.Status = SubmissionFailed
		submission.Result = &result
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
		compileRes, err := s.executor.Compile(ctx, workspace, spec, submission.Job.Limits)
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

	sandbox, err := s.pool.Acquire(ctx, submission.Job.Limits)
	if err != nil {
		return infrastructureFailure("acquire sandbox: " + err.Error())
	}
	defer s.pool.Release(sandbox)

	if err := s.executor.PrepareSandbox(ctx, sandbox, workspace); err != nil {
		return infrastructureFailure("prepare sandbox: " + err.Error())
	}

	result, err := runTestCases(ctx, s.executor, sandbox, submission.Job, spec)
	if err != nil {
		return infrastructureFailure("run test cases: " + err.Error())
	}

	submission.Status = SubmissionFinished
	submission.Result = &result
	if err := s.store.Update(submission); err != nil {
		return Result{}, err
	}

	return result, nil
}
