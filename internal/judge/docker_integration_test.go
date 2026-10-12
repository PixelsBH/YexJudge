package judge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestDockerSupervisorRunsTwoHundredFunctionCases(t *testing.T) {
	if os.Getenv("YEXJUDGE_DOCKER_TESTS") != "1" {
		t.Skip("opt-in Docker integration test; set YEXJUDGE_DOCKER_TESTS=1 with compiler/runtime images and a reachable Docker daemon")
	}

	dockerRunner := &runner.DockerRunner{}
	for _, image := range []string{RuntimeSandboxImage, (languages.Cpp{}).CompileImage()} {
		checkCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		imageResult, err := dockerRunner.Run(checkCtx, "", "docker", "image", "inspect", image)
		cancel()
		if err != nil || imageResult == nil || imageResult.ExitCode != 0 {
			t.Skipf("required Docker image %q is unavailable", image)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	job := Job{
		Language: "cpp",
		SourceCode: `class Solution {
public:
    vector<int> twoSum(vector<int>& nums, int target) {
        unordered_map<int, int> seen;
        for (int i = 0; i < static_cast<int>(nums.size()); ++i) {
            int need = target - nums[i];
            if (seen.count(need)) return {seen[need], i};
            seen[nums[i]] = i;
        }
        return {};
    }
};`,
		Function: &FunctionSpec{
			Name:       "twoSum",
			ReturnType: "vector<int>",
			Params: []FunctionParam{
				{Name: "nums", Type: "vector<int>&"},
				{Name: "target", Type: "int"},
			},
		},
		TestCases: make([]TestCase, 200),
		Limits:    Limits{TimeLimitMs: 1000, MemoryLimitMb: 128},
	}
	for i := range job.TestCases {
		job.TestCases[i] = TestCase{
			ID:       i + 1,
			Args:     []json.RawMessage{json.RawMessage(`[2,7,11,15]`), json.RawMessage(`9`)},
			Expected: json.RawMessage(`[0,1]`),
		}
	}

	workspace, err := createWorkspace(job, languages.Cpp{})
	if err != nil {
		t.Fatalf("createWorkspace() error = %v", err)
	}
	defer os.RemoveAll(workspace)
	executor := NewDockerExecutor(dockerRunner)
	compileResult, err := executor.Compile(ctx, workspace, languages.Cpp{}, job.Limits)
	if err != nil || compileResult == nil || compileResult.ExitCode != 0 {
		t.Fatalf("Compile() result = %+v, error = %v", compileResult, err)
	}
	sandbox, err := executor.StartSandbox(ctx)
	if err != nil {
		t.Fatalf("StartSandbox() error = %v", err)
	}
	defer executor.RemoveSandbox(sandbox)
	if err := executor.ConfigureSandbox(ctx, sandbox, job.Limits); err != nil {
		t.Fatalf("ConfigureSandbox() error = %v", err)
	}
	if err := executor.PrepareSandbox(ctx, sandbox, workspace); err != nil {
		t.Fatalf("PrepareSandbox() error = %v", err)
	}

	started := time.Now()
	result, err := runTestCasesWithSandboxReset(ctx, executor, sandbox, workspace, job, languages.Cpp{}, false)
	if err != nil {
		t.Fatalf("runTestCasesWithSandboxReset() error = %v", err)
	}
	t.Logf("200 testcase supervisor execution took %s", time.Since(started))
	if result.Status != Accepted || result.PassedTestCases != 200 || result.TotalTestCases != 200 {
		t.Fatalf("result = %+v, want 200 accepted cases", result)
	}
	if result.RuntimeMs >= 100 {
		t.Fatalf("RuntimeMs = %d, want contestant CPU time below 100ms for trivial cases", result.RuntimeMs)
	}
}

func TestDockerSupervisorSupportsPythonStdin(t *testing.T) {
	if os.Getenv("YEXJUDGE_DOCKER_TESTS") != "1" {
		t.Skip("opt-in Docker integration test; set YEXJUDGE_DOCKER_TESTS=1 with the runtime image and a reachable Docker daemon")
	}
	dockerRunner := &runner.DockerRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	job := Job{
		Language: "python",
		SourceCode: `import ctypes, errno, os, sys
value = sys.stdin.readline().strip()
if value == 'alpha':
    libc = ctypes.CDLL(None, use_errno=True)
    for entry in os.listdir('/proc'):
        if not entry.isdigit() or int(entry) == os.getpid():
            continue
        try:
            lines = open('/proc/' + entry + '/status').read().splitlines()
        except OSError:
            continue
        uid_line = next((line for line in lines if line.startswith('Uid:')), '')
        fields = uid_line.split()
        if len(fields) < 3 or fields[2] == '10001':
            continue
        ctypes.set_errno(0)
        if libc.ptrace(16, int(entry), None, None) == 0:
            libc.ptrace(17, int(entry), None, None)
            raise RuntimeError('contestant attached to the root supervisor')
        if ctypes.get_errno() not in (errno.EPERM, errno.ESRCH):
            raise RuntimeError('unexpected ptrace error')
print(value + ':' + str(len(value)))
`,
		TestCases: []TestCase{
			{ID: 1, Input: "alpha\n", ExpectedOutput: "alpha:5"},
			{ID: 2, Input: "second case\n", ExpectedOutput: "second case:11"},
		},
		Limits: Limits{TimeLimitMs: 1000, MemoryLimitMb: 128},
	}
	workspace, err := createWorkspace(job, languages.Python{})
	if err != nil {
		t.Fatalf("createWorkspace() error = %v", err)
	}
	defer os.RemoveAll(workspace)
	executor := NewDockerExecutor(dockerRunner)
	sandbox, err := executor.StartSandbox(ctx)
	if err != nil {
		t.Fatalf("StartSandbox() error = %v", err)
	}
	defer executor.RemoveSandbox(sandbox)
	if err := executor.ConfigureSandbox(ctx, sandbox, job.Limits); err != nil {
		t.Fatalf("ConfigureSandbox() error = %v", err)
	}
	if err := executor.PrepareSandbox(ctx, sandbox, workspace); err != nil {
		t.Fatalf("PrepareSandbox() error = %v", err)
	}
	result, err := runTestCasesWithSandboxReset(ctx, executor, sandbox, workspace, job, languages.Python{}, false)
	if err != nil || result.Status != Accepted || result.PassedTestCases != 2 {
		t.Fatalf("result = %+v, error = %v, want accepted Python stdin cases", result, err)
	}
	sleepJob := Job{Language: "python", SourceCode: "import time\ntime.sleep(5)\n", TestCases: []TestCase{{ID: 1, Input: "", ExpectedOutput: ""}}, Limits: Limits{TimeLimitMs: 100, MemoryLimitMb: 128}}
	sleepWorkspace, err := createWorkspace(sleepJob, languages.Python{})
	if err != nil {
		t.Fatalf("create sleep workspace: %v", err)
	}
	defer os.RemoveAll(sleepWorkspace)
	if err := executor.PrepareSandbox(ctx, sandbox, sleepWorkspace); err != nil {
		t.Fatalf("PrepareSandbox(sleep): %v", err)
	}
	result, err = runTestCasesWithSandboxReset(ctx, executor, sandbox, sleepWorkspace, sleepJob, languages.Python{}, false)
	if err != nil || result.Status != TimeLimitExceeded {
		t.Fatalf("sleep result = %+v, error = %v, want blocking wall timeout", result, err)
	}
}

func TestDockerSupervisorEnforcesTimeoutOutputLimitAndCancellation(t *testing.T) {
	if os.Getenv("YEXJUDGE_DOCKER_TESTS") != "1" {
		t.Skip("opt-in Docker integration test; set YEXJUDGE_DOCKER_TESTS=1 with compiler/runtime images and a reachable Docker daemon")
	}

	dockerRunner := &runner.DockerRunner{}
	for _, image := range []string{RuntimeSandboxImage, (languages.Cpp{}).CompileImage()} {
		checkCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		imageResult, err := dockerRunner.Run(checkCtx, "", "docker", "image", "inspect", image)
		cancel()
		if err != nil || imageResult == nil || imageResult.ExitCode != 0 {
			t.Skipf("required Docker image %q is unavailable", image)
		}
	}

	job := Job{
		Language: "cpp",
		SourceCode: `#include <string>
class Solution {
public:
    std::string value(int id) {
        if (id == 1) for (;;) {}
        return std::string(70000, 'x');
    }
};`,
		Function: &FunctionSpec{
			Name:       "value",
			ReturnType: "string",
			Params:     []FunctionParam{{Name: "id", Type: "int"}},
		},
		TestCases: []TestCase{
			{ID: 1, Args: []json.RawMessage{json.RawMessage(`1`)}, Expected: json.RawMessage(`""`)},
			{ID: 2, Args: []json.RawMessage{json.RawMessage(`2`)}, Expected: json.RawMessage(`""`)},
		},
		Limits: Limits{TimeLimitMs: 1000, MemoryLimitMb: 128},
	}
	workspace, err := createWorkspace(job, languages.Cpp{})
	if err != nil {
		t.Fatalf("createWorkspace() error = %v", err)
	}
	defer os.RemoveAll(workspace)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	executor := NewDockerExecutor(dockerRunner)
	compileResult, err := executor.Compile(ctx, workspace, languages.Cpp{}, job.Limits)
	if err != nil || compileResult == nil || compileResult.ExitCode != 0 {
		t.Fatalf("Compile() result = %+v, error = %v", compileResult, err)
	}
	sandbox, err := executor.StartSandbox(ctx)
	if err != nil {
		t.Fatalf("StartSandbox() error = %v", err)
	}
	defer executor.RemoveSandbox(sandbox)
	if err := executor.ConfigureSandbox(ctx, sandbox, job.Limits); err != nil {
		t.Fatalf("ConfigureSandbox() error = %v", err)
	}
	if err := executor.PrepareSandbox(ctx, sandbox, workspace); err != nil {
		t.Fatalf("PrepareSandbox() error = %v", err)
	}

	timeoutJob := job
	timeoutJob.TestCases = job.TestCases[:1]
	timeoutJob.Limits.TimeLimitMs = 100
	result, err := runTestCasesWithSandboxReset(ctx, executor, sandbox, workspace, timeoutJob, languages.Cpp{}, false)
	if err != nil || result.Status != TimeLimitExceeded {
		t.Fatalf("timeout result = %+v, error = %v, want time limit exceeded", result, err)
	}

	outputJob := job
	outputJob.TestCases = job.TestCases[1:]
	result, err = runTestCasesWithSandboxReset(ctx, executor, sandbox, workspace, outputJob, languages.Cpp{}, false)
	if err != nil || result.Status != OutputLimitExceeded {
		t.Fatalf("output result = %+v, error = %v, want output limit exceeded", result, err)
	}

	cancelCtx, cancelRun := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancelRun()
	_, err = executor.RunTestCases(cancelCtx, sandbox, []string{"1"}, languages.Cpp{}, 10*time.Second)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled RunTestCases() error = %v, want deadline exceeded", err)
	}
	if !sandbox.restarted || sandbox.needsReplace {
		t.Fatalf("sandbox state after cancellation = restarted:%t needsReplace:%t, want a restarted reusable sandbox", sandbox.restarted, sandbox.needsReplace)
	}

	if err := executor.PrepareSandbox(ctx, sandbox, workspace); err != nil {
		t.Fatalf("PrepareSandbox() after cancellation error = %v", err)
	}
	result, err = runTestCasesWithSandboxReset(ctx, executor, sandbox, workspace, outputJob, languages.Cpp{}, false)
	if err != nil || result.Status != OutputLimitExceeded {
		t.Fatalf("post-cancellation result = %+v, error = %v, want output limit exceeded", result, err)
	}
}

func TestDockerCppFunctionCaseSupervisorBenchmark(t *testing.T) {
	if os.Getenv("YEXJUDGE_RUN_SUPERVISOR_BENCHMARK") != "1" {
		t.Skip("set YEXJUDGE_RUN_SUPERVISOR_BENCHMARK=1 to run the Docker Function Mode supervisor benchmark")
	}
	if os.Getenv("YEXJUDGE_DOCKER_TESTS") != "1" {
		t.Skip("set YEXJUDGE_DOCKER_TESTS=1 with compiler/runtime images and a reachable Docker daemon")
	}
	dockerRunner := &runner.DockerRunner{}
	for _, image := range []string{RuntimeSandboxImage, (languages.Cpp{}).CompileImage()} {
		checkCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		imageResult, err := dockerRunner.Run(checkCtx, "", "docker", "image", "inspect", image)
		cancel()
		if err != nil || imageResult == nil || imageResult.ExitCode != 0 {
			t.Skipf("required Docker image %q is unavailable", image)
		}
	}

	for _, count := range []int{1, 4, 10, 100} {
		t.Run(fmt.Sprintf("supervisor/cases_%d", count), func(t *testing.T) {
			job := Job{
				Language:   "cpp",
				SourceCode: "class Solution { public: int increment(int value) { return value + 1; } };",
				Function: &FunctionSpec{
					Name:       "increment",
					ReturnType: "int",
					Params:     []FunctionParam{{Name: "value", Type: "int"}},
				},
				TestCases: make([]TestCase, count),
				Limits:    Limits{TimeLimitMs: 1000, MemoryLimitMb: 128},
			}
			for i := range job.TestCases {
				job.TestCases[i] = TestCase{
					ID:       i + 1,
					Args:     []json.RawMessage{json.RawMessage(fmt.Sprintf("%d", i))},
					Expected: json.RawMessage(fmt.Sprintf("%d", i+1)),
				}
			}
			workspace, err := createWorkspace(job, languages.Cpp{})
			if err != nil {
				t.Fatalf("createWorkspace() error = %v", err)
			}
			defer os.RemoveAll(workspace)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			executor := NewDockerExecutor(dockerRunner)
			endToEndStarted := time.Now()
			compileStarted := time.Now()
			compileResult, err := executor.Compile(ctx, workspace, languages.Cpp{}, job.Limits)
			compileTime := time.Since(compileStarted)
			if err != nil || compileResult == nil || compileResult.ExitCode != 0 {
				t.Fatalf("Compile() result = %+v, error = %v", compileResult, err)
			}
			sandbox, err := executor.StartSandbox(ctx)
			if err != nil {
				t.Fatalf("StartSandbox() error = %v", err)
			}
			defer executor.RemoveSandbox(sandbox)
			if err := executor.ConfigureSandbox(ctx, sandbox, job.Limits); err != nil {
				t.Fatalf("ConfigureSandbox() error = %v", err)
			}
			if err := executor.PrepareSandbox(ctx, sandbox, workspace); err != nil {
				t.Fatalf("PrepareSandbox() error = %v", err)
			}
			testcaseStarted := time.Now()
			result, err := runTestCases(ctx, executor, sandbox, workspace, job, languages.Cpp{})
			testcaseTime := time.Since(testcaseStarted)
			endToEndTime := time.Since(endToEndStarted)
			if err != nil || result.Status != Accepted || result.PassedTestCases != count {
				t.Fatalf("execution result = %+v, error = %v, want %d accepted cases", result, err, count)
			}
			t.Logf("mode=supervisor cases=%d compile_ms=%.3f testcase_phase_ms=%.3f end_to_end_ms=%.3f queue_wait_ms=not-measured",
				count, milliseconds(compileTime), milliseconds(testcaseTime), milliseconds(endToEndTime))
		})
	}
}

func TestDockerSupervisorIsolatesCasesFromFilesAndDetachedChildren(t *testing.T) {
	if os.Getenv("YEXJUDGE_DOCKER_TESTS") != "1" {
		t.Skip("opt-in Docker integration test; set YEXJUDGE_DOCKER_TESTS=1 with compiler/runtime images and a reachable Docker daemon")
	}

	dockerRunner := &runner.DockerRunner{}
	for _, image := range []string{RuntimeSandboxImage, (languages.Cpp{}).CompileImage()} {
		checkCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		imageResult, err := dockerRunner.Run(checkCtx, "", "docker", "image", "inspect", image)
		cancel()
		if err != nil || imageResult == nil || imageResult.ExitCode != 0 {
			t.Skipf("required Docker image %q is unavailable", image)
		}
	}

	job := Job{
		Language: "cpp",
		SourceCode: `#include <fstream>
#include <sstream>
#include <cerrno>
#include <sys/types.h>
#include <unistd.h>
class Solution {
public:
    bool isolated(int id) {
        if (id == 1) {
            std::ofstream("/workspace/dirty") << "dirty";
            std::ofstream("/tmp/dirty") << "dirty";
            std::ofstream("/dev/shm/dirty") << "dirty";
            bool identityIsRestricted = false;
            std::ifstream status("/proc/self/status");
            std::string line;
			bool uidOK = false, gidOK = false, capsOK = true;
			int capabilitySets = 0;
            while (std::getline(status, line)) {
                if (line.rfind("Uid:", 0) == 0 || line.rfind("Gid:", 0) == 0) {
                    std::istringstream ids(line.substr(4));
                    int realID, effectiveID, savedID, filesystemID;
                    if (ids >> realID >> effectiveID >> savedID >> filesystemID) {
                        bool allContestant = realID == 10001 && effectiveID == 10001 && savedID == 10001 && filesystemID == 10001;
                        if (line.rfind("Uid:", 0) == 0) uidOK = allContestant;
                        else gidOK = allContestant;
                    }
                }
                if (line.rfind("CapEff:", 0) == 0 || line.rfind("CapPrm:", 0) == 0 ||
                    line.rfind("CapInh:", 0) == 0 || line.rfind("CapAmb:", 0) == 0) {
                    ++capabilitySets;
                    if (line.find("0000000000000000") == std::string::npos) capsOK = false;
                }
            }
            identityIsRestricted = uidOK && gidOK && capsOK && capabilitySets == 4;
            errno = 0;
            bool sessionChangeBlocked = setsid() == -1 && errno == EPERM;
            errno = 0;
            bool processGroupChangeBlocked = setpgid(0, 0) == -1 && errno == EPERM;
            bool executableProtected = unlink("/workspace/main") == -1 && unlink("/workspace/main.cpp") == -1;
            std::ofstream sourceOverwrite("/workspace/main.cpp");
            bool sourceWriteBlocked = !sourceOverwrite;
            pid_t child = fork();
            if (child == 0) {
                (void)setsid();
                usleep(500000);
                std::ofstream("/workspace/late") << "late";
                _exit(0);
            }
            bool forkLimitObserved = false;
            for (int i = 0; i < 96; ++i) {
                pid_t forkBombChild = fork();
                if (forkBombChild == 0) { for (;;) pause(); }
                if (forkBombChild < 0) { forkLimitObserved = true; break; }
            }
            return identityIsRestricted && sessionChangeBlocked && processGroupChangeBlocked && executableProtected && sourceWriteBlocked && forkLimitObserved;
        }
        return access("/workspace/dirty", F_OK) == -1 && access("/workspace/late", F_OK) == -1 &&
            access("/tmp/dirty", F_OK) == -1 && access("/dev/shm/dirty", F_OK) == -1;
    }
};`,
		Function: &FunctionSpec{
			Name:       "isolated",
			ReturnType: "bool",
			Params:     []FunctionParam{{Name: "id", Type: "int"}},
		},
		TestCases: []TestCase{
			{ID: 1, Args: []json.RawMessage{json.RawMessage(`1`)}, Expected: json.RawMessage(`true`)},
			{ID: 2, Args: []json.RawMessage{json.RawMessage(`2`)}, Expected: json.RawMessage(`true`)},
		},
		Limits: Limits{TimeLimitMs: 1000, MemoryLimitMb: 128},
	}
	workspace, err := createWorkspace(job, languages.Cpp{})
	if err != nil {
		t.Fatalf("createWorkspace() error = %v", err)
	}
	defer os.RemoveAll(workspace)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	executor := NewDockerExecutor(dockerRunner)
	compileResult, err := executor.Compile(ctx, workspace, languages.Cpp{}, job.Limits)
	if err != nil || compileResult == nil || compileResult.ExitCode != 0 {
		t.Fatalf("Compile() result = %+v, error = %v", compileResult, err)
	}
	sandbox, err := executor.StartSandbox(ctx)
	if err != nil {
		t.Fatalf("StartSandbox() error = %v", err)
	}
	defer executor.RemoveSandbox(sandbox)
	if err := executor.ConfigureSandbox(ctx, sandbox, job.Limits); err != nil {
		t.Fatalf("ConfigureSandbox() error = %v", err)
	}
	if err := executor.PrepareSandbox(ctx, sandbox, workspace); err != nil {
		t.Fatalf("PrepareSandbox() error = %v", err)
	}

	result, err := runTestCasesWithSandboxReset(ctx, executor, sandbox, workspace, job, languages.Cpp{}, false)
	if err != nil {
		t.Fatalf("runTestCasesWithSandboxReset() error = %v", err)
	}
	if result.Status != Accepted || result.PassedTestCases != 2 {
		t.Fatalf("result = %+v, want both cases accepted after filesystem and child-process cleanup", result)
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
