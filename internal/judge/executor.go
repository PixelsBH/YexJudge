package judge

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
	"yexjudge/internal/judge/languages"
	"yexjudge/internal/runner"
)

const (
	RuntimeSandboxImage     = "yexjudge-runtime:latest"
	MinCompileMemoryLimitMb = 512
	CompileTimeout          = 30 * time.Second
	sandboxReadyTimeout     = 2 * time.Second
	sandboxReadyPoll        = 50 * time.Millisecond
)

type Executor interface {
	Compile(ctx context.Context, workspace string, spec languages.Spec, limits Limits) (*runner.RunResult, error)
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

func dockerCommandError(action string, result *runner.RunResult) error {
	if result == nil {
		return fmt.Errorf("%s returned no result", action)
	}
	if result.ExitCode == 0 {
		return nil
	}
	message := strings.TrimSpace(result.Stderr)
	if len(message) > runner.DefaultOutputLimitBytes {
		message = message[:runner.DefaultOutputLimitBytes]
	}
	if message == "" {
		return fmt.Errorf("%s exited with code %d", action, result.ExitCode)
	}
	return fmt.Errorf("%s exited with code %d: %s", action, result.ExitCode, message)
}

type diagnosticBuffer struct {
	bytes.Buffer
	limit int
}

func (b *diagnosticBuffer) Write(data []byte) (int, error) {
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if len(data) > remaining {
			_, _ = b.Buffer.Write(data[:remaining])
		} else {
			_, _ = b.Buffer.Write(data)
		}
	}
	return len(data), nil
}

func (e *DockerExecutor) waitForSandboxReady(ctx context.Context, sandbox *Sandbox) error {
	readyCtx, cancel := context.WithTimeout(ctx, sandboxReadyTimeout)
	defer cancel()

	var lastErr error
	for {
		result, err := e.runner.Run(
			readyCtx,
			"",
			"docker",
			"exec",
			sandbox.ContainerName,
			"true",
		)
		if err != nil {
			lastErr = err
		} else if commandErr := dockerCommandError("check sandbox readiness", result); commandErr != nil {
			lastErr = commandErr
		} else {
			return nil
		}

		timer := time.NewTimer(sandboxReadyPoll)
		select {
		case <-readyCtx.Done():
			if lastErr == nil {
				lastErr = readyCtx.Err()
			}
			return fmt.Errorf("sandbox %s did not become ready: %w", sandbox.ContainerName, lastErr)
		case <-timer.C:
		}
	}
}

func (e *DockerExecutor) Compile(ctx context.Context,
	workspace string, spec languages.Spec, limits Limits) (*runner.RunResult, error) {
	ctxCompile, cancel := context.WithTimeout(ctx, CompileTimeout)
	defer cancel()
	compileContainer := fmt.Sprintf("yexjudge-compile-%d", time.Now().UnixNano())
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = e.runner.Run(cleanupCtx, "", "docker", "rm", "-f", compileContainer)
	}()

	compileMemoryMb := limits.MemoryLimitMb
	if compileMemoryMb < MinCompileMemoryLimitMb {
		compileMemoryMb = MinCompileMemoryLimitMb
	}

	args := []string{
		"run",
		"--rm",
		"--name", compileContainer,
		"--network", "none",
		"--memory", fmt.Sprintf("%dm", compileMemoryMb),
		"--memory-swap", fmt.Sprintf("%dm", compileMemoryMb),
		"--cpus", "1",
		"--pids-limit", "128",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--read-only",
		"--tmpfs", "/tmp:rw,exec,nosuid,size=128m,mode=1777",
		"--env", "HOME=/tmp",
		"--env", "GOCACHE=/tmp/go-build",
		"--env", "GOMODCACHE=/tmp/go-mod",
		"--ulimit", "nofile=1024:1024",
		"--user", "10001:10001",
		"--workdir", "/workspace",
		"-v", workspace + ":/workspace:rw",
		spec.CompileImage(),
	}
	args = append(args, spec.CompileCommand()...)

	return e.runner.Run(ctxCompile, "", "docker", args...)
}

