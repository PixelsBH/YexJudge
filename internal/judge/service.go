package judge

import (
	"context"
	"os"
	"yexjudge/internal/judge/languages"
)

type Service struct {
	executor Executor
	registry *languages.Registry
}

func NewService(executor Executor, registry *languages.Registry) *Service {
	return &Service{
		executor: executor,
		registry: registry,
	}
}

func (s *Service) Judge(ctx context.Context, job Job) (Result, error) {
	spec, ok := s.registry.Get(job.Language)
	if !ok {
		return Result{
			Status:       CompilationError,
			ErrorMessage: "unsupported language",
		}, nil
	}

	workspace, err := createWorkspace(job, spec)

	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(workspace)

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

	containerName, err := s.executor.StartSandbox(ctx, workspace, job.Limits)
	if err != nil {
		return Result{}, err
	}
	defer s.executor.RemoveSandbox(containerName)

	return runTestCases(ctx, s.executor, containerName, job, spec)
}
