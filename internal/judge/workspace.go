package judge

import (
	"os"
	"path/filepath"
)

func createWorkspace(job Job) (string, error) {
	workspace, err := os.MkdirTemp("", "yexjudge-*")
	if err != nil {
		return "", err
	}

	sourcePath := filepath.Join(workspace, "main.cpp")

	if err := os.WriteFile(sourcePath, []byte(job.SourceCode), 0644); err != nil {
		os.RemoveAll(workspace)
		return "", err
	}

	return workspace, nil
}
