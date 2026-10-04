package judge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yexjudge/internal/judge/languages"
	"yexjudge/internal/runner"
)

// Opt in with YEXJUDGE_DOCKER_SECURITY=1. Requires a Linux Docker Engine with
// working memory/CPU/PID cgroups, default seccomp, private namespaces and tmpfs,
// socket access, and all reviewed images already installed (--pull never).
// Optional overrides: YEXJUDGE_SECURITY_RUNTIME_IMAGE and
// YEXJUDGE_SECURITY_{C,CPP,GO,JAVA}_IMAGE. No images are built or pulled here.
// These are containment regression tests, not evidence of container-escape proof.
func securityDockerExecutor(t *testing.T) *DockerExecutor {
	t.Helper()
	if os.Getenv("YEXJUDGE_DOCKER_SECURITY") != "1" {
		t.Skip("set YEXJUDGE_DOCKER_SECURITY=1 with reviewed local Docker images to run containment tests")
	}
	r := &runner.DockerRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), DockerOperationTimeout)
	defer cancel()
	result, err := r.Run(ctx, "", "docker", "info", "--format", "{{json .}}")
	if err != nil || dockerCommandError("Docker security prerequisite check", result) != nil {
		t.Fatal("Docker unavailable: require Linux Engine socket access and preinstalled reviewed images")
	}
	var info struct {
		OSType          string
		MemoryLimit     bool
		SwapLimit       bool
		PidsLimit       bool
		CPUCfsQuota     bool
		SecurityOptions []string
	}
	if err := json.Unmarshal([]byte(result.Stdout), &info); err != nil {
		t.Fatal("cannot read Docker security prerequisites")
	}
	if info.OSType != "linux" || !info.MemoryLimit || !info.SwapLimit || !info.PidsLimit || !info.CPUCfsQuota {
		t.Fatal("Docker must enforce Linux memory/swap/PID/CPU limits")
	}
	seccomp := false
	for _, option := range info.SecurityOptions {
		if strings.Contains(option, "name=seccomp") && strings.Contains(option, "profile=builtin") {
			seccomp = true
		}
	}
	if !seccomp {
		t.Fatal("Docker must use the built-in seccomp profile")
	}
	opts := DockerExecutorOptions{RuntimeImage: os.Getenv("YEXJUDGE_SECURITY_RUNTIME_IMAGE"), CompileImages: map[string]string{}}
	for _, language := range []string{"c", "cpp", "go", "java"} {
		if image := os.Getenv("YEXJUDGE_SECURITY_" + strings.ToUpper(language) + "_IMAGE"); image != "" {
			opts.CompileImages[language] = image
		}
	}
	e, err := NewDockerExecutorWithOptions(r, opts)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func securitySandbox(t *testing.T, executor *DockerExecutor, memory int) *Sandbox {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), DockerOperationTimeout)
	defer cancel()
	sandbox, err := executor.StartSandbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { executor.RemoveSandbox(sandbox) })
	if err := executor.ConfigureSandbox(ctx, sandbox, Limits{MemoryLimitMb: memory}); err != nil {
		t.Fatal(err)
	}
	securityInspectSandbox(t, executor, sandbox, memory)
	return sandbox
}

func securityInspectSandbox(t *testing.T, executor *DockerExecutor, sandbox *Sandbox, memory int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), DockerOperationTimeout)
	defer cancel()
	result, err := executor.runner.Run(ctx, "", "docker", "inspect", "--format", "{{json .}}", sandbox.ContainerName)
	if err != nil || dockerCommandError("inspect security settings", result) != nil {
		t.Fatal("cannot inspect Docker security settings")
	}
	var inspect struct {
		Config     struct{ User string }
		HostConfig struct {
			Privileged        bool
			ReadonlyRootfs    bool
			NetworkMode       string
			PidMode           string
			IpcMode           string
			UTSMode           string
			CgroupnsMode      string
			CapAdd            []string
			CapDrop           []string
			SecurityOpt       []string
			Devices           []json.RawMessage
			DeviceRequests    []json.RawMessage
			DeviceCgroupRules []string
			Binds             []string
			Memory            int64
			MemorySwap        int64
			NanoCpus          int64
			PidsLimit         int64
			Tmpfs             map[string]string
			LogConfig         struct{ Type string }
		}
	}
	if err := json.Unmarshal([]byte(result.Stdout), &inspect); err != nil {
		t.Fatal("invalid Docker inspect response")
	}
	h := inspect.HostConfig
	if inspect.Config.User != "10001:10001" || h.Privileged || !h.ReadonlyRootfs || h.NetworkMode != "none" || h.PidMode != "" || h.IpcMode != "private" || h.UTSMode != "" || h.CgroupnsMode != "private" || len(h.CapAdd) != 0 || strings.Join(h.CapDrop, ",") != "ALL" || len(h.Devices) != 0 || len(h.DeviceRequests) != 0 || len(h.DeviceCgroupRules) != 0 || len(h.Binds) != 0 || h.LogConfig.Type != "none" {
		t.Fatal("Docker sandbox has unsafe user/namespaces/capabilities/devices/mounts/logging")
	}
	if strings.Join(h.SecurityOpt, ",") != "no-new-privileges:true" {
		t.Fatal("unexpected security options")
	}
	if h.Memory != int64(memory)*1024*1024 || h.MemorySwap != h.Memory || h.NanoCpus != 1_000_000_000 || h.PidsLimit != 64 {
		t.Fatal("Docker resource limits were not applied")
	}
	if !strings.Contains(h.Tmpfs["/workspace"], "size=64m") || !strings.Contains(h.Tmpfs["/tmp"], "size=16m") {
		t.Fatal("Docker tmpfs limits were not applied")
	}
}

