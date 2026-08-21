package judge

import (
	"context"
	"strings"
	"testing"
	"time"

	"yexjudge/internal/judge/languages"
	"yexjudge/internal/runner"
)

type executorCall struct {
	command string
	args    []string
}

type recordingRunner struct {
	calls     []executorCall
	result    *runner.RunResult
	resultFor func(executorCall) *runner.RunResult
}

func (r *recordingRunner) Run(_ context.Context, _ string, command string, args ...string) (*runner.RunResult, error) {
	call := executorCall{command: command, args: append([]string(nil), args...)}
	r.calls = append(r.calls, call)
	if r.resultFor != nil {
		if result := r.resultFor(call); result != nil {
			return result, nil
		}
	}
	if strings.Contains(strings.Join(args, " "), "restart") {
		return &runner.RunResult{ExitCode: 0}, nil
	}
	if r.result != nil {
		return r.result, nil
	}
	return &runner.RunResult{ExitCode: 0}, nil
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
	recorder := &recordingRunner{}
	executor := NewDockerExecutor(recorder)

	result, err := executor.Compile(
		context.Background(),
		"/tmp/workspace",
		languages.Cpp{},
		Limits{TimeLimitMs: 1000, MemoryLimitMb: 128},
	)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("Compile() result = %+v, want success", result)
	}
	if len(recorder.calls) != 2 {
		t.Fatalf("docker calls = %d, want compile plus cleanup", len(recorder.calls))
	}
	args := recorder.calls[0].args
	for _, required := range [][]string{
		{"--network", "none"},
		{"--memory", "512m"},
		{"--memory-swap", "512m"},
		{"--pids-limit", "128"},
		{"--cap-drop", "ALL"},
		{"--security-opt", "no-new-privileges"},
		{"--read-only"},
		{"--user", "10001:10001"},
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
			if len(err.Error()) > runner.DefaultOutputLimitBytes+100 {
				t.Fatalf("error diagnostic was not capped: %d bytes", len(err.Error()))
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
