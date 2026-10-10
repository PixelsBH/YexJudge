package judge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yexjudge/internal/judge/languages"
	"yexjudge/internal/runner"
)

func TestDockerCompilerArtifactWithLargeAssemblySectionCanBeStaged(t *testing.T) {
	if os.Getenv("YEXJUDGE_DOCKER_TESTS") != "1" {
		t.Skip("opt-in Docker integration test; set YEXJUDGE_DOCKER_TESTS=1 with compiler/runtime images and a reachable Docker daemon")
	}

	dockerRunner := &runner.DockerRunner{}
	for _, image := range []string{RuntimeSandboxImage, (languages.Cpp{}).CompileImage()} {
		checkCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		result, err := dockerRunner.Run(checkCtx, "", "docker", "image", "inspect", image)
		cancel()
		if err != nil || result == nil || result.ExitCode != 0 {
			t.Skipf("required Docker image %q is unavailable", image)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	workspace, err := os.MkdirTemp("", "yexjudge-large-assembly-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workspace)
	source := `asm(".pushsection .audit\n.fill 16777216,1,0\n.popsection"); int main(){return 0;}`
	if err := os.WriteFile(filepath.Join(workspace, "main.cpp"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}

	executor := NewDockerExecutor(dockerRunner)
	compileResult, err := executor.Compile(ctx, workspace, languages.Cpp{}, Limits{TimeLimitMs: 1000, MemoryLimitMb: 128})
	if err != nil || compileResult == nil || compileResult.ExitCode != 0 {
		t.Fatalf("Compile() result = %+v, error = %v", compileResult, err)
	}
	sandbox, err := executor.StartSandbox(ctx)
	if err != nil {
		t.Fatalf("StartSandbox() error = %v", err)
	}
	defer executor.RemoveSandbox(sandbox)
	if err := executor.PrepareSandbox(ctx, sandbox, workspace); err != nil {
		t.Fatalf("PrepareSandbox() error = %v", err)
	}
	runResult, err := executor.RunTestCase(ctx, sandbox, "", languages.Cpp{})
	if err != nil || runResult == nil || runResult.ExitCode != 0 {
		t.Fatalf("RunTestCase() result = %+v, error = %v", runResult, err)
	}
}

func TestDockerTestcasesStartFromCleanSnapshotAndProcessTree(t *testing.T) {
	if os.Getenv("YEXJUDGE_DOCKER_TESTS") != "1" {
		t.Skip("opt-in Docker integration test; set YEXJUDGE_DOCKER_TESTS=1 with the runtime image and a reachable Docker daemon")
	}

	dockerRunner := &runner.DockerRunner{}
	checkCtx, cancelCheck := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelCheck()
	imageResult, err := dockerRunner.Run(checkCtx, "", "docker", "image", "inspect", RuntimeSandboxImage)
	if err != nil {
		t.Skipf("Docker is unavailable or the runtime image cannot be inspected: %v", err)
	}
	if imageResult == nil {
		t.Skip("Docker image inspection returned no result")
	}
	if imageResult.ExitCode != 0 {
		t.Skipf("Docker runtime image %q is unavailable: %s", RuntimeSandboxImage, strings.TrimSpace(imageResult.Stderr))
	}

	executor := NewDockerExecutor(dockerRunner)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sandbox, err := executor.StartSandbox(ctx)
	if err != nil {
		t.Fatalf("Docker daemon could not start the runtime sandbox: %v", err)
	}
	defer executor.RemoveSandbox(sandbox)
	if err := executor.ConfigureSandbox(ctx, sandbox, Limits{MemoryLimitMb: 128}); err != nil {
		t.Fatalf("ConfigureSandbox() error = %v", err)
	}

	job := Job{
		Language: "python",
		SourceCode: `import os
import subprocess
import sys
import time

mode = sys.stdin.readline().strip()
if mode == "dirty":
    with open("/workspace/dirty", "w") as marker:
        marker.write("left by testcase one")
    subprocess.Popen(
        ["sh", "-c", "sleep 0.5; printf child > /workspace/background"],
        stdin=subprocess.DEVNULL,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        start_new_session=True,
    )
    print("started")
else:
    time.sleep(0.8)
    print(f"{os.path.exists('/workspace/dirty')} {os.path.exists('/workspace/background')}")
`,
		TestCases: []TestCase{
			{ID: 1, Input: "dirty", ExpectedOutput: "started"},
			{ID: 2, Input: "clean", ExpectedOutput: "False False"},
		},
		Limits: Limits{TimeLimitMs: 5000, MemoryLimitMb: 128},
	}
	workspace, err := createWorkspace(job, languages.Python{})
	if err != nil {
		t.Fatalf("createWorkspace() error = %v", err)
	}
	defer os.RemoveAll(workspace)
	if err := executor.PrepareSandbox(ctx, sandbox, workspace); err != nil {
		t.Fatalf("PrepareSandbox() error = %v", err)
	}

	result, err := runTestCases(ctx, executor, sandbox, workspace, job, languages.Python{})
	if err != nil {
		t.Fatalf("runTestCases() error = %v", err)
	}
	if result.Status != Accepted || result.PassedTestCases != 2 {
		t.Fatalf("result = %+v, want both cases accepted with no leaked filesystem or child process state", result)
	}
	if result.FailedTestCase != nil || strings.TrimSpace(result.ErrorMessage) != "" {
		t.Fatalf("unexpected failed testcase or error: %+v", result)
	}
}
