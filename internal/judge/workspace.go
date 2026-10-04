package judge

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
	"yexjudge/internal/judge/languages"
)

const (
	MaxWorkspaceBytes       = 32 * 1024 * 1024
	MaxGeneratedSourceBytes = 16 * 1024 * 1024
	MaxArtifactFiles        = 128
	MaxArtifactArchiveBytes = MaxWorkspaceBytes + MaxArtifactFiles*2048 + 10240
)

func createWorkspace(job Job, spec languages.Spec) (string, error) {
	if err := validateJobWork(job); err != nil {
		return "", err
	}
	if !sourceFileAllowed(spec.SourceFileName()) {
		return "", fmt.Errorf("unsupported source filename")
	}
	sourceCode := job.SourceCode
	var err error
	if job.Function != nil || job.Class != nil {
		if spec.Name() != "cpp" {
			return "", fmt.Errorf("driver modes currently support cpp only")
		}
		if job.Function != nil {
			sourceCode, err = buildCppFunctionHarness(job)
		} else {
			sourceCode, err = buildCppClassHarness(job)
		}
		if err != nil {
			return "", fmt.Errorf("generate source: invalid driver metadata")
		}
	}
	if len(sourceCode) > MaxGeneratedSourceBytes {
		return "", fmt.Errorf("generated source exceeds workspace limit")
	}
	workspace, err := os.MkdirTemp("", "yexjudge-*")
	if err != nil {
		return "", fmt.Errorf("create source directory failed")
	}
	// Keep host ownership with the service, including when it runs as root.
	// The unprivileged compiler only needs traversal and read access via a RO bind.
	if os.Getuid() == 0 {
		err = os.Chown(workspace, -1, 10001)
		if err == nil {
			err = os.Chmod(workspace, 0750)
		}
	}
	if err == nil {
		err = os.WriteFile(filepath.Join(workspace, spec.SourceFileName()), []byte(sourceCode), 0644)
	}
	if err != nil {
		_ = os.RemoveAll(workspace)
		return "", fmt.Errorf("write source workspace failed")
	}
	return workspace, nil
}

func sourceFileAllowed(name string) bool {
	switch name {
	case "main.c", "main.cpp", "main.go", "Main.java", "main.py":
		return true
	}
	return false
}

func artifactAllowed(language, name string) bool {
	if language != "java" {
		return (language == "c" || language == "cpp" || language == "go") && name == "main"
	}
	if !strings.HasSuffix(name, ".class") || len(name) > 200 || !utf8.ValidString(name) {
		return false
	}
	stem := strings.TrimSuffix(name, ".class")
	if stem == "" {
		return false
	}
	for i, r := range stem {
		start := unicode.IsLetter(r) || unicode.Is(unicode.Nl, r) || unicode.Is(unicode.Sc, r) || unicode.Is(unicode.Pc, r)
		part := start || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r)
		if !start && !(i > 0 && part) {
			return false
		}
	}
	return true
}

func openWorkspace(workspace string) (*os.Root, error) {
	info, err := os.Lstat(workspace)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("workspace must be a regular directory")
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, fmt.Errorf("open workspace failed")
	}
	return root, nil
}

// O_NOFOLLOW and fstat validate the opened inode, not a racy path check. Never
// open special files blocking, and never accept hardlinked host files.
func openWorkspaceFile(root *os.Root, name string) (*os.File, os.FileInfo, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open workspace file failed")
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("workspace files must be regular and non-symlink")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		_ = f.Close()
		return nil, nil, fmt.Errorf("workspace hardlinks are forbidden")
	}
	return f, info, nil
}

func workspaceNames(root *os.Root) ([]string, error) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("list workspace failed")
	}
	defer dir.Close()
	entries, err := dir.ReadDir(MaxArtifactFiles + 2)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("list workspace failed")
	}
	if len(entries) > MaxArtifactFiles+1 {
		return nil, fmt.Errorf("workspace contains too many files")
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}

