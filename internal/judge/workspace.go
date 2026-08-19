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

	return workspace, nil
}