func securityRunPython(t *testing.T, executor *DockerExecutor, sandbox *Sandbox, source string, timeout time.Duration) *runner.RunResult {
	t.Helper()
	job := Job{Language: "python", SourceCode: source}
	workspace, err := createWorkspace(job, languages.Python{})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workspace)
	if err := executor.PrepareSandbox(context.Background(), sandbox, workspace); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	result, err := executor.RunTestCase(ctx, sandbox, "", languages.Python{})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestDockerSecurityRuntimeAttacks(t *testing.T) {
	executor := securityDockerExecutor(t)
	sandbox := securitySandbox(t, executor, 128)
	cases := []struct {
		name    string
		source  string
		timeout time.Duration
		check   func(*testing.T, *runner.RunResult)
	}{
		{"filesystem and privilege", `import os
from pathlib import Path
status = Path('/proc/self/status').read_text()
assert int(next(x.split()[1] for x in status.splitlines() if x.startswith('CapEff:')), 16) == 0
assert next(x.split()[1] for x in status.splitlines() if x.startswith('NoNewPrivs:')) == '1'
assert next(x.split()[1] for x in status.splitlines() if x.startswith('Seccomp:')) == '2'
assert os.getuid() == 10001 and os.getgid() == 10001
assert not Path('/var/run/docker.sock').exists()
assert not Path('/source').exists()
for path in ['/etc/security-probe', '/proc/sys/kernel/security-probe', '/dev/security-probe']:
    try:
        Path(path).write_text('attack')
    except OSError:
        pass
    else:
        raise AssertionError('unexpected write permission')
print('contained')
`, 3 * time.Second, securityWantSuccess},
		{"network", `import socket
s = socket.socket()
s.settimeout(0.5)
try:
    s.connect(('1.1.1.1', 443))
except OSError:
    print('contained')
else:
    raise AssertionError('external network accessible')
`, 3 * time.Second, securityWantSuccess},
		{"fork", `import os, time
children = []
for i in range(256):
    try:
        pid = os.fork()
    except OSError:
        break
    if pid == 0:
        fd = os.open('/dev/null', os.O_RDWR)
        for stream in (0, 1, 2): os.dup2(fd, stream)
        os.close(fd)
        time.sleep(60)
        os._exit(0)
    children.append(pid)
assert 0 < len(children) < 64
print('contained', flush=True)
`, 4 * time.Second, securityWantSuccess},
		{"stdout flood", `import os
while True: os.write(1, b'x' * 8192)
`, 3 * time.Second, securityWantOutputLimit},
		{"stderr flood", `import os
while True: os.write(2, b'x' * 8192)
`, 3 * time.Second, securityWantOutputLimit},
		{"timeout", `while True: pass
`, 200 * time.Millisecond, func(t *testing.T, result *runner.RunResult) {
			if !result.TimedOut {
				t.Fatal("busy loop did not time out")
			}
		}},
		{"memory", `x = bytearray(512 * 1024 * 1024)
print(len(x))
`, 4 * time.Second, func(t *testing.T, result *runner.RunResult) {
			if result.ExitCode == 0 {
				t.Fatal("memory attack succeeded")
			}
		}},
		{"disk and file size", `import os, resource
assert resource.getrlimit(resource.RLIMIT_CORE)[0] == 0
assert resource.getrlimit(resource.RLIMIT_NOFILE)[0] == 1024
assert resource.getrlimit(resource.RLIMIT_FSIZE)[0] == 33554432
written = 0
try:
    for i in range(16):
        with open('/workspace/fill%d' % i, 'wb') as f:
            for j in range(16):
                written += f.write(b'x' * (1024 * 1024))
except OSError:
    pass
else:
    raise AssertionError('workspace was not bounded')
assert written <= 64 * 1024 * 1024
print('contained')
`, 4 * time.Second, securityWantSuccess},
		{"file descriptor exhaustion", `import os
files = []
try:
    for i in range(2048): files.append(os.open('/dev/null', os.O_RDONLY))
except OSError:
    pass
else:
    raise AssertionError('file descriptors were not bounded')
assert len(files) < 1024
print('contained')
`, 3 * time.Second, securityWantSuccess},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			started := time.Now()
			result := securityRunPython(t, executor, sandbox, tc.source, tc.timeout)
			tc.check(t, result)
			if time.Since(started) > DockerOperationTimeout+tc.timeout+DockerCleanupTimeout+2*time.Second {
				t.Fatal("attack cleanup exceeded operation bounds")
			}
			if !sandbox.restarted || sandbox.needsReplace {
				t.Fatal("attack did not leave a reset sandbox")
			}
			// Validate the actual restart, not just an in-memory flag. No children
			// or attacker filesystem state should survive the remounted tmpfs;
			// only the trusted source snapshot should have been restored.
			checkCtx, checkCancel := context.WithTimeout(context.Background(), DockerOperationTimeout)
			result, err := executor.runner.Run(checkCtx, "", "docker", "exec", sandbox.ContainerName,
				"python3", "-c", `import os; assert os.listdir('/workspace') == ['main.py']; assert not os.listdir('/tmp'); assert len([n for n in os.listdir('/proc') if n.isdigit()]) <= 3`)
			checkCancel()
			if err != nil || dockerCommandError("post-attack isolation check", result) != nil {
				t.Fatal("attack left processes or filesystem state behind")
			}
		})
	}
}

