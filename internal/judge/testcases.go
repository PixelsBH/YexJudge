package judge

import (
	"context"
	"strings"
	"time"
	"yexjudge/internal/judge/languages"
)

func runTestCases(
	ctx context.Context, executor Executor,
	containerName string, job Job, spec languages.Spec) (Result, error) {
	maxRuntimeMs := 0

	for _, tc := range job.TestCases {
		ctxRun, cancelRun := context.WithTimeout(
			ctx,
			time.Duration(job.Limits.TimeLimitMs)*time.Millisecond,
		)

		runRes, err := executor.RunTestCase(
			ctxRun,
			containerName,
			tc.Input,
			spec,
		)

		cancelRun()

		if err != nil {
			return Result{}, err
		}

		if runRes.TimedOut {
			return Result{
				Status:         TimeLimitExceeded,
				FailedTestCase: &tc,
			}, nil
		}

		if runRes.ExitCode != 0 {
			return Result{
				Status:         RuntimeError,
				FailedTestCase: &tc,
				ErrorMessage:   runRes.Stderr,
			}, nil
		}

		output := strings.TrimSpace(runRes.Stdout)
		expected := strings.TrimSpace(tc.ExpectedOutput)

		if output != expected {
			return Result{
				Status:         WrongAnswer,
				FailedTestCase: &tc,
			}, nil
		}

		runtimeMs := int(runRes.TimeUsed.Milliseconds())
		if runtimeMs > maxRuntimeMs {
			maxRuntimeMs = runtimeMs
		}
	}

	return Result{
		Status:    Accepted,
		RuntimeMs: maxRuntimeMs,
	}, nil
}
