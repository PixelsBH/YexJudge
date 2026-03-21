package judge

import (
	"context"
	"time"
	"yexjudge/internal/runner"
)

func compileProgram(ctx context.Context, r runner.Runner, workspace string) (*runner.RunResult, error) {
	ctxCompile, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	return r.Run(
		ctxCompile,
		"",
		"docker",
		"run",
		"--rm",
		"-v", workspace+":/workspace",
		"gcc:13",
		"g++",
		"-O2",
		"-pipe",
		"-static",
		"-s",
		"/workspace/main.cpp",
		"-o",
		"/workspace/main",
	)
}
