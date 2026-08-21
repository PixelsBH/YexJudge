package runner

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDockerRunnerBoundsCapturedOutput(t *testing.T) {
	runner := &DockerRunner{}
	result, err := runner.Run(
		context.Background(),
		"",
		"sh",
		"-c",
		"head -c 100000 /dev/zero",
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.OutputLimitExceeded {
		t.Fatal("Run() did not report output limit exceeded")
	}
	if len(result.Stdout) != DefaultOutputLimitBytes {
		t.Fatalf("captured stdout length = %d, want %d", len(result.Stdout), DefaultOutputLimitBytes)
	}
}

func TestDockerRunnerStopsAProcessThatExceedsOutputLimit(t *testing.T) {
	runner := &DockerRunner{}
	started := time.Now()
	result, err := runner.Run(
		context.Background(),
		"",
		"sh",
		"-c",
		"while :; do printf 0123456789; done",
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.OutputLimitExceeded {
		t.Fatal("Run() did not report output limit exceeded")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Run() took %s after output overflow, want it to stop promptly", elapsed)
	}
}

func TestDockerRunnerPreservesOutputBelowLimit(t *testing.T) {
	const output = "hello from the runner"
	runner := &DockerRunner{}
	result, err := runner.Run(context.Background(), output, "sh", "-c", "cat")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.OutputLimitExceeded {
		t.Fatal("Run() reported an output limit for small output")
	}
	if strings.TrimSpace(result.Stdout) != output {
		t.Fatalf("stdout = %q, want %q", result.Stdout, output)
	}
}
