package judge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"yexjudge/internal/judge/languages"
	"yexjudge/internal/runner"
)

const (
	RuntimeSandboxImage     = "yexjudge-runtime:latest"
	MinCompileMemoryLimitMb = 512
	CompileTimeout          = 30 * time.Second
	DockerOperationTimeout  = 10 * time.Second
	DockerCleanupTimeout    = 5 * time.Second
	MaxJobExecutionTime     = 2 * time.Minute
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

// Images are operator-reviewed local images, not submission-supplied values.
// Empty options preserve existing defaults. Prefer immutable sha256 digests.
// Environment integration belongs to the application constructing the executor.
type DockerExecutorOptions struct {
	RuntimeImage  string
	CompileImages map[string]string
}

type stagedWorkspace struct {
	archive  string
	deadline time.Time
}

type DockerExecutor struct {
	runner        runner.Runner
	runtimeImage  string
	compileImages map[string]string
	staged        sync.Map // container name -> stagedWorkspace; bounded by the sandbox pool
}

func NewDockerExecutor(r runner.Runner) *DockerExecutor {
	e, _ := NewDockerExecutorWithOptions(r, DockerExecutorOptions{})
	return e
}

func NewDockerExecutorWithOptions(r runner.Runner, opts DockerExecutorOptions) (*DockerExecutor, error) {
	image := opts.RuntimeImage
	if image == "" {
		image = RuntimeSandboxImage
	}
	if err := ValidateDockerImageReference(image); err != nil {
		return nil, fmt.Errorf("invalid runtime image reference")
	}
	images := make(map[string]string, len(opts.CompileImages))
	for language, image := range opts.CompileImages {
		switch language {
		case "c", "cpp", "go", "java":
		default:
			return nil, fmt.Errorf("unsupported compile image language")
		}
		if err := ValidateDockerImageReference(image); err != nil {
			return nil, fmt.Errorf("invalid compile image reference")
		}
		images[language] = image
	}
	return &DockerExecutor{runner: r, runtimeImage: image, compileImages: images}, nil
}

var imageReferencePattern = regexp.MustCompile(`^(?:[a-z0-9]+(?:[.-][a-z0-9]+)*(?::[0-9]{1,5})?/)?[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?(?:@sha256:[a-f0-9]{64})?$`)

// ValidateDockerImageReference checks a deliberately conservative Docker
// reference grammar. It does not attest to the image's contents or provenance.
func ValidateDockerImageReference(image string) error {
	if len(image) > 512 || !imageReferencePattern.MatchString(image) {
		return fmt.Errorf("invalid Docker image reference")
	}
	return nil
}

type dockerCommandFailure struct {
	action   string
	exitCode int
}

func (e *dockerCommandFailure) Error() string {
	return fmt.Sprintf("%s exited with code %d", e.action, e.exitCode)
}

func sanitizedOperationError(action string, err error) error {
	var commandFailure *dockerCommandFailure
	if errors.As(err, &commandFailure) {
		return commandFailure
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s exceeded time limit", action)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s was canceled", action)
	}
	// Emit only a fixed category, never the original error text or wrapping.
	if errors.Is(err, os.ErrPermission) || strings.Contains(err.Error(), "permission denied") {
		return fmt.Errorf("%s failed: permission denied; check Docker and filesystem permissions", action)
	}
	return fmt.Errorf("%s failed; check Docker availability and reviewed image prerequisites", action)
}

func dockerCommandError(action string, result *runner.RunResult) error {
	if result == nil {
		return fmt.Errorf("%s returned no result", action)
	}
	if result.TimedOut {
		return fmt.Errorf("%s exceeded time limit", action)
	}
	if result.OutputLimitExceeded {
		return fmt.Errorf("%s exceeded output limit", action)
	}
	if result.ExitCode != 0 {
		return &dockerCommandFailure{action: action, exitCode: result.ExitCode}
	}
	return nil
}

