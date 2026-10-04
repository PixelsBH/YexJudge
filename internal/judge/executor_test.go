package judge

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"yexjudge/internal/judge/languages"
	"yexjudge/internal/runner"
)

type executorCall struct {
	command string
	args    []string
	input   string
	bounded bool
}

type recordingRunner struct {
	calls     []executorCall
	result    *runner.RunResult
	resultFor func(executorCall) *runner.RunResult
}

func (r *recordingRunner) Run(ctx context.Context, input string, command string, args ...string) (*runner.RunResult, error) {
	_, bounded := ctx.Deadline()
	call := executorCall{command: command, args: append([]string(nil), args...), input: input, bounded: bounded}
	r.calls = append(r.calls, call)
	if r.resultFor != nil {
		if result := r.resultFor(call); result != nil {
			return result, nil
		}
	}
	if hasExecutorArg(args, "image", "inspect") {
		return &runner.RunResult{Stdout: "null"}, nil
	}
	if strings.Contains(strings.Join(args, " "), "restart") {
		return &runner.RunResult{ExitCode: 0}, nil
	}
	if r.result != nil && hasExecutorArg(args, "/usr/bin/time") {
		copy := *r.result
		return &copy, nil
	}
	return &runner.RunResult{ExitCode: 0}, nil
}

func (r *recordingRunner) RunWithOutput(ctx context.Context, input string, output io.Writer, limit int64, command string, args ...string) (*runner.RunResult, error) {
	result, err := r.Run(ctx, input, command, args...)
	if err == nil {
		if int64(len(result.Stdout)) > limit {
			return &runner.RunResult{OutputLimitExceeded: true}, nil
		}
		_, err = io.WriteString(output, result.Stdout)
	}
	return result, err
}

func hasExecutorArg(args []string, want ...string) bool {
	for i := 0; i+len(want) <= len(args); i++ {
		match := true
		for j := range want {
			if args[i+j] != want[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func TestDockerExecutorCompileUsesRestrictedContainer(t *testing.T) {
	workspace, err := createWorkspace(Job{SourceCode: "int main() {}"}, languages.Cpp{})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workspace)
	recorder := &recordingRunner{resultFor: func(call executorCall) *runner.RunResult {
		if hasExecutorArg(call.args, artifactExportScript) {
			return &runner.RunResult{Stdout: testArtifactArchive(t, &testArtifact{name: "main", data: "binary"})}
		}
		return nil
	}}
	executor := NewDockerExecutor(recorder)

	result, err := executor.Compile(
		context.Background(),
		workspace,
		languages.Cpp{},
		Limits{TimeLimitMs: 1000, MemoryLimitMb: 128},
	)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("Compile() result = %+v, want success", result)
	}
	if len(recorder.calls) != 6 {
		t.Fatalf("docker calls = %d, want image check, start, compile, quiesce, export and cleanup", len(recorder.calls))
	}
	args := recorder.calls[1].args
	for _, required := range [][]string{
		{"--network", "none"},
		{"--memory", "512m"},
		{"--memory-swap", "512m"},
		{"--pids-limit", "128"},
		{"--cap-drop", "ALL"},
		{"--security-opt", "no-new-privileges:true"},
		{"--pull", "never"},
		{"--log-driver", "none"},
		{"--no-healthcheck"},
		{"--ipc", "private"},
		{"--cgroupns", "private"},
		{"--mount", "type=bind,src=" + workspace + ",dst=/source,readonly,bind-propagation=rprivate"},
		{"--read-only"},
		{"--user", compileContainerUser()},
		{"--workdir", "/workspace"},
		{"--ulimit", "nofile=1024:1024"},
	} {
		if !hasExecutorArg(args, required...) {
			t.Errorf("compile args missing %q: %v", required, args)
		}
	}
}

func TestDockerExecutorRestartsSandboxAfterOutputLimit(t *testing.T) {
	recorder := &recordingRunner{result: &runner.RunResult{
		OutputLimitExceeded: true,
	}}
	executor := NewDockerExecutor(recorder)

	sandbox := &Sandbox{ContainerName: "sandbox"}
	result, err := executor.RunTestCase(
		context.Background(),
		sandbox,
		"input",
		languages.Python{},
	)
	if err != nil {
		t.Fatalf("RunTestCase() error = %v", err)
	}
	if !result.OutputLimitExceeded {
		t.Fatal("RunTestCase() lost output-limit result")
	}
	if !sandbox.restarted {
		t.Fatal("RunTestCase() did not mark the restarted sandbox for pool reuse")
	}
	if len(recorder.calls) != 3 || !hasExecutorArg(recorder.calls[1].args, "restart", "-t", "0", "sandbox") || !hasExecutorArg(recorder.calls[2].args, "exec", "sandbox", "true") {
		t.Fatalf("docker calls = %+v, want exec, restart, and readiness check", recorder.calls)
	}
}

func TestDockerExecutorMeasuresPeakResidentMemory(t *testing.T) {
	recorder := &recordingRunner{resultFor: func(call executorCall) *runner.RunResult {
		for i, arg := range call.args {
			if arg != "-f" || i+1 >= len(call.args) {
				continue
			}
			format := call.args[i+1]
			markerStart := strings.Index(format, "__YEXJUDGE_MAX_RSS_KB_")
			if markerStart < 0 {
				continue
			}
			markerLength := strings.Index(format[markerStart:], "%M")
			if markerLength < 0 {
				continue
			}
			marker := format[markerStart : markerStart+markerLength]
			return &runner.RunResult{ExitCode: 0, Stderr: "program warning" + marker + "256\n"}
		}
		return nil
	}}

	result, err := NewDockerExecutor(recorder).RunTestCase(
		context.Background(),
		&Sandbox{ContainerName: "sandbox"},
		"input",
		languages.Python{},
	)
	if err != nil {
		t.Fatalf("RunTestCase() error = %v", err)
	}
	if result.MemoryUsed != 256*1024 {
		t.Fatalf("MemoryUsed = %d bytes, want 256 KiB", result.MemoryUsed)
	}
	if result.Stderr != "program warning" {
		t.Fatalf("Stderr = %q, want program stderr without the time marker", result.Stderr)
	}
	if len(recorder.calls) != 3 || !hasExecutorArg(recorder.calls[0].args, "exec", "-i", "sandbox", "/usr/bin/time", "-f") {
		t.Fatalf("docker calls = %+v, want execution wrapped by GNU time", recorder.calls)
	}
}

func TestDockerExecutorMarksDeadlineAndRestartsSandbox(t *testing.T) {
	recorder := &recordingRunner{result: &runner.RunResult{}}
	executor := NewDockerExecutor(recorder)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Millisecond))
	defer cancel()

	result, err := executor.RunTestCase(ctx, &Sandbox{ContainerName: "sandbox"}, "", languages.Python{})
	if err != nil {
		t.Fatalf("RunTestCase() error = %v", err)
	}
	if !result.TimedOut {
		t.Fatal("RunTestCase() did not mark a deadline result as timed out")
	}
	if len(recorder.calls) != 3 {
		t.Fatalf("docker calls = %d, want exec, restart, and readiness check", len(recorder.calls))
	}
}