func (e *DockerExecutor) StartSandbox(ctx context.Context) (*Sandbox, error) {
	containerName := fmt.Sprintf("yexjudge-%d", time.Now().UnixNano())

	ctxContainer, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	result, err := e.runner.Run(
		ctxContainer,
		"",
		"docker",
		"run",
		"-d",
		"--name", containerName,
		"--memory", fmt.Sprintf("%dm", MaxMemoryLimitMb),
		"--memory-swap", fmt.Sprintf("%dm", MaxMemoryLimitMb),
		"--cpus", "1",
		"--network", "none",
		"--pids-limit", "64",
		"--ulimit", "nofile=1024:1024",
		"--cap-drop", "ALL",
		"--user", "10001:10001",
		"--security-opt", "no-new-privileges",
		"--read-only",
		"--tmpfs", "/workspace:rw,exec,size=64m,mode=700,uid=10001,gid=10001",
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=16m,mode=700,uid=10001,gid=10001",
		"--workdir", "/workspace",
		RuntimeSandboxImage,
		"sleep", "infinity",
	)
	if err != nil {
		return nil, err
	}
	if err := dockerCommandError("start sandbox", result); err != nil {
		return nil, err
	}

	sandbox := &Sandbox{ContainerName: containerName}
	if err := e.waitForSandboxReady(ctx, sandbox); err != nil {
		e.RemoveSandbox(sandbox)
		return nil, err
	}

	return sandbox, nil
}

func (e *DockerExecutor) ConfigureSandbox(ctx context.Context, sandbox *Sandbox, limits Limits) error {
	memoryLimit := fmt.Sprintf("%dm", limits.MemoryLimitMb)
	result, err := e.runner.Run(
		ctx,
		"",
		"docker",
		"update",
		"--memory", memoryLimit,
		"--memory-swap", memoryLimit,
		"--cpus", "1",
		"--pids-limit", "64",
		sandbox.ContainerName,
	)
	if err != nil {
		return err
	}
	return dockerCommandError("configure sandbox", result)
}

func (e *DockerExecutor) PrepareSandbox(ctx context.Context, sandbox *Sandbox, workspace string) error {
	result, err := e.runner.Run(
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
	if err := dockerCommandError("clear sandbox workspace", result); err != nil {
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

	var tarStderr diagnosticBuffer
	var dockerStderr diagnosticBuffer
	tarStderr.limit = runner.DefaultOutputLimitBytes
	dockerStderr.limit = runner.DefaultOutputLimitBytes
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
	result, err = e.runner.Run(
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
	return dockerCommandError("set sandbox executable permissions", result)
}

func (e *DockerExecutor) ResetSandbox(ctx context.Context, sandbox *Sandbox) error {
	result, err := e.runner.Run(
		ctx,
		"",
		"docker",
		"restart",
		"-t", "0",
		sandbox.ContainerName,
	)
	if err != nil {
		return err
	}
	if err := dockerCommandError("reset sandbox", result); err != nil {
		return err
	}
	return e.waitForSandboxReady(ctx, sandbox)
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

	result, err := e.runner.Run(
		ctx,
		input+"\n",
		"docker",
		execArgs...,
	)
	if err != nil {
		return nil, err
	}

	// docker exec is a client-side command. Canceling that client does not
	// reliably terminate the process that the daemon started in the sandbox.
	// Restarting the reusable container makes both timeout and output-limit
	// paths kill the entire process tree before the sandbox is returned to the
	// pool.
	if result.OutputLimitExceeded || ctx.Err() != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		restartResult, restartErr := e.runner.Run(
			cleanupCtx,
			"",
			"docker",
			"restart",
			"-t", "0",
			sandbox.ContainerName,
		)
		if restartErr != nil {
			sandbox.needsReplace = true
			return nil, fmt.Errorf("restart sandbox after canceled execution: %w", restartErr)
		}
		if err := dockerCommandError("restart sandbox after canceled execution", restartResult); err != nil {
			sandbox.needsReplace = true
			return nil, err
		}
		if err := e.waitForSandboxReady(cleanupCtx, sandbox); err != nil {
			sandbox.needsReplace = true
			return nil, err
		}
		sandbox.restarted = true
	}

	if ctx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
	}
	return result, nil
}