func (e *DockerExecutor) operation(ctx context.Context, action, input string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, DockerOperationTimeout)
	defer cancel()
	result, err := e.runner.Run(ctx, input, "docker", args...)
	if err != nil {
		return sanitizedOperationError(action, err)
	}
	return dockerCommandError(action, result)
}

// Dockerfile VOLUME declarations otherwise create writable host-backed
// anonymous volumes even with --read-only. Reviewed images must declare none.
func (e *DockerExecutor) checkImageVolumes(ctx context.Context, image string) error {
	ctx, cancel := context.WithTimeout(ctx, DockerOperationTimeout)
	defer cancel()
	result, err := e.runner.Run(ctx, "", "docker", "image", "inspect", "--format", "{{json .Config.Volumes}}", image)
	if err != nil {
		return sanitizedOperationError("inspect reviewed image", err)
	}
	if err := dockerCommandError("inspect reviewed image", result); err != nil {
		return err
	}
	var volumes map[string]json.RawMessage
	if err := json.Unmarshal([]byte(result.Stdout), &volumes); err != nil {
		return fmt.Errorf("invalid reviewed image volume metadata")
	}
	if len(volumes) != 0 {
		return fmt.Errorf("reviewed execution images must not declare Dockerfile volumes")
	}
	return nil
}