func securityWantSuccess(t *testing.T, result *runner.RunResult) {
	t.Helper()
	if result.ExitCode != 0 || result.TimedOut || result.OutputLimitExceeded || strings.TrimSpace(result.Stdout) != "contained" {
		t.Fatalf("containment probe failed: exit=%d timeout=%v output_limit=%v", result.ExitCode, result.TimedOut, result.OutputLimitExceeded)
	}
}

func securityWantOutputLimit(t *testing.T, result *runner.RunResult) {
	t.Helper()
	if !result.OutputLimitExceeded || len(result.Stdout) > runner.DefaultOutputLimitBytes || len(result.Stderr) > runner.DefaultOutputLimitBytes {
		t.Fatal("output flood not bounded")
	}
}

// Simulates an adversarial compiler/plugin, independently of whether a given
// language can currently execute build hooks or exploit its compiler.
type securityCompilerSpec struct {
	languages.Cpp
	command string
}

func (s securityCompilerSpec) CompileCommand() []string { return []string{"/bin/sh", "-c", s.command} }

func TestDockerSecurityCompileAttacks(t *testing.T) {
	executor := securityDockerExecutor(t)
	cases := []struct {
		name            string
		command         string
		timeout         time.Duration
		wantError       bool
		wantOutputLimit bool
		wantTimeout     bool
	}{
		{"read-only source and selective export", `echo attack > /source/main.cpp && exit 99; cp /bin/true /workspace/main; echo do-not-export > /workspace/extra; ln -s /etc/passwd /workspace/extra-link`, 10 * time.Second, false, false, false},
		{"symlink", `ln -s /etc/passwd /workspace/main`, 10 * time.Second, true, false, false},
		{"hardlink", `ln /workspace/main.cpp /workspace/main`, 10 * time.Second, true, false, false},
		{"fifo", `mkfifo /workspace/main`, 10 * time.Second, true, false, false},
		{"directory", `mkdir /workspace/main`, 10 * time.Second, true, false, false},
		{"oversized disk", `for f in a b c d e; do dd if=/dev/zero of=/workspace/$f bs=1M count=16 2>/dev/null || exit 7; done; exit 0`, 10 * time.Second, false, false, false},
		{"compiler stdout flood", `while :; do printf 0123456789; done`, 3 * time.Second, false, true, false},
		{"compiler stderr flood", `while :; do printf 0123456789 >&2; done`, 3 * time.Second, false, true, false},
		{"compiler timeout", `while :; do :; done`, 3 * time.Second, false, false, true},
		{"compiler background fork", `sleep 60 >/dev/null 2>&1 </dev/null & cp /bin/true /workspace/main`, 10 * time.Second, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workspace := sourceWorkspace(t, languages.Cpp{})
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()
			result, err := executor.Compile(ctx, workspace, securityCompilerSpec{command: tc.command}, Limits{MemoryLimitMb: 128})
			if tc.wantError {
				if err == nil {
					t.Fatal("unsafe artifact accepted")
				}
				if strings.Contains(err.Error(), "root:") || strings.Contains(err.Error(), "original source") {
					t.Fatal("export error leaked payload")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if result.OutputLimitExceeded != tc.wantOutputLimit || result.TimedOut != tc.wantTimeout {
					t.Fatalf("incorrect compiler bound result: exit=%d timeout=%v output_limit=%v", result.ExitCode, result.TimedOut, result.OutputLimitExceeded)
				}
				if tc.name == "oversized disk" && result.ExitCode == 0 {
					t.Fatal("compiler workspace was unbounded")
				}
				if !tc.wantOutputLimit && !tc.wantTimeout && tc.name != "oversized disk" && result.ExitCode != 0 {
					t.Fatal("compiler containment probe failed")
				}
			}
			data, readErr := os.ReadFile(filepath.Join(workspace, "main.cpp"))
			if readErr != nil || string(data) != "original source" {
				t.Fatal("compiler wrote host source")
			}
			files, readErr := os.ReadDir(workspace)
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, file := range files {
				if file.Name() != "main.cpp" && file.Name() != "main" {
					t.Fatal("non-artifact exported to host")
				}
			}
		})
	}
}