func TestDockerExecutorRejectsNonzeroLifecycleExitCodes(t *testing.T) {
	tests := []struct {
		name string
		call func(*DockerExecutor) error
	}{
		{
			name: "start",
			call: func(executor *DockerExecutor) error {
				_, err := executor.StartSandbox(context.Background())
				return err
			},
		},
		{
			name: "update",
			call: func(executor *DockerExecutor) error {
				return executor.ConfigureSandbox(context.Background(), &Sandbox{ContainerName: "sandbox"}, Limits{MemoryLimitMb: 128})
			},
		},
		{
			name: "restart",
			call: func(executor *DockerExecutor) error {
				return executor.ResetSandbox(context.Background(), &Sandbox{ContainerName: "sandbox"})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := &recordingRunner{resultFor: func(call executorCall) *runner.RunResult {
				if call.args[0] == "run" || call.args[0] == "update" || call.args[0] == "restart" {
					return &runner.RunResult{ExitCode: 17, Stderr: strings.Repeat("diagnostic ", 10000)}
				}
				return nil
			}}
			err := test.call(NewDockerExecutor(recorder))
			if err == nil {
				t.Fatal("lifecycle command unexpectedly succeeded")
			}
			if !strings.Contains(err.Error(), "exited with code 17") {
				t.Fatalf("error = %v, want exit-code diagnostic", err)
			}
			if strings.Contains(err.Error(), "diagnostic") {
				t.Fatalf("error leaked container stderr: %v", err)
			}
		})
	}
}

func TestDockerExecutorRetriesSandboxReadiness(t *testing.T) {
	readinessChecks := 0
	recorder := &recordingRunner{resultFor: func(call executorCall) *runner.RunResult {
		if hasExecutorArg(call.args, "exec", "sandbox", "true") {
			readinessChecks++
			if readinessChecks == 1 {
				return &runner.RunResult{ExitCode: 1, Stderr: "container is starting"}
			}
		}
		return nil
	}}

	if err := NewDockerExecutor(recorder).ResetSandbox(context.Background(), &Sandbox{ContainerName: "sandbox"}); err != nil {
		t.Fatalf("ResetSandbox() error = %v", err)
	}
	if readinessChecks != 2 {
		t.Fatalf("readiness checks = %d, want 2", readinessChecks)
	}
}
