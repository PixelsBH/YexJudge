package judge

import (
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

	if err := os.WriteFile(sourcePath, []byte(job.SourceCode), 0644); err != nil {
		os.RemoveAll(workspace)
		return "", err
	}

	return workspace, nil
}
