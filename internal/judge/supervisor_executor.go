package judge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"yexjudge/internal/judge/languages"
	"yexjudge/internal/runner"
)

type testcaseSupervisorExecutor interface {
	RunTestCases(ctx context.Context, sandbox *Sandbox, inputs []string, spec languages.Spec, timeLimit time.Duration) ([]*runner.RunResult, error)
}

type supervisorRequest struct {
	Command     []string `json:"command"`
	Inputs      []string `json:"inputs"`
	TimeLimitMs int64    `json:"timeLimitMs"`
	OutputLimit int      `json:"outputLimit"`
}

type supervisorResult struct {
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	ExitCode    int    `json:"exitCode"`
	CPUMs       int64  `json:"cpuMs"`
	MaxRssKb    int64  `json:"maxRssKb"`
	TimedOut    bool   `json:"timedOut"`
	OutputLimit bool   `json:"outputLimit"`
}

type cappedCommandBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *cappedCommandBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = b.Buffer.Write(p[:remaining])
		} else {
			_, _ = b.Buffer.Write(p)
		}
	}
	if len(p) > remaining {
		b.exceeded = true
	}
	return len(p), nil
}

func (e *DockerExecutor) RunTestCases(ctx context.Context, sandbox *Sandbox, inputs []string, spec languages.Spec, timeLimit time.Duration) ([]*runner.RunResult, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	if timeLimit <= 0 || timeLimit.Milliseconds() == 0 {
		return nil, fmt.Errorf("testcase time limit must be at least one millisecond")
	}
	if spec == nil || len(spec.RunCommand()) == 0 {
		return nil, fmt.Errorf("supervisor testcase command is empty")
	}
	command, err := json.Marshal(supervisorRequest{Command: spec.RunCommand(), Inputs: inputs, TimeLimitMs: timeLimit.Milliseconds(), OutputLimit: runner.DefaultOutputLimitBytes})
	if err != nil {
		return nil, fmt.Errorf("encode supervisor request: %w", err)
	}
	// JSON may expand each control byte to a six-byte \u00XX escape.
	transportLimit := (len(inputs) + 1) * (12*runner.DefaultOutputLimitBytes + 4096)
	args := []string{"exec", "--user", "0:0", "-i", sandbox.ContainerName, "/usr/local/bin/yexsupervisor", "run"}
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = bytes.NewReader(command)
	stdout := &cappedCommandBuffer{limit: transportLimit}
	stderr := &cappedCommandBuffer{limit: runner.DefaultOutputLimitBytes}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err = cmd.Run()
	if ctx.Err() != nil {
		if restartErr := e.restartSandboxAfterInterruptedRun(sandbox); restartErr != nil {
			return nil, restartErr
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("docker supervisor execution canceled: %w", err)
	}
	if stdout.exceeded {
		sandbox.needsReplace = true
		return nil, fmt.Errorf("supervisor output exceeded transport limit")
	}
	if err != nil {
		sandbox.needsReplace = true
		return nil, fmt.Errorf("run supervisor test cases: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	results, err := decodeSupervisorResults(stdout.Bytes(), len(inputs))
	if err != nil {
		sandbox.needsReplace = true
		return nil, err
	}
	return results, nil
}

func decodeSupervisorResults(output []byte, expected int) ([]*runner.RunResult, error) {
	decoder := json.NewDecoder(bytes.NewReader(output))
	results := make([]*runner.RunResult, 0, expected)
	for len(results) < expected {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			if err == io.EOF && len(results) > 0 && (results[len(results)-1].ExitCode != 0 || results[len(results)-1].TimedOut || results[len(results)-1].OutputLimitExceeded) {
				break
			}
			return nil, fmt.Errorf("decode supervisor testcase %d: %w", len(results)+1, err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			return nil, fmt.Errorf("decode supervisor testcase %d: result must be a JSON object", len(results)+1)
		}
		for _, name := range []string{"stdout", "stderr", "exitCode", "cpuMs", "maxRssKb", "timedOut", "outputLimit"} {
			if _, ok := fields[name]; !ok {
				return nil, fmt.Errorf("decode supervisor testcase %d: missing %q field", len(results)+1, name)
			}
		}
		var result supervisorResult
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("decode supervisor testcase %d: %w", len(results)+1, err)
		}
		if result.CPUMs < 0 || result.MaxRssKb < 0 {
			return nil, fmt.Errorf("invalid measurements for supervisor testcase %d", len(results)+1)
		}
		results = append(results, &runner.RunResult{Stdout: result.Stdout, Stderr: result.Stderr, ExitCode: result.ExitCode, TimedOut: result.TimedOut, OutputLimitExceeded: result.OutputLimit, TimeUsed: time.Duration(result.CPUMs) * time.Millisecond, MemoryUsed: result.MaxRssKb * 1024})
		if result.ExitCode != 0 || result.TimedOut || result.OutputLimit {
			break
		}
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("supervisor returned no testcase results")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("supervisor returned trailing data")
	}
	return results, nil
}

func (e *DockerExecutor) restartSandboxAfterInterruptedRun(sandbox *Sandbox) error {
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cleanupCancel()
	restartResult, restartErr := e.runner.Run(cleanupCtx, "", "docker", "restart", "-t", "0", sandbox.ContainerName)
	if restartErr != nil {
		sandbox.needsReplace = true
		return fmt.Errorf("restart sandbox after interrupted supervisor execution: %w", restartErr)
	}
	if err := dockerCommandError("restart sandbox after interrupted supervisor execution", restartResult); err != nil {
		sandbox.needsReplace = true
		return err
	}
	if err := e.waitForSandboxReady(cleanupCtx, sandbox); err != nil {
		sandbox.needsReplace = true
		return err
	}
	sandbox.restarted = true
	return nil
}
