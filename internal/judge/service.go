package judge

import (
	"context"
	"os"
	"yexjudge/internal/judge/languages"
)

type Service struct {
	executor Executor
	pool     SandboxPool
	registry *languages.Registry
}

func NewService(executor Executor, pool SandboxPool, registry *languages.Registry) *Service {
	return &Service{
		executor: executor,
		pool:     pool,
		registry: registry,
	}
}

func (s *Service) Judge(ctx context.Context, job Job) (Result, error) {
	if err := ValidateJob(job); err != nil {
		return Result{
			Status:       ValidationError,
			ErrorMessage: err.Error(),
		}, nil
	}

	spec, ok := s.registry.Get(job.Language)
	if !ok {
		return Result{
			Status:       ValidationError,
			ErrorMessage: "unsupported language",
		}, nil
	}

	workspace, err := createWorkspace(job, spec)

	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(workspace)

	if spec.NeedsCompile() {
		compileRes, err := s.executor.Compile(ctx, workspace, spec)
		if err != nil {
			return Result{}, err
		}

		if compileRes.ExitCode != 0 {
			return Result{
				Status:       CompilationError,
				ErrorMessage: compileRes.Stderr,
			}, nil
		}
	}

	sandbox, err := s.pool.Acquire(ctx, workspace, job.Limits, spec)
	if err != nil {
		return Result{}, err
	}
	defer s.pool.Release(sandbox)

	return runTestCases(ctx, s.executor, sandbox, job, spec)
}