func TestDockerSecurityCppDriverCompatibility(t *testing.T) {
	executor := securityDockerExecutor(t)
	sandbox := securitySandbox(t, executor, 512)
	function := validFunctionJob()
	function.SourceCode = "class Solution { public: int value(){return 42;} };"
	class := Job{
		Language: "cpp", SourceCode: "class Counter { public: Counter(){} int get(){return 42;} };",
		Class:     &ClassSpec{Name: "Counter", Operations: []ClassOperationSpec{{Name: "get", ReturnType: "int"}}},
		TestCases: []TestCase{{ID: 1, Operations: []OperationCall{{Name: "get"}}, Expected: json.RawMessage(`[42]`)}},
		Limits:    Limits{TimeLimitMs: 1000, MemoryLimitMb: 512},
	}
	for _, job := range []Job{function, class} {
		t.Run(string(job.ExecutionMode()), func(t *testing.T) {
			if err := ValidateJob(job); err != nil {
				t.Fatal(err)
			}
			workspace, err := createWorkspace(job, languages.Cpp{})
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(workspace)
			result, err := executor.Compile(context.Background(), workspace, languages.Cpp{}, job.Limits)
			if err != nil {
				t.Fatal(err)
			}
			if result.ExitCode != 0 || result.TimedOut || result.OutputLimitExceeded {
				t.Fatal("driver compilation failed")
			}
			if err := executor.PrepareSandbox(context.Background(), sandbox, workspace); err != nil {
				t.Fatal(err)
			}
			result, err = executor.RunTestCase(context.Background(), sandbox, "1", languages.Cpp{})
			if err != nil {
				t.Fatal(err)
			}
			expected, err := testCaseExpectedOutput(job, job.TestCases[0])
			if err != nil {
				t.Fatal(err)
			}
			if result.ExitCode != 0 || strings.TrimSpace(result.Stdout) != expected {
				t.Fatal("driver execution failed")
			}
		})
	}
}

func TestDockerSecurityLanguageCompatibility(t *testing.T) {
	executor := securityDockerExecutor(t)
	sandbox := securitySandbox(t, executor, 512)
	cases := []struct {
		spec   languages.Spec
		source string
	}{
		{languages.C{}, "#include <stdio.h>\nint main(){puts(\"contained\");}"},
		{languages.Cpp{}, "#include <iostream>\nint main(){std::cout << \"contained\\n\";}"},
		{languages.Go{}, "package main\nimport \"fmt\"\nfunc main(){fmt.Println(\"contained\")}"},
		{languages.Java{}, "class Helper { static String value(){return \"contained\";} } public class Main { static class Inner {} public static void main(String[] args){System.out.println(Helper.value());} }"},
		{languages.Python{}, "print('contained')"},
	}
	for _, tc := range cases {
		t.Run(tc.spec.Name(), func(t *testing.T) {
			workspace, err := createWorkspace(Job{Language: tc.spec.Name(), SourceCode: tc.source}, tc.spec)
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(workspace)
			if tc.spec.NeedsCompile() {
				result, err := executor.Compile(context.Background(), workspace, tc.spec, Limits{MemoryLimitMb: 512})
				if err != nil {
					t.Fatal(err)
				}
				if result.ExitCode != 0 || result.TimedOut || result.OutputLimitExceeded {
					t.Fatalf("reviewed compiler probe failed: exit=%d timeout=%v output_limit=%v", result.ExitCode, result.TimedOut, result.OutputLimitExceeded)
				}
			}
			if err := executor.PrepareSandbox(context.Background(), sandbox, workspace); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				result, err := executor.RunTestCase(context.Background(), sandbox, "", tc.spec)
				if err != nil {
					t.Fatal(err)
				}
				securityWantSuccess(t, result)
			}
		})
	}
}
