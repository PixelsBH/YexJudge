package judge

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"yexjudge/internal/judge/languages"
)

const (
	maxCompilerArtifactFiles       = 1024
	maxCompilerArtifactEntries     = 2048
	maxCompilerArtifactFileBytes   = 64 << 20
	maxCompilerArtifactTotalBytes  = 64 << 20
	maxCompilerArtifactArchiveSize = 80 << 20
	maxWorkspaceArchiveSize        = 80 << 20
)

func createWorkspace(job Job, spec languages.Spec) (string, error) {
	workspace, err := os.MkdirTemp("", "yexjudge-*")
	if err != nil {
		return "", err
	}
	sourcePath := filepath.Join(workspace, spec.SourceFileName())
	sourceCode := job.SourceCode

	if job.Function != nil || job.Class != nil {
		if spec.Name() != "cpp" {
			os.RemoveAll(workspace)
			return "", fmt.Errorf("driver modes currently support cpp only")
		}

		if job.Function != nil {
			sourceCode, err = buildCppFunctionHarness(job)
		} else {
			sourceCode, err = buildCppClassHarness(job)
		}
		if err != nil {
			os.RemoveAll(workspace)
			return "", err
		}
	}

	if err := os.WriteFile(sourcePath, []byte(sourceCode), 0644); err != nil {
		os.RemoveAll(workspace)
		return "", err
	}

	// A root server still compiles as the dedicated container user. Transfer
	// ownership after writing the source so the workspace can remain mode 0700.
	if os.Getuid() == 0 {
		if err := os.Chown(workspace, 10001, 10001); err != nil {
			os.RemoveAll(workspace)
			return "", err
		}
		if err := os.Chmod(workspace, 0700); err != nil {
			os.RemoveAll(workspace)
			return "", err
		}
	}

	return workspace, nil
}

func importCompilerArtifacts(archivePath, workspace string) (returnErr error) {
	if err := validateCompilerArtifactArchive(archivePath); err != nil {
		return err
	}

	staging, err := os.MkdirTemp(workspace, ".yexjudge-artifacts-*")
	if err != nil {
		return fmt.Errorf("create compiler artifact staging directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(staging); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("remove compiler artifact staging directory: %w", err)
		}
	}()

	if err := extractCompilerArtifactArchive(archivePath, staging); err != nil {
		return err
	}

	return filepath.WalkDir(staging, func(source string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if source == staging {
			return nil
		}
		relative, err := filepath.Rel(staging, source)
		if err != nil {
			return err
		}
		destination := filepath.Join(workspace, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("compiler artifact %q is not a regular file", relative)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return err
		}
		if err := os.Rename(source, destination); err != nil {
			return fmt.Errorf("install compiler artifact %q: %w", relative, err)
		}
		return nil
	})
}

func validateCompilerArtifactArchive(archivePath string) error {
	info, err := os.Stat(archivePath)
	if err != nil {
		return fmt.Errorf("stat compiler artifact archive: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("compiler artifact archive is not a regular file")
	}
	if info.Size() > maxCompilerArtifactArchiveSize {
		return fmt.Errorf("compiler artifact archive exceeds %d bytes", maxCompilerArtifactArchiveSize)
	}

	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open compiler artifact archive: %w", err)
	}
	defer file.Close()

	reader := tar.NewReader(file)
	entries := 0
	files := 0
	var totalBytes int64
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read compiler artifact archive: %w", err)
		}
		entries++
		if entries > maxCompilerArtifactEntries {
			return fmt.Errorf("compiler artifact archive contains too many entries")
		}
		name, err := safeArchivePath(header.Name)
		if err != nil {
			return err
		}
		if name == "." {
			if header.Typeflag == tar.TypeDir && header.Size == 0 {
				continue
			}
			return fmt.Errorf("compiler artifact archive contains an invalid root entry")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if header.Size != 0 {
				return fmt.Errorf("compiler artifact directory %q has data", name)
			}
		case tar.TypeReg, tar.TypeRegA:
			files++
			if files > maxCompilerArtifactFiles {
				return fmt.Errorf("compiler produced too many artifacts")
			}
			totalBytes, err = addCompilerArtifactSize(totalBytes, header.Size, name)
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("compiler artifact %q has unsupported tar type %d", name, header.Typeflag)
		}
	}
	if files == 0 {
		return fmt.Errorf("compiler artifact archive is empty")
	}
	return nil
}

func addCompilerArtifactSize(totalBytes, fileBytes int64, name string) (int64, error) {
	if fileBytes < 0 || fileBytes > maxCompilerArtifactFileBytes {
		return totalBytes, fmt.Errorf("compiler artifact %q exceeds %d bytes", name, maxCompilerArtifactFileBytes)
	}
	if fileBytes > maxCompilerArtifactTotalBytes-totalBytes {
		return totalBytes, fmt.Errorf("compiler artifacts exceed %d aggregate bytes", maxCompilerArtifactTotalBytes)
	}
	return totalBytes + fileBytes, nil
}

func safeArchivePath(name string) (string, error) {
	if name == "" || strings.IndexByte(name, 0) >= 0 || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("compiler artifact archive contains an invalid path")
	}
	clean := path.Clean(strings.TrimPrefix(name, "./"))
	if clean == ".." || strings.HasPrefix(clean, "../") || clean == "." && name != "." && name != "./" {
		return "", fmt.Errorf("compiler artifact archive path %q escapes the workspace", name)
	}
	return clean, nil
}

func validateWorkspaceForTransfer(workspace string) error {
	entries := 0
	files := 0
	var totalBytes int64
	err := filepath.WalkDir(workspace, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > maxCompilerArtifactEntries {
			return fmt.Errorf("workspace contains too many entries")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("workspace entry %q is not a regular file or directory", name)
		}
		files++
		if files > maxCompilerArtifactFiles {
			return fmt.Errorf("workspace contains too many files")
		}
		totalBytes, err = addCompilerArtifactSize(totalBytes, info.Size(), name)
		if err != nil {
			return fmt.Errorf("workspace: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("validate workspace before transfer: %w", err)
	}
	return nil
}

func extractCompilerArtifactArchive(archivePath, destination string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open compiler artifact archive: %w", err)
	}
	defer file.Close()

	reader := tar.NewReader(file)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read compiler artifact archive: %w", err)
		}
		name, err := safeArchivePath(header.Name)
		if err != nil {
			return err
		}
		if name == "." {
			continue
		}
		target := filepath.Join(destination, filepath.FromSlash(name))
		relative, err := filepath.Rel(destination, target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("compiler artifact archive path %q escapes the staging directory", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0700); err != nil {
				return fmt.Errorf("create compiler artifact directory %q: %w", name, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return fmt.Errorf("create compiler artifact parent for %q: %w", name, err)
			}
			output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return fmt.Errorf("create compiler artifact %q: %w", name, err)
			}
			_, copyErr := io.CopyN(output, reader, header.Size)
			modeErr := output.Chmod(os.FileMode(header.Mode) & 0777)
			closeErr := output.Close()
			if copyErr != nil {
				return fmt.Errorf("write compiler artifact %q: %w", name, copyErr)
			}
			if modeErr != nil {
				return fmt.Errorf("set compiler artifact mode %q: %w", name, modeErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close compiler artifact %q: %w", name, closeErr)
			}
		default:
			return fmt.Errorf("compiler artifact %q has unsupported tar type %d", name, header.Typeflag)
		}
	}
}
