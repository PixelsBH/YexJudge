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

	if job.Function != nil {
		if spec.Name() != "cpp" {
			os.RemoveAll(workspace)
			return "", fmt.Errorf("function mode currently supports cpp only")
		}

		sourceCode, err = buildCppFunctionHarness(job)
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