func validateSourceWorkspace(workspace string, spec languages.Spec) error {
	root, err := openWorkspace(workspace)
	if err != nil {
		return err
	}
	defer root.Close()
	names, err := workspaceNames(root)
	if err != nil {
		return err
	}
	if len(names) != 1 || names[0] != spec.SourceFileName() || !sourceFileAllowed(names[0]) {
		return fmt.Errorf("source workspace must contain only the expected source")
	}
	f, info, err := openWorkspaceFile(root, names[0])
	if err != nil {
		return err
	}
	defer f.Close()
	if info.Size() > MaxGeneratedSourceBytes {
		return fmt.Errorf("source workspace exceeds size limit")
	}
	return nil
}

// Build our own archive from validated regular files; never invoke host tar on
// a compiler-controlled directory. Compiled sources are not staged at runtime.
func runtimeWorkspaceArchive(workspace string) (string, error) {
	root, err := openWorkspace(workspace)
	if err != nil {
		return "", err
	}
	defer root.Close()
	names, err := workspaceNames(root)
	if err != nil {
		return "", err
	}
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	var total int64
	staged := 0
	for _, name := range names {
		if !sourceFileAllowed(name) && name != "main" && !artifactAllowed("java", name) {
			return "", fmt.Errorf("unexpected workspace file")
		}
		f, info, err := openWorkspaceFile(root, name)
		if err != nil {
			return "", err
		}
		total += info.Size()
		if info.Size() < 0 || total > MaxWorkspaceBytes {
			_ = f.Close()
			return "", fmt.Errorf("workspace exceeds total size limit")
		}
		if name != "main.py" && sourceFileAllowed(name) {
			_ = f.Close()
			continue
		}
		mode := int64(0600)
		if name == "main" {
			mode = 0700
		}
		err = tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: info.Size(), Typeflag: tar.TypeReg})
		if err == nil {
			_, err = io.CopyN(tw, f, info.Size())
		}
		_ = f.Close()
		if err != nil {
			return "", fmt.Errorf("archive workspace file failed")
		}
		staged++
	}
	if staged == 0 || staged > MaxArtifactFiles {
		return "", fmt.Errorf("workspace has no artifacts or too many artifacts")
	}
	if err := tw.Close(); err != nil {
		return "", fmt.Errorf("finish workspace archive failed")
	}
	return archive.String(), nil
}

// This is a strict artifact protocol, NOT general-purpose tar extraction.
// All paths, types and sizes are checked; ownership/modes/extensions from the
// container are ignored. O_EXCL prevents overwriting any host file or link.
func importArtifacts(workspace, language string, archive io.Reader) error {
	root, err := openWorkspace(workspace)
	if err != nil {
		return err
	}
	defer root.Close()
	names, err := workspaceNames(root)
	if err != nil {
		return err
	}
	var total int64
	for _, name := range names {
		if !sourceFileAllowed(name) {
			return fmt.Errorf("artifact destination must contain only source")
		}
		f, info, err := openWorkspaceFile(root, name)
		if err != nil {
			return err
		}
		_ = f.Close()
		total += info.Size()
	}
	tr := tar.NewReader(io.LimitReader(archive, MaxArtifactArchiveBytes+1))
	seen := make(map[string]bool)
	var created []string
	success := false
	defer func() {
		if !success {
			for _, name := range created {
				_ = root.Remove(name)
			}
		}
	}()
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("invalid artifact archive")
		}
		if !artifactAllowed(language, header.Name) || seen[header.Name] || len(seen) >= MaxArtifactFiles ||
			header.Typeflag != tar.TypeReg || header.Linkname != "" || len(header.PAXRecords) != 0 || len(header.Xattrs) != 0 {
			return fmt.Errorf("artifact archive contains an unexpected file or link")
		}
		if header.Size <= 0 || header.Size > MaxWorkspaceBytes-total {
			return fmt.Errorf("artifacts exceed workspace total size limit")
		}
		total += header.Size
		seen[header.Name] = true
		f, err := root.OpenFile(header.Name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return fmt.Errorf("create artifact failed")
		}
		created = append(created, header.Name)
		_, copyErr := io.CopyN(f, tr, header.Size)
		closeErr := f.Close()
		if copyErr != nil || closeErr != nil {
			return fmt.Errorf("write artifact failed")
		}
	}
	required := "main"
	if language == "java" {
		required = "Main.class"
	}
	if !seen[required] {
		return fmt.Errorf("compiler did not produce the required artifact")
	}
	success = true
	return nil
}
