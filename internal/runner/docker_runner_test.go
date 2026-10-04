package runner

import (
	"bytes"
	"context"
	"errors"
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

func TestDockerRunnerBoundsStderr(t *testing.T) {
	result, err := (&DockerRunner{}).Run(context.Background(), "", "sh", "-c", "while :; do printf diagnostic >&2; done")
	if err != nil {
		t.Fatal(err)
	}
	if !result.OutputLimitExceeded || len(result.Stderr) != DefaultOutputLimitBytes {
		t.Fatalf("stderr bound not enforced: %+v", result)
	}
}

func TestDockerRunnerStreamsBoundedArtifacts(t *testing.T) {
	var output bytes.Buffer
	const limit = 128 * 1024
	result, err := (&DockerRunner{}).RunWithOutput(context.Background(), "", &output, limit, "sh", "-c", "while :; do printf 0123456789; done")
	if err != nil {
		t.Fatal(err)
	}
	if !result.OutputLimitExceeded || output.Len() != limit || result.Stdout != "" {
		t.Fatalf("stream bound not enforced: length=%d result=%+v", output.Len(), result)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("SECRET_WRITER_DATA") }

func TestDockerRunnerRedactsErrors(t *testing.T) {
	_, err := (&DockerRunner{}).Run(context.Background(), "", "/SECRET_SOURCE/nonexistent")
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("start error leaked: %v", err)
	}
	_, err = (&DockerRunner{}).RunWithOutput(context.Background(), "", failingWriter{}, 1024, "sh", "-c", "printf output")
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("writer error leaked: %v", err)
	}
}

func TestDockerRunnerBoundsTimeoutWithDescendantHoldingOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	result, err := (&DockerRunner{}).Run(ctx, "", "sh", "-c", "sleep 30 & wait")
	if err != nil {
		t.Fatal(err)
	}
	if !result.TimedOut || result.ExitCode == 0 {
		t.Fatalf("timeout incorrectly successful: %+v", result)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("descendant held runner open")
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
