package judge

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"yexjudge/internal/judge/languages"
	"yexjudge/internal/runner"
)

const (
	RuntimeSandboxImage         = "yexjudge-runtime:latest"
	MinCompileMemoryLimitMb     = 512
	CompileTimeout              = 30 * time.Second
	compileWorkspaceTmpfsBytes  = 128 << 20
	compileTemporaryTmpfsBytes  = 256 << 20
	compileFileSizeLimitBytes   = 128 << 20
	compileArtifactArchiveBytes = 80 << 20
	transferCleanupTimeout      = 5 * time.Second
	caseCleanupTimeout          = 30 * time.Second
	sandboxReadyTimeout         = 2 * time.Second
	sandboxReadyPoll            = 50 * time.Millisecond
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

func compileContainerUser() string {
	if os.Getuid() == 0 {
		return "10001:10001"
	}
	return fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
}

func compileContainerTmpfsOwner() string {
	if os.Getuid() == 0 {
		return "uid=10001,gid=10001"
	}
	return fmt.Sprintf("uid=%d,gid=%d", os.Getuid(), os.Getgid())
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

	sourceName := filepath.Base(spec.SourceFileName())
	if sourceName == "." || sourceName == string(filepath.Separator) || sourceName != spec.SourceFileName() {
		return nil, fmt.Errorf("invalid compiler source filename %q", spec.SourceFileName())
	}
	if _, err := os.Stat(filepath.Join(workspace, sourceName)); err != nil {
		return nil, fmt.Errorf("stat compiler source: %w", err)
	}

	archiveDirectory, err := os.MkdirTemp("", "yexjudge-compiler-archive-*")
	if err != nil {
		return nil, fmt.Errorf("create compiler archive directory: %w", err)
	}
	defer os.RemoveAll(archiveDirectory)
	if os.Getuid() == 0 {
		if err := os.Chown(archiveDirectory, 10001, 10001); err != nil {
			return nil, fmt.Errorf("set compiler archive directory owner: %w", err)
		}
	}
	archivePath := filepath.Join(archiveDirectory, "artifacts.tar")

	compileMemoryMb := limits.MemoryLimitMb
	if compileMemoryMb < MinCompileMemoryLimitMb {
		compileMemoryMb = MinCompileMemoryLimitMb
	}
	compileScript := fmt.Sprintf(`cp -- /source/%s /workspace/%s || exit $?
"$@" || exit $?
tar -C /workspace -cf /artifacts/artifacts.tar . || exit $?
archive_size=$(wc -c < /artifacts/artifacts.tar) || exit $?
[ "$archive_size" -le %d ] || { echo 'compiler artifact archive exceeds the allowed size' >&2; exit 1; }`,
		shellQuote(sourceName),
		shellQuote(sourceName),
		compileArtifactArchiveBytes,
	)
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
		"--tmpfs", fmt.Sprintf("/workspace:rw,exec,size=%dm,mode=700,%s", compileWorkspaceTmpfsBytes/(1<<20), compileContainerTmpfsOwner()),
		"--tmpfs", fmt.Sprintf("/tmp:rw,exec,nosuid,size=%dm,mode=700,%s", compileTemporaryTmpfsBytes/(1<<20), compileContainerTmpfsOwner()),
		"--env", "HOME=/tmp",
		"--env", "GOCACHE=/tmp/go-build",
		"--env", "GOMODCACHE=/tmp/go-mod",
		"--ulimit", "nofile=1024:1024",
		"--ulimit", fmt.Sprintf("fsize=%d:%d", compileFileSizeLimitBytes, compileFileSizeLimitBytes),
		"--user", compileContainerUser(),
		"--workdir", "/workspace",
		"-v", workspace + ":/source:ro",
		"-v", archiveDirectory + ":/artifacts:rw",
		spec.CompileImage(),
		"sh", "-c", compileScript, "yexjudge-compile",
	}
	args = append(args, spec.CompileCommand()...)

	result, err := e.runner.Run(ctxCompile, "", "docker", args...)
	if result == nil && err == nil {
		return nil, fmt.Errorf("compile container returned no result")
	}
	if err != nil || result.ExitCode != 0 || result.OutputLimitExceeded || ctxCompile.Err() != nil {
		return result, err
	}
	if err := importCompilerArtifacts(archivePath, workspace); err != nil {
		return nil, fmt.Errorf("import compiler artifacts: %w", err)
	}
	return result, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
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

type boundedArchiveWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *boundedArchiveWriter) Write(data []byte) (int, error) {
	if int64(len(data)) <= w.remaining {
		n, err := w.writer.Write(data)
		w.remaining -= int64(n)
		return n, err
	}
	if w.remaining <= 0 {
		return 0, fmt.Errorf("archive exceeds the %d-byte limit", maxWorkspaceArchiveSize)
	}
	n, err := w.writer.Write(data[:w.remaining])
	w.remaining -= int64(n)
	if err != nil {
		return n, err
	}
	return n, fmt.Errorf("archive exceeds the %d-byte limit", maxWorkspaceArchiveSize)
}

type commandProcess struct {
	command *exec.Cmd
	done    <-chan error
}

func stopAndReapCommands(commands ...commandProcess) []error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), transferCleanupTimeout)
	defer cancel()
	for _, process := range commands {
		if process.command.Process != nil {
			_ = process.command.Process.Kill()
		}
	}

	commandErrors := make([]error, len(commands))
	for i, process := range commands {
		select {
		case commandErrors[i] = <-process.done:
		case <-cleanupCtx.Done():
			commandErrors[i] = cleanupCtx.Err()
			return commandErrors
		}
	}
	return commandErrors
}

