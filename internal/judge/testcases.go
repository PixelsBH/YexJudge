package judge

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"yexjudge/internal/judge/languages"
)

func runTestCases(
	ctx context.Context, executor Executor,
	sandbox *Sandbox, job Job, spec languages.Spec) (Result, error) {
	maxRuntimeMs := 0

	for _, tc := range job.TestCases {
		ctxRun, cancelRun := context.WithTimeout(
			ctx,
			time.Duration(job.Limits.TimeLimitMs)*time.Millisecond,
		)

		runRes, err := executor.RunTestCase(
			ctxRun,
			sandbox,
			testCaseInput(job, tc),
			spec,
		)

		cancelRun()

		if err != nil {
			return Result{}, err
		}

		if runRes.TimedOut {
			return Result{
				Status:         TimeLimitExceeded,
				FailedTestCase: failedTestCase(tc, runRes.Stdout),
			}, nil
		}

		if runRes.ExitCode != 0 {
			return Result{
				Status:         RuntimeError,
				FailedTestCase: failedTestCase(tc, runRes.Stdout),
				ErrorMessage:   runRes.Stderr,
			}, nil
		}

		output := strings.TrimSpace(runRes.Stdout)
		expected, err := testCaseExpectedOutput(job, tc)
		if err != nil {
			return Result{}, err
		}
		expected = strings.TrimSpace(expected)

		if output != expected {
			return Result{
				Status:         WrongAnswer,
				FailedTestCase: failedTestCase(tc, output),
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

func failedTestCase(tc TestCase, actualOutput string) *TestCase {
	tc.ActualOutput = strings.TrimSpace(actualOutput)
	return &tc
}

func testCaseInput(job Job, tc TestCase) string {
	if job.Function != nil {
		return strconv.Itoa(tc.ID)
	}

	return tc.Input
}

func testCaseExpectedOutput(job Job, tc TestCase) (string, error) {
	if job.Function == nil {
		return tc.ExpectedOutput, nil
	}

	expected, err := normalizeExpectedJSON(tc.Expected)
	if err != nil {
		return "", fmt.Errorf("test case %d expected: %w", tc.ID, err)
	}

	return expected, nil
}
