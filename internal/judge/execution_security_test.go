package judge

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yexjudge/internal/judge/languages"
	"yexjudge/internal/runner"
)

type testArtifact struct {
	name     string
	data     string
	typeflag byte
	link     string
	pax      map[string]string
	size     int64
}

func testArtifactArchive(t *testing.T, entries ...*testArtifact) string {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, entry := range entries {
		kind := entry.typeflag
		if kind == 0 {
			kind = tar.TypeReg
		}
		size := int64(len(entry.data))
		if entry.size != 0 {
			size = entry.size
		}
		h := &tar.Header{Name: entry.name, Typeflag: kind, Linkname: entry.link, Size: size, Mode: 07777, Uid: 0, Gid: 0, PAXRecords: entry.pax, Format: tar.FormatGNU}
		if len(entry.pax) > 0 {
			h.Format = tar.FormatPAX
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, entry.data); err != nil {
			t.Fatal(err)
		}
		if entry.size != 0 {
			return b.String()
		} // deliberately incomplete oversized body
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func sourceWorkspace(t *testing.T, spec languages.Spec) string {
	t.Helper()
	workspace, err := createWorkspace(Job{SourceCode: "original source"}, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(workspace) })
	return workspace
}

func TestTrustedArtifactExportRejectsLinksAndUnexpectedFiles(t *testing.T) {
	for _, kind := range []string{"regular", "symlink", "hardlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			workspace := t.TempDir()
			if err := os.WriteFile(filepath.Join(workspace, "source"), []byte("source"), 0600); err != nil {
				t.Fatal(err)
			}
			main := filepath.Join(workspace, "main")
			var err error
			switch kind {
			case "regular":
				err = os.WriteFile(main, []byte("binary"), 0600)
			case "symlink":
				err = os.Symlink("source", main)
			case "hardlink":
				err = os.Link(filepath.Join(workspace, "source"), main)
			case "directory":
				err = os.Mkdir(main, 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			// Only change the fixed working directory for this local script test;
			// never execute the Docker descendant-kill operation on the host.
			script := strings.Replace(artifactExportScript, "cd /workspace", `cd "$2"`, 1)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result, err := (&runner.DockerRunner{}).Run(ctx, "", "/bin/sh", "-c", script, "export", "cpp", workspace)
			if err != nil {
				t.Fatal(err)
			}
			if kind != "regular" {
				if result.ExitCode == 0 {
					t.Fatal("export accepted a link or non-regular file")
				}
				return
			}
			if result.ExitCode != 0 {
				t.Fatal("export failed")
			}
			tr := tar.NewReader(strings.NewReader(result.Stdout))
			h, err := tr.Next()
			if err != nil || h.Name != "main" || h.Typeflag != tar.TypeReg {
				t.Fatal("missing expected regular artifact")
			}
			if _, err := tr.Next(); err != io.EOF {
				t.Fatal("source or unexpected file exported")
			}
		})
	}
}

func TestArtifactImportRejectsUnsafeArchives(t *testing.T) {
	cases := []struct {
		name    string
		entries []*testArtifact
	}{
		{"symlink", []*testArtifact{{name: "main", typeflag: tar.TypeSymlink, link: "/etc/passwd"}}},
		{"hardlink", []*testArtifact{{name: "main", typeflag: tar.TypeLink, link: "main.cpp"}}},
		{"traversal", []*testArtifact{{name: "../main", data: "bad"}}},
		{"absolute", []*testArtifact{{name: "/main", data: "bad"}}},
		{"nested", []*testArtifact{{name: "sub/main", data: "bad"}}},
		{"directory", []*testArtifact{{name: "main", typeflag: tar.TypeDir}}},
		{"fifo", []*testArtifact{{name: "main", typeflag: tar.TypeFifo}}},
		{"device", []*testArtifact{{name: "main", typeflag: tar.TypeChar}}},
		{"source overwrite", []*testArtifact{{name: "main.cpp", data: "bad"}}},
		{"unexpected", []*testArtifact{{name: "secret", data: "bad"}}},
		{"duplicate", []*testArtifact{{name: "main", data: "one"}, {name: "main", data: "two"}}},
		{"empty", []*testArtifact{{name: "main"}}},
		{"total bound includes source", []*testArtifact{{name: "main", size: MaxWorkspaceBytes}}},
		{"pax metadata", []*testArtifact{{name: "main", data: "bad", pax: map[string]string{"comment": "untrusted"}}}},
		{"rollback", []*testArtifact{{name: "main", data: "one"}, {name: "../escape", data: "bad"}}},
		{"missing", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workspace := sourceWorkspace(t, languages.Cpp{})
			err := importArtifacts(workspace, "cpp", strings.NewReader(testArtifactArchive(t, tc.entries...)))
			if err == nil {
				t.Fatal("accepted unsafe artifact archive")
			}
			if strings.Contains(err.Error(), "untrusted") || strings.Contains(err.Error(), "/etc/passwd") {
				t.Fatalf("error leaked artifact data: %v", err)
			}
			entries, err := os.ReadDir(workspace)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != "main.cpp" {
				t.Fatalf("partial artifacts remain: %v", entries)
			}
			data, err := os.ReadFile(filepath.Join(workspace, "main.cpp"))
			if err != nil || string(data) != "original source" {
				t.Fatal("source modified")
			}
		})
	}
}

