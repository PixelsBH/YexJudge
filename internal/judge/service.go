package judge

import (
	"context"
	"os"
	"yexjudge/internal/runner"
)

type Service struct {
	runner runner.Runner
}

func NewService(r runner.Runner) *Service {
	return &Service{
		runner: r,
	}
}

func (s *Service) Judge(ctx context.Context, job Job) (Result, error) {
	workspace, err := createWorkspace(job)
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(workspace)

	compileRes, err := compileProgram(ctx, s.runner, workspace)
	if err != nil {
		return Result{}, err
	}

	if compileRes.ExitCode != 0 {
		return Result{
			Status:       CompilationError,
			ErrorMessage: compileRes.Stderr,
		}, nil
	}

	containerName, err := startSandbox(ctx, s.runner, workspace, job.Limits)
	if err != nil {
		return Result{}, err
	}
	defer removeSandbox(s.runner, containerName)

	return runTestCases(ctx, s.runner, containerName, job)
}