func (e *DockerExecutor) waitForSandboxReady(ctx context.Context, sandbox *Sandbox) error {
	readyCtx, cancel := context.WithTimeout(ctx, sandboxReadyTimeout)
	defer cancel()
	for {
		if err := e.operation(readyCtx, "check sandbox readiness", "", "exec", sandbox.ContainerName, "true"); err == nil {
			return nil
		}
		timer := time.NewTimer(sandboxReadyPoll)
		select {
		case <-readyCtx.Done():
			timer.Stop()
			return fmt.Errorf("sandbox readiness exceeded time limit; check reviewed runtime image")
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

// Both container kinds retain Docker's default seccomp/device restrictions.
// Never request host namespaces, devices, extra capabilities or privileged mode.
func restrictedContainerArgs() []string {
	return []string{
		"--pull", "never",
		"--network", "none",
		"--ipc", "private",
		"--cgroupns", "private",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true",
		"--read-only",
		"--log-driver", "none",
		"--no-healthcheck",
		"--cpus", "1",
		"--ulimit", "nofile=1024:1024",
		"--ulimit", "core=0:0",
		"--ulimit", "fsize=33554432:33554432",
		"--entrypoint", "/bin/sh",
	}
}

func (e *DockerExecutor) Compile(ctx context.Context, workspace string, spec languages.Spec, limits Limits) (*runner.RunResult, error) {
	if err := validateSourceWorkspace(workspace, spec); err != nil {
		return nil, err
	}
	image := spec.CompileImage()
	if override, ok := e.compileImages[spec.Name()]; ok {
		image = override
	}
	if err := ValidateDockerImageReference(image); err != nil {
		return nil, fmt.Errorf("invalid compiler image reference")
	}
	if !artifactAllowed(spec.Name(), "main") && spec.Name() != "java" {
		return nil, fmt.Errorf("unsupported compiler artifact contract")
	}
	if limits.MemoryLimitMb <= 0 || limits.MemoryLimitMb > MaxMemoryLimitMb {
		return nil, fmt.Errorf("invalid compile memory limit")
	}
	workspace, err := filepath.Abs(workspace)
	if err != nil || strings.ContainsAny(workspace, ",\n\r") {
		return nil, fmt.Errorf("invalid source mount path")
	}
	ctxCompile, cancel := context.WithTimeout(ctx, CompileTimeout)
	defer cancel()
	if err := e.checkImageVolumes(ctxCompile, image); err != nil {
		return nil, err
	}
	container := fmt.Sprintf("yexjudge-compile-%d", time.Now().UnixNano())
	defer e.RemoveSandbox(&Sandbox{ContainerName: container})
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		uid, gid = 10001, 10001
	}
	memory := limits.MemoryLimitMb
	if memory < MinCompileMemoryLimitMb {
		memory = MinCompileMemoryLimitMb
	}
	args := append([]string{"run", "-d", "--name", container}, restrictedContainerArgs()...)
	args = append(args,
		"--memory", fmt.Sprintf("%dm", memory), "--memory-swap", fmt.Sprintf("%dm", memory),
		"--pids-limit", "128", "--user", compileContainerUser(),
		"--tmpfs", fmt.Sprintf("/workspace:rw,exec,nosuid,nodev,size=64m,mode=700,uid=%d,gid=%d", uid, gid),
		"--tmpfs", "/tmp:rw,exec,nosuid,nodev,size=128m,mode=1777",
		"--env", "HOME=/tmp", "--env", "GOCACHE=/tmp/go-build", "--env", "GOMODCACHE=/tmp/go-mod",
		"--workdir", "/workspace",
		"--mount", "type=bind,src="+workspace+",dst=/source,readonly,bind-propagation=rprivate",
		image, "-c", "exec sleep infinity")
	if err := e.operation(ctxCompile, "start compiler container", "", args...); err != nil {
		return nil, err
	}
	compileArgs := []string{"exec", container, "/bin/sh", "-c",
		`cp -- "/source/$1" "/workspace/$1" || exit 125; shift; exec "$@"`,
		"yexjudge-compile", spec.SourceFileName()}
	compileArgs = append(compileArgs, spec.CompileCommand()...)
	result, err := e.runner.Run(ctxCompile, "", "docker", compileArgs...)
	if err != nil {
		return nil, sanitizedOperationError("compile execution", err)
	}
	if result == nil {
		return nil, fmt.Errorf("compile execution returned no result")
	}
	if result.ExitCode != 0 || result.TimedOut || result.OutputLimitExceeded || ctxCompile.Err() != nil {
		return result, nil // bounded compiler diagnostics remain an authorized result, never an infra error
	}
	// PID 1 is the trusted sleep process. kill(-1) excludes PID 1 and the
	// caller, eliminating same-UID compiler descendants before artifact export.
	if err := e.operation(ctxCompile, "stop compiler descendants", "", "exec", container, "/bin/sh", "-c", "kill -9 -1"); err != nil {
		return nil, err
	}
	archive, err := os.CreateTemp("", "yexjudge-artifacts-*")
	if err != nil {
		return nil, fmt.Errorf("create artifact spool failed")
	}
	defer func() { _ = archive.Close(); _ = os.Remove(archive.Name()) }()
	exportArgs := []string{"exec", container, "/bin/sh", "-c", artifactExportScript, "yexjudge-export", spec.Name()}
	var exportResult *runner.RunResult
	if streaming, ok := e.runner.(runner.OutputRunner); ok {
		exportResult, err = streaming.RunWithOutput(ctxCompile, "", archive, MaxArtifactArchiveBytes, "docker", exportArgs...)
	} else {
		// Small-artifact compatibility for existing Runner implementations; their
		// output cap must not be raised to accommodate binary artifacts.
		exportResult, err = e.runner.Run(ctxCompile, "", "docker", exportArgs...)
		if err == nil && exportResult != nil && len(exportResult.Stdout) <= runner.DefaultOutputLimitBytes {
			_, err = io.WriteString(archive, exportResult.Stdout)
		} else if err == nil {
			err = fmt.Errorf("artifact streaming runner required")
		}
	}
	if err != nil {
		return nil, sanitizedOperationError("export compiler artifacts", err)
	}
	if err := dockerCommandError("export compiler artifacts", exportResult); err != nil {
		return nil, err
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("read artifact spool failed")
	}
	if err := importArtifacts(workspace, spec.Name(), archive); err != nil {
		return nil, err
	}
	return result, nil
}

// Uses only utilities present in all default compiler images (including
// BusyBox). No dereferencing, recursion, source files or user-supplied paths.
// Host validation independently distrusts every archive header and byte count.
const artifactExportScript = `set -eu
PATH=/usr/bin:/bin
export PATH
cd /workspace
case "$1" in
  c|cpp|go) set -- main ;;
  java) set -- *.class ;;
  *) exit 20 ;;
esac
[ "$#" -le 128 ] || exit 21
total=0
for f do
  [ -f "$f" ] && [ ! -L "$f" ] || exit 22
  [ "$(stat -c %h -- "$f")" = 1 ] || exit 23
  size=$(stat -c %s -- "$f")
  [ "$size" -gt 0 ] && [ "$size" -le 33554432 ] || exit 24
  total=$((total + size))
  [ "$total" -le 33554432 ] || exit 24
done
exec tar -cf - -- "$@"
`

func (e *DockerExecutor) StartSandbox(ctx context.Context) (*Sandbox, error) {
	ctx, cancel := context.WithTimeout(ctx, DockerOperationTimeout)
	defer cancel()
	if err := e.checkImageVolumes(ctx, e.runtimeImage); err != nil {
		return nil, err
	}
	sandbox := &Sandbox{ContainerName: fmt.Sprintf("yexjudge-%d", time.Now().UnixNano())}
	args := append([]string{"run", "-d", "--name", sandbox.ContainerName}, restrictedContainerArgs()...)
	args = append(args,
		"--memory", fmt.Sprintf("%dm", MaxMemoryLimitMb), "--memory-swap", fmt.Sprintf("%dm", MaxMemoryLimitMb),
		"--pids-limit", "64", "--user", "10001:10001",
		"--tmpfs", "/workspace:rw,exec,nosuid,nodev,size=64m,mode=700,uid=10001,gid=10001",
		"--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=16m,mode=700,uid=10001,gid=10001",
		"--env", "HOME=/tmp", "--workdir", "/workspace", e.runtimeImage, "-c", "exec sleep infinity")
	if err := e.operation(ctx, "start sandbox", "", args...); err != nil {
		e.RemoveSandbox(sandbox)
		return nil, err
	}
	if err := e.waitForSandboxReady(ctx, sandbox); err != nil {
		e.RemoveSandbox(sandbox)
		return nil, err
	}
	return sandbox, nil
}

func (e *DockerExecutor) ConfigureSandbox(ctx context.Context, sandbox *Sandbox, limits Limits) error {
	if limits.MemoryLimitMb <= 0 || limits.MemoryLimitMb > MaxMemoryLimitMb {
		return fmt.Errorf("invalid runtime memory limit")
	}
	memory := fmt.Sprintf("%dm", limits.MemoryLimitMb)
	return e.operation(ctx, "configure sandbox", "", "update", "--memory", memory, "--memory-swap", memory,
		"--cpus", "1", "--pids-limit", "64", sandbox.ContainerName)
}

func (e *DockerExecutor) stageArchive(ctx context.Context, sandbox *Sandbox, archive string) error {
	return e.operation(ctx, "stage sandbox artifacts", archive, "exec", "-i", sandbox.ContainerName,
		"tar", "-xf", "-", "-C", "/workspace")
}

func (e *DockerExecutor) PrepareSandbox(ctx context.Context, sandbox *Sandbox, workspace string) error {
	e.staged.Delete(sandbox.ContainerName)
	archive, err := runtimeWorkspaceArchive(workspace)
	if err != nil {
		return err
	}
	// Restart first: never extract even our trusted archive into a directory
	// whose symlinks, files or processes may have been left by a previous job.
	if err := e.ResetSandbox(ctx, sandbox); err != nil {
		sandbox.needsReplace = true
		return err
	}
	if err := e.stageArchive(ctx, sandbox, archive); err != nil {
		sandbox.needsReplace = true
		return err
	}
	e.staged.Store(sandbox.ContainerName, stagedWorkspace{archive: archive, deadline: time.Now().Add(MaxJobExecutionTime)})
	sandbox.restarted = false
	return nil
}

func (e *DockerExecutor) restartSandbox(ctx context.Context, sandbox *Sandbox) error {
	ctx, cancel := context.WithTimeout(ctx, DockerOperationTimeout)
	defer cancel()
	if err := e.operation(ctx, "reset sandbox", "", "restart", "-t", "0", sandbox.ContainerName); err != nil {
		return err
	}
	return e.waitForSandboxReady(ctx, sandbox)
}

func (e *DockerExecutor) ResetSandbox(ctx context.Context, sandbox *Sandbox) error {
	e.staged.Delete(sandbox.ContainerName)
	return e.restartSandbox(ctx, sandbox)
}

func (e *DockerExecutor) RemoveSandbox(sandbox *Sandbox) {
	if sandbox == nil {
		return
	}
	e.staged.Delete(sandbox.ContainerName)
	ctx, cancel := context.WithTimeout(context.Background(), DockerCleanupTimeout)
	defer cancel()
	_ = e.operation(ctx, "remove sandbox", "", "rm", "-f", sandbox.ContainerName)
}

func (e *DockerExecutor) RunTestCase(ctx context.Context, sandbox *Sandbox, input string, spec languages.Spec) (*runner.RunResult, error) {
	if len(input) > MaxTestCaseBytes {
		return nil, fmt.Errorf("testcase input exceeds limit")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(MaxTimeLimitMs)*time.Millisecond)
	defer cancel()
	var snapshot *stagedWorkspace
	if value, ok := e.staged.Load(sandbox.ContainerName); ok {
		staged := value.(stagedWorkspace)
		snapshot = &staged
		jobCtx, jobCancel := context.WithDeadline(ctx, staged.deadline)
		defer jobCancel()
		ctx = jobCtx
		if time.Until(staged.deadline) <= 0 {
			return nil, fmt.Errorf("job execution budget exhausted")
		}

	}
	sandbox.restarted = false
	marker := fmt.Sprintf("__YEXJUDGE_MAX_RSS_KB_%d:", time.Now().UnixNano())
	args := []string{"exec", "-i", sandbox.ContainerName, "/usr/bin/time", "-f", marker + "%M\\n", "--"}
	args = append(args, spec.RunCommand()...)
	result, runErr := e.runner.Run(ctx, input+"\n", "docker", args...)
	// Cleanup/staging time must not turn a completed program into a timeout.
	timedOut := ctx.Err() == context.DeadlineExceeded
	// Even a successful parent can leave a forked child behind or replace the
	// next testcase's executable. Restart every time, including runner failures;
	// tmpfs is remounted empty, then the trusted snapshot is restored now so
	// staging does not consume the next testcase's execution time limit.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), DockerCleanupTimeout)
	defer cleanupCancel()
	if err := e.restartSandbox(cleanupCtx, sandbox); err != nil {
		sandbox.needsReplace = true
		return nil, err
	}
	if snapshot != nil {
		if err := e.stageArchive(cleanupCtx, sandbox, snapshot.archive); err != nil {
			sandbox.needsReplace = true
			return nil, err
		}
	}
	sandbox.restarted = true
	if runErr != nil {
		return nil, sanitizedOperationError("run testcase", runErr)
	}
	if result == nil {
		return nil, fmt.Errorf("run testcase returned no result")
	}
	result.Stderr, result.MemoryUsed = extractMeasuredMemory(result.Stderr, marker)
	if timedOut {
		result.TimedOut = true
	}
	return result, nil
}

func extractMeasuredMemory(stderr, marker string) (string, int64) {
	index := strings.LastIndex(stderr, marker)
	if index < 0 {
		return stderr, 0
	}
	output := stderr[index+len(marker):]
	if newline := strings.IndexByte(output, '\n'); newline >= 0 {
		output = output[:newline]
	}
	memoryKB, err := strconv.ParseInt(strings.TrimSpace(output), 10, 64)
	if err != nil || memoryKB < 0 || memoryKB > (1<<63-1)/1024 {
		return stderr, 0
	}
	return stderr[:index], memoryKB * 1024
}