func TestArtifactImportLanguageCompatibility(t *testing.T) {
	for _, spec := range []languages.Spec{languages.C{}, languages.Cpp{}, languages.Go{}, languages.Java{}} {
		t.Run(spec.Name(), func(t *testing.T) {
			workspace := sourceWorkspace(t, spec)
			entries := []*testArtifact{{name: "main", data: "binary"}}
			if spec.Name() == "java" {
				entries = []*testArtifact{{name: "Main.class", data: "class"}, {name: "Main$1.class", data: "anonymous"}, {name: "Helper.class", data: "helper"}, {name: "Élément.class", data: "unicode"}}
			}
			if err := importArtifacts(workspace, spec.Name(), strings.NewReader(testArtifactArchive(t, entries...))); err != nil {
				t.Fatal(err)
			}
			archive, err := runtimeWorkspaceArchive(workspace)
			if err != nil {
				t.Fatal(err)
			}
			tr := tar.NewReader(strings.NewReader(archive))
			seen := 0
			for {
				h, err := tr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if !artifactAllowed(spec.Name(), h.Name) || h.Typeflag != tar.TypeReg {
					t.Fatalf("unexpected runtime header: %+v", h)
				}
				if h.Name == "main" && h.Mode != 0700 {
					t.Fatal("missing executable permission")
				}
				seen++
			}
			if seen != len(entries) {
				t.Fatalf("staged %d files, want %d", seen, len(entries))
			}
		})
	}
}

func TestArtifactImportBoundsJavaFileCount(t *testing.T) {
	entries := []*testArtifact{{name: "Main.class", data: "x"}}
	for i := 0; i < MaxArtifactFiles; i++ {
		entries = append(entries, &testArtifact{name: "Main$" + strings.Repeat("A", i+1) + ".class", data: "x"})
	}
	workspace := sourceWorkspace(t, languages.Java{})
	if err := importArtifacts(workspace, "java", strings.NewReader(testArtifactArchive(t, entries...))); err == nil {
		t.Fatal("accepted too many artifacts")
	}
}

