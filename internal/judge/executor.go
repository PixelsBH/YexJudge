package judge

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"
	"yexjudge/internal/judge/languages"
	"yexjudge/internal/runner"
)

const RuntimeSandboxImage = "yexjudge-runtime:latest"

type Executor interface {
	Compile(ctx context.Context, workspace string, spec languages.Spec) (*runner.RunResult, error)
	StartSandbox(ctx context.Context) (*Sandbox, error)
	ConfigureSandbox(ctx context.Context, sandbox *Sandbox, limits Limits) error
	PrepareSandbox(ctx context.Context, sandbox *Sandbox, workspace string) error
	ResetSandbox(ctx context.Context, sandbox *Sandbox) error
	RemoveSandbox(sandbox *Sandbox)
	RunTestCase(ctx context.Context, sandbox *Sandbox, input string, spec languages.Spec) (*runner.RunResult, error)
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

func (e *DockerExecutor) StartSandbox(ctx context.Context) (*Sandbox, error) {
	containerName := fmt.Sprintf("yexjudge-%d", time.Now().UnixNano())

	ctxContainer, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := e.runner.Run(
		ctxContainer,
		"",
		"docker",
		"run",
		"-d",
		"--name", containerName,
		"--memory", fmt.Sprintf("%dm", MaxMemoryLimitMb),
		"--cpus", "1",
		"--network", "none",
		"--pids-limit", "64",
		"--cap-drop", "ALL",
		"--user", "10001:10001",
		"--security-opt", "no-new-privileges",
		"--tmpfs", "/workspace:rw,exec,size=64m,mode=700,uid=10001,gid=10001",
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=16m,mode=700,uid=10001,gid=10001",
		"--workdir", "/workspace",
		RuntimeSandboxImage,
		"sleep", "infinity",
	)
	if err != nil {
		return nil, err
	}

	return &Sandbox{ContainerName: containerName}, nil
}

func (e *DockerExecutor) ConfigureSandbox(ctx context.Context, sandbox *Sandbox, limits Limits) error {
	memoryLimit := fmt.Sprintf("%dm", limits.MemoryLimitMb)
	_, err := e.runner.Run(
		ctx,
		"",
		"docker",
		"update",
		"--memory", memoryLimit,
		sandbox.ContainerName,
	)
	return err
}

func (e *DockerExecutor) PrepareSandbox(ctx context.Context, sandbox *Sandbox, workspace string) error {
	_, err := e.runner.Run(
		ctx,
		"",
		"docker",
		"exec",
		sandbox.ContainerName,
		"sh",
		"-c",
		"rm -rf /workspace/* /workspace/.[!.]* /workspace/..?*",
	)
	if err != nil {
		return err
	}

	tarCmd := exec.CommandContext(ctx, "tar", "-C", workspace, "-cf", "-", ".")
	dockerCmd := exec.CommandContext(
		ctx,
		"docker",
		"exec",
		"-i",
		sandbox.ContainerName,
		"tar",
		"-xf",
		"-",
		"-C",
		"/workspace",
	)

	pipeReader, pipeWriter := io.Pipe()
	defer pipeReader.Close()

	tarCmd.Stdout = pipeWriter
	dockerCmd.Stdin = pipeReader

	var tarStderr bytes.Buffer
	var dockerStderr bytes.Buffer
	tarCmd.Stderr = &tarStderr
	dockerCmd.Stderr = &dockerStderr

	if err := dockerCmd.Start(); err != nil {
		pipeWriter.Close()
		return fmt.Errorf("start docker extract: %w", err)
	}

	if err := tarCmd.Start(); err != nil {
		pipeWriter.Close()
		_ = dockerCmd.Process.Kill()
		_ = dockerCmd.Wait()
		return fmt.Errorf("start tar archive: %w", err)
	}

	tarErr := tarCmd.Wait()
	pipeWriter.Close()
	dockerErr := dockerCmd.Wait()

	if tarErr != nil {
		return fmt.Errorf("archive workspace: %w: %s", tarErr, tarStderr.String())
	}

	if dockerErr != nil {
		return fmt.Errorf("extract workspace into sandbox: %w: %s", dockerErr, dockerStderr.String())
	}
	_, err = e.runner.Run(
		ctx,
		"",
		"docker",
		"exec",
		sandbox.ContainerName,
		"sh",
		"-c",
		"if [ -f /workspace/main ]; then chmod 700 /workspace/main; fi",
	)
	if err != nil {
		return err
	}
	return nil
}

func (e *DockerExecutor) ResetSandbox(ctx context.Context, sandbox *Sandbox) error {
	_, err := e.runner.Run(
		ctx,
		"",
		"docker",
		"restart",
		"-t", "0",
		sandbox.ContainerName,
	)
	return err
}

func (e *DockerExecutor) RemoveSandbox(sandbox *Sandbox) {
	_, _ = e.runner.Run(
		context.Background(),
		"",
		"docker",
		"rm",
		"-f",
		sandbox.ContainerName,
	)
}

func (e *DockerExecutor) RunTestCase(
	ctx context.Context,
	sandbox *Sandbox,
	input string,
	spec languages.Spec,
) (*runner.RunResult, error) {
	execArgs := []string{
		"exec",
		"-i",
		sandbox.ContainerName,
	}
	execArgs = append(execArgs, spec.RunCommand()...)

	return e.runner.Run(
		ctx,
		input+"\n",
		"docker",
		execArgs...,
	)
}
