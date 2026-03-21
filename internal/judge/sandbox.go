package judge

import (
	"context"
	"fmt"
	"time"
	"yexjudge/internal/runner"
)

func startSandbox(ctx context.Context, r runner.Runner, workspace string, limits Limits) (string, error) {
	containerName := fmt.Sprintf("yexjudge-%d", time.Now().UnixNano())
	memoryLimit := fmt.Sprintf("%dm", limits.MemoryLimitMb)

	ctxContainer, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := r.Run(
		ctxContainer,
		"",
		"docker",
		"run",
		"-d",
		"--name", containerName,
		"--memory", memoryLimit,
		"--cpus", "1",
		"--network", "none",
		"--pids-limit", "64",
		"--security-opt", "no-new-privileges",
		"--tmpfs", "/tmp",
		"--workdir", "/workspace",
		"-v", workspace+":/workspace",
		"alpine",
		"sleep", "60",
	)
	if err != nil {
		return "", err
	}

	return containerName, nil
}

func removeSandbox(r runner.Runner, containerName string) {
	_, _ = r.Run(
		context.Background(),
		"",
		"docker",
		"rm",
		"-f",
		containerName,
	)
}