func transferWorkspaceArchive(ctx context.Context, producer, extractor *exec.Cmd) error {
	pipeReader, pipeWriter := io.Pipe()
	defer pipeReader.Close()
	defer pipeWriter.Close()
	producer.Stdout = &boundedArchiveWriter{writer: pipeWriter, remaining: maxWorkspaceArchiveSize}
	extractor.Stdin = pipeReader

	if err := extractor.Start(); err != nil {
		_ = pipeReader.Close()
		_ = pipeWriter.CloseWithError(err)
		return fmt.Errorf("start docker extract: %w", err)
	}
	extractorDone := make(chan error, 1)
	go func() { extractorDone <- extractor.Wait() }()

	if err := producer.Start(); err != nil {
		_ = pipeReader.Close()
		_ = pipeWriter.CloseWithError(err)
		reapErrors := stopAndReapCommands(commandProcess{command: extractor, done: extractorDone})
		if reapErrors[0] != nil {
			return fmt.Errorf("start tar archive: %w (reap extractor: %v)", err, reapErrors[0])
		}
		return fmt.Errorf("start tar archive: %w", err)
	}
	producerDone := make(chan error, 1)
	go func() { producerDone <- producer.Wait() }()

	var producerErr error
	producerFinished := false
	for {
		select {
		case producerErr = <-producerDone:
			producerFinished = true
			if producerErr != nil {
				_ = pipeWriter.CloseWithError(producerErr)
				_ = pipeReader.Close()
				reapErrors := stopAndReapCommands(commandProcess{command: extractor, done: extractorDone})
				if reapErrors[0] != nil {
					return fmt.Errorf("archive workspace: %w (extractor: %v)", producerErr, reapErrors[0])
				}
				return fmt.Errorf("archive workspace: %w", producerErr)
			}
			_ = pipeWriter.Close()
		case extractorErr := <-extractorDone:
			if !producerFinished {
				_ = pipeReader.Close()
				_ = pipeWriter.CloseWithError(fmt.Errorf("extractor exited before archive completed"))
				reapErrors := stopAndReapCommands(commandProcess{command: producer, done: producerDone})
				producerErr = reapErrors[0]
				if extractorErr != nil {
					return fmt.Errorf("extract workspace into sandbox: %w: extractor exited before archive completed", extractorErr)
				}
				if producerErr != nil {
					return fmt.Errorf("extractor exited before archive completed (producer: %v)", producerErr)
				}
				return fmt.Errorf("extractor exited before archive completed")
			}
			if extractorErr != nil {
				return fmt.Errorf("extract workspace into sandbox: %w", extractorErr)
			}
			return nil
		case <-ctx.Done():
			_ = pipeReader.Close()
			_ = pipeWriter.CloseWithError(ctx.Err())
			var reapErrors []error
			if producerFinished {
				reapErrors = stopAndReapCommands(commandProcess{command: extractor, done: extractorDone})
				return fmt.Errorf("transfer workspace archive canceled: %w (extractor: %v)", ctx.Err(), reapErrors[0])
			}
			reapErrors = stopAndReapCommands(
				commandProcess{command: producer, done: producerDone},
				commandProcess{command: extractor, done: extractorDone},
			)
			return fmt.Errorf("transfer workspace archive canceled: %w (producer: %v, extractor: %v)", ctx.Err(), reapErrors[0], reapErrors[1])
		}
	}
}

func (e *DockerExecutor) PrepareSandbox(ctx context.Context, sandbox *Sandbox, workspace string) error {
	if err := validateWorkspaceForTransfer(workspace); err != nil {
		return err
	}

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

	producer := exec.CommandContext(ctx, "tar", "-C", workspace, "-cf", "-", ".")
	extractor := exec.CommandContext(
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
	var producerStderr diagnosticBuffer
	var extractorStderr diagnosticBuffer
	producerStderr.limit = runner.DefaultOutputLimitBytes
	extractorStderr.limit = runner.DefaultOutputLimitBytes
	producer.Stderr = &producerStderr
	extractor.Stderr = &extractorStderr
	if err := transferWorkspaceArchive(ctx, producer, extractor); err != nil {
		return fmt.Errorf("transfer workspace archive: %w; tar: %s; docker: %s", err, producerStderr.String(), extractorStderr.String())
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
	memoryMarker := fmt.Sprintf("__YEXJUDGE_MAX_RSS_KB_%d:", time.Now().UnixNano())
	execArgs := []string{
		"exec",
		"-i",
		sandbox.ContainerName,
		"/usr/bin/time",
		"-f",
		memoryMarker + "%M\\n",
		"--",
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
	result.Stderr, result.MemoryUsed = extractMeasuredMemory(result.Stderr, memoryMarker)

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

func extractMeasuredMemory(stderr, marker string) (string, int64) {
	markerIndex := strings.LastIndex(stderr, marker)
	if markerIndex < 0 {
		return stderr, 0
	}

	memoryOutput := stderr[markerIndex+len(marker):]
	if newline := strings.IndexByte(memoryOutput, '\n'); newline >= 0 {
		memoryOutput = memoryOutput[:newline]
	}
	memoryKB, err := strconv.ParseInt(strings.TrimSpace(memoryOutput), 10, 64)
	if err != nil || memoryKB < 0 {
		return stderr, 0
	}

	return stderr[:markerIndex], memoryKB * 1024
}