func TestWorkspaceRejectsLinksSpecialFilesAndSizeOverflow(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(string) error
	}{
		{"symlink", func(w string) error { return os.Symlink("/etc/passwd", filepath.Join(w, "main")) }},
		{"hardlink", func(w string) error { return os.Link(filepath.Join(w, "main.cpp"), filepath.Join(w, "main")) }},
		{"directory", func(w string) error { return os.Mkdir(filepath.Join(w, "main"), 0700) }},
		{"unexpected file", func(w string) error { return os.WriteFile(filepath.Join(w, "private"), []byte("secret"), 0600) }},
		{"total size", func(w string) error {
			f, err := os.Create(filepath.Join(w, "main"))
			if err != nil {
				return err
			}
			defer f.Close()
			return f.Truncate(MaxWorkspaceBytes)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workspace := sourceWorkspace(t, languages.Cpp{})
			if err := tc.mutate(workspace); err != nil {
				t.Fatal(err)
			}
			if _, err := runtimeWorkspaceArchive(workspace); err == nil {
				t.Fatal("unsafe workspace staged")
			}
		})
	}
	workspace := sourceWorkspace(t, languages.Cpp{})
	link := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(workspace, link); err != nil {
		t.Fatal(err)
	}
	if err := validateSourceWorkspace(link, languages.Cpp{}); err == nil {
		t.Fatal("accepted symlink workspace")
	}
}

func TestSourceWorkspaceRejectsHardlink(t *testing.T) {
	workspace := sourceWorkspace(t, languages.Cpp{})
	if err := os.Link(filepath.Join(workspace, "main.cpp"), filepath.Join(t.TempDir(), "linked-source")); err != nil {
		t.Fatal(err)
	}
	if err := validateSourceWorkspace(workspace, languages.Cpp{}); err == nil {
		t.Fatal("hardlinked source accepted")
	}
}

func TestDockerExecutorReviewedImages(t *testing.T) {
	digest := "registry.example:5000/judge/runtime@sha256:" + strings.Repeat("a", 64)
	opts := DockerExecutorOptions{RuntimeImage: digest, CompileImages: map[string]string{"cpp": "reviewed/compiler:v1"}}
	recorder := &recordingRunner{resultFor: func(call executorCall) *runner.RunResult {
		if hasExecutorArg(call.args, artifactExportScript) {
			return &runner.RunResult{Stdout: testArtifactArchive(t, &testArtifact{name: "main", data: "binary"})}
		}
		return nil
	}}
	executor, err := NewDockerExecutorWithOptions(recorder, opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.CompileImages["cpp"] = "mutated/compiler:v2"
	sandbox, err := executor.StartSandbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !hasExecutorArg(recorder.calls[1].args, digest) {
		t.Fatal("runtime override not used")
	}
	executor.RemoveSandbox(sandbox)
	workspace := sourceWorkspace(t, languages.Cpp{})
	if _, err := executor.Compile(context.Background(), workspace, languages.Cpp{}, Limits{MemoryLimitMb: 128}); err != nil {
		t.Fatal(err)
	}
	for _, call := range recorder.calls {
		if !call.bounded {
			t.Fatalf("unbounded operation: %v", call.args)
		}
		if call.args[0] != "run" {
			continue
		}
		for _, flag := range []string{"--privileged", "--device", "--cap-add", "-v", "--volumes-from"} {
			if hasExecutorArg(call.args, flag) {
				t.Fatalf("unsafe flag %s", flag)
			}
		}
		for _, args := range [][]string{{"--pull", "never"}, {"--log-driver", "none"}, {"--cap-drop", "ALL"}, {"--security-opt", "no-new-privileges:true"}, {"--ipc", "private"}, {"--cgroupns", "private"}, {"--network", "none"}, {"--read-only"}, {"--ulimit", "core=0:0"}} {
			if !hasExecutorArg(call.args, args...) {
				t.Fatalf("missing restriction %v", args)
			}
		}
		joined := strings.Join(call.args, " ")
		for _, forbidden := range []string{"--pid host", "--ipc host", "--uts host", "--cgroupns host", "seccomp=unconfined", "apparmor=unconfined", "dst=/workspace"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("unsafe container configuration: %s", forbidden)
			}
		}
		if !strings.Contains(joined, "/workspace:rw,exec,nosuid,nodev,size=64m") {
			t.Fatal("workspace tmpfs not bounded")
		}
	}
	if !hasExecutorArg(recorder.calls[5].args, "reviewed/compiler:v1") {
		t.Fatal("compile override not copied or not used")
	}
	defaults := NewDockerExecutor(&recordingRunner{})
	if defaults.runtimeImage != RuntimeSandboxImage || len(defaults.compileImages) != 0 {
		t.Fatal("default images changed")
	}
}

