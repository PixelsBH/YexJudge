package judge

import (
	"fmt"
	"os"
	"path/filepath"
	"yexjudge/internal/judge/languages"
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
