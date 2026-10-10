package judge

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestArchive(t *testing.T, entries []tar.Header) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "artifacts.tar")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	for i := range entries {
		if err := writer.WriteHeader(&entries[i]); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close tar file: %v", err)
	}
	return path
}

func TestValidateCompilerArtifactArchiveRejectsUnsafeAndSpecialEntries(t *testing.T) {
	tests := []struct {
		name    string
		header  tar.Header
		message string
	}{
		{
			name:    "path traversal",
			header:  tar.Header{Name: "../outside", Mode: 0600, Size: 0, Typeflag: tar.TypeReg},
			message: "escapes the workspace",
		},
		{
			name:    "symlink",
			header:  tar.Header{Name: "link", Linkname: "main", Mode: 0777, Typeflag: tar.TypeSymlink},
			message: "unsupported tar type",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := writeTestArchive(t, []tar.Header{test.header})
			err := validateCompilerArtifactArchive(archive)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("validateCompilerArtifactArchive() error = %v, want message containing %q", err, test.message)
			}
		})
	}
}

func TestValidateCompilerArtifactArchiveEnforcesEntryAndArchiveBounds(t *testing.T) {
	t.Run("entry count", func(t *testing.T) {
		entries := make([]tar.Header, maxCompilerArtifactFiles+1)
		for i := range entries {
			entries[i] = tar.Header{Name: filepath.Join("output", string(rune('a'+i%26))+string(rune('A'+i/26))), Mode: 0600, Typeflag: tar.TypeReg}
		}
		archive := writeTestArchive(t, entries)
		err := validateCompilerArtifactArchive(archive)
		if err == nil || !strings.Contains(err.Error(), "too many artifacts") {
			t.Fatalf("validateCompilerArtifactArchive() error = %v, want artifact count limit", err)
		}
	})

	t.Run("archive byte size", func(t *testing.T) {
		archive := filepath.Join(t.TempDir(), "oversized.tar")
		file, err := os.Create(archive)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(maxCompilerArtifactArchiveSize + 1); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		err = validateCompilerArtifactArchive(archive)
		if err == nil || !strings.Contains(err.Error(), "archive exceeds") {
			t.Fatalf("validateCompilerArtifactArchive() error = %v, want archive byte limit", err)
		}
	})
}

func TestAddCompilerArtifactSizeEnforcesFileAndAggregateLimits(t *testing.T) {
	if _, err := addCompilerArtifactSize(0, int64(maxCompilerArtifactFileBytes)+1, "large"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("addCompilerArtifactSize() error = %v, want per-file artifact bound", err)
	}
	total := int64(maxCompilerArtifactTotalBytes) - 1
	if _, err := addCompilerArtifactSize(total, 1, "first"); err != nil {
		t.Fatalf("addCompilerArtifactSize() rejected an artifact at the limit: %v", err)
	}
	if _, err := addCompilerArtifactSize(total, 2, "second"); err == nil || !strings.Contains(err.Error(), "aggregate") {
		t.Fatalf("addCompilerArtifactSize() error = %v, want aggregate artifact bound", err)
	}
}

func TestValidateWorkspaceForTransferRejectsSymlinks(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "main.py"), []byte("print('ok')"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("main.py", filepath.Join(workspace, "alias")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	if err := validateWorkspaceForTransfer(workspace); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("validateWorkspaceForTransfer() error = %v, want symlink rejection", err)
	}
}