func TestDockerExecutorRejectsImageDeclaredVolumes(t *testing.T) {
	for _, compile := range []bool{false, true} {
		recorder := &recordingRunner{resultFor: func(call executorCall) *runner.RunResult {
			if hasExecutorArg(call.args, "image", "inspect") {
				return &runner.RunResult{Stdout: `{"/unbounded-host-volume":{}}`}
			}
			return nil
		}}
		executor := NewDockerExecutor(recorder)
		var err error
		if compile {
			_, err = executor.Compile(context.Background(), sourceWorkspace(t, languages.Cpp{}), languages.Cpp{}, Limits{MemoryLimitMb: 128})
		} else {
			_, err = executor.StartSandbox(context.Background())
		}
		if err == nil || !strings.Contains(err.Error(), "must not declare") {
			t.Fatalf("declared volume accepted: %v", err)
		}
		if len(recorder.calls) != 1 {
			t.Fatal("container launched before image volume rejection")
		}
	}
}

func TestDockerImageReferenceValidation(t *testing.T) {
	for _, image := range []string{"gcc:13", "golang:1.24-alpine", "eclipse-temurin:17-jdk", "yexjudge-runtime:latest", "localhost:5000/team/image:v1", "ghcr.io/org/image@sha256:" + strings.Repeat("1", 64)} {
		if err := ValidateDockerImageReference(image); err != nil {
			t.Errorf("valid image rejected %q: %v", image, err)
		}
	}
	for _, image := range []string{"", "--privileged", "gcc:13\nsecret", " image", "https://registry/image", "../image", "image@sha256:abc", "image;touch", "IMAGE:tag", "repo/image:tag with spaces"} {
		if err := ValidateDockerImageReference(image); err == nil {
			t.Errorf("invalid image accepted %q", image)
		}
		if _, err := NewDockerExecutorWithOptions(&recordingRunner{}, DockerExecutorOptions{CompileImages: map[string]string{"cpp": image}}); err == nil {
			t.Error("invalid compile option accepted")
		}
	}
	if _, err := NewDockerExecutorWithOptions(&recordingRunner{}, DockerExecutorOptions{RuntimeImage: "--privileged"}); err == nil {
		t.Fatal("invalid runtime option accepted")
	}
	if _, err := NewDockerExecutorWithOptions(&recordingRunner{}, DockerExecutorOptions{CompileImages: map[string]string{"unsupported": "gcc:13"}}); err == nil {
		t.Fatal("unknown language accepted")
	}
}

