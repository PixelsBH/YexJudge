package judge

import (
	"context"
	"fmt"
	"time"
	"yexjudge/internal/judge/languages"
	"yexjudge/internal/runner"
)

type Executor interface {
	Compile(ctx context.Context, workspace string, spec languages.Spec) (*runner.RunResult, error)
	StartSandbox(ctx context.Context, workspace string, limits Limits) (string, error)
	RemoveSandbox(containerName string)
	RunTestCase(ctx context.Context, containerName string, input string, spec languages.Spec) (*runner.RunResult, error)
}

type DockerExecutor struct {
	runner runner.Runner
}

func NewDockerExecutor(r runner.Runner) *DockerExecutor {
	return &DockerExecutor{runner: r}
}

func (e *DockerExecutor) Compile(ctx context.Context,
	workspace string, spec languages.Spec) (*runner.RunResult, error) {
	ctxCompile, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	args := []string{
		"run",
		"--rm",
		"-v", workspace + ":/workspace",
		spec.CompileImage(),
	}
	args = append(args, spec.CompileCommand()...)

	return e.runner.Run(ctxCompile, "", "docker", args...)
}

func (e *DockerExecutor) StartSandbox(ctx context.Context,
	workspace string, limits Limits) (string, error) {
	containerName := fmt.Sprintf("yexjudge-%d", time.Now().UnixNano())
	memoryLimit := fmt.Sprintf("%dm", limits.MemoryLimitMb)

	ctxContainer, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := e.runner.Run(
		ctxContainer,
		"",
		"docker",
		"run",
		"-d",
		"--name", containerName,
		"--memory", memoryLimit,
		"--cpus", "1",
		"--network", "none",
		"--pids-limit", "64",
		"--security-opt", "no-new-privileges",
		"--tmpfs", "/tmp",
		"--workdir", "/workspace",
		"-v", workspace+":/workspace",
		"alpine",
		"sleep", "60",
	)
	if err != nil {
		return "", err
	}

	return containerName, nil
}

func (e *DockerExecutor) RemoveSandbox(containerName string) {
	_, _ = e.runner.Run(
		context.Background(),
		"",
		"docker",
		"rm",
		"-f",
		containerName,
	)
}

func (e *DockerExecutor) RunTestCase(
	ctx context.Context,
	containerName string,
	input string,
	spec languages.Spec,
) (*runner.RunResult, error) {
	execArgs := []string{
		"exec",
		"-i",
		containerName,
	}
	execArgs = append(execArgs, spec.RunCommand()...)

	return e.runner.Run(
		ctx,
		input+"\n",
		"docker",
		execArgs...,
	)
}