func TestDockerExecutorRestoresCleanSnapshotBetweenTestcases(t *testing.T) {
	workspace := sourceWorkspace(t, languages.Python{})
	recorder := &recordingRunner{}
	executor := NewDockerExecutor(recorder)
	sandbox := &Sandbox{ContainerName: "sandbox"}
	if err := executor.PrepareSandbox(context.Background(), sandbox, workspace); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := executor.RunTestCase(context.Background(), sandbox, "", languages.Python{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(recorder.calls) != 11 {
		t.Fatalf("calls = %d, want prepare(3), run/reset/restore(4), run/reset/restore(4)", len(recorder.calls))
	}
	if recorder.calls[2].input == "" || recorder.calls[2].input != recorder.calls[6].input {
		t.Fatal("snapshot not restored")
	}
	if !hasExecutorArg(recorder.calls[6].args, "tar", "-xf", "-", "-C", "/workspace") {
		t.Fatal("missing restaging before second case")
	}
	if !sandbox.restarted {
		t.Fatal("successful execution not reset")
	}
	if err := executor.ResetSandbox(context.Background(), sandbox); err != nil {
		t.Fatal(err)
	}
	if _, ok := executor.staged.Load(sandbox.ContainerName); ok {
		t.Fatal("reset retained previous job snapshot")
	}
}

func TestDockerExecutorCleanupDoesNotTurnSuccessIntoTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	recorder := &recordingRunner{resultFor: func(call executorCall) *runner.RunResult {
		if hasExecutorArg(call.args, "restart") {
			<-ctx.Done()
		}
		return nil
	}}
	result, err := NewDockerExecutor(recorder).RunTestCase(ctx, &Sandbox{ContainerName: "sandbox"}, "", languages.Python{})
	if err != nil {
		t.Fatal(err)
	}
	if result.TimedOut {
		t.Fatal("cleanup consumed the completed program's execution time limit")
	}
}

func TestDockerExecutorEnforcesWallClockJobBudget(t *testing.T) {
	recorder := &recordingRunner{}
	executor := NewDockerExecutor(recorder)
	executor.staged.Store("sandbox", stagedWorkspace{deadline: time.Now().Add(-time.Second)})
	if _, err := executor.RunTestCase(context.Background(), &Sandbox{ContainerName: "sandbox"}, "", languages.Python{}); err == nil {
		t.Fatal("expired job executed")
	}
	if len(recorder.calls) != 0 {
		t.Fatal("Docker invoked for expired job")
	}
}

type failingSecurityRunner struct{}

func (failingSecurityRunner) Run(context.Context, string, string, ...string) (*runner.RunResult, error) {
	return nil, errors.New("SECRET_SOURCE SECRET_TESTCASE SECRET_STDERR")
}

func TestDockerExecutorRedactsRunnerErrorsAndStillResets(t *testing.T) {
	e := NewDockerExecutor(failingSecurityRunner{})
	if _, err := e.StartSandbox(context.Background()); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("unsafe start error: %v", err)
	}
	sandbox := &Sandbox{ContainerName: "sandbox"}
	if _, err := e.RunTestCase(context.Background(), sandbox, "SECRET_TESTCASE", languages.Python{}); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("unsafe runtime error: %v", err)
	}
	if !sandbox.needsReplace {
		t.Fatal("failed cleanup did not quarantine sandbox")
	}
}

func TestServiceInfrastructureLogRedactionAndCompilerDiagnostics(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(old)
	for _, compilation := range []bool{false, true} {
		store := &testcaseStore{}
		executor := &testcaseExecutor{prepareErr: errors.New("SECRET_STDERR SECRET_SOURCE SECRET_TESTCASE")}
		spec := languages.Spec(languages.Python{})
		job := Job{Language: "python", SourceCode: "SECRET_SOURCE", TestCases: []TestCase{{Input: "SECRET_TESTCASE"}}, Limits: Limits{TimeLimitMs: 1000, MemoryLimitMb: 128}}
		if compilation {
			spec, job.Language = languages.Cpp{}, "cpp"
			executor.compileResult = &runner.RunResult{ExitCode: 1, Stderr: "SECRET_COMPILER_DIAGNOSTIC"}
		}
		service := NewService(executor, testcasePool{}, store, languages.NewRegistry(spec))
		result, err := service.ProcessSubmission(context.Background(), Submission{ID: "redaction", Job: job})
		service.Close()
		if err != nil {
			t.Fatal(err)
		}
		if compilation {
			if result.Status != CompilationError || result.ErrorMessage != "SECRET_COMPILER_DIAGNOSTIC" {
				t.Fatal("authorized compilation diagnostic lost")
			}
		} else if result.Status != InfrastructureError || strings.Contains(result.ErrorMessage, "SECRET") || strings.Contains(store.submission.FailureMessage, "SECRET") {
			t.Fatalf("infrastructure diagnostic leaked: %+v", result)
		}
	}
	if strings.Contains(logs.String(), "SECRET") {
		t.Fatalf("logs leaked execution payload: %s", logs.String())
	}
}
