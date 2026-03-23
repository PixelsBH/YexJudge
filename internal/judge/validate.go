package judge

import "fmt"

func ValidateJob(job Job) error {
	if job.Language == "" {
		return fmt.Errorf("language is required")
	}

	if job.SourceCode == "" {
		return fmt.Errorf("sourceCode is required")
	}

	if len(job.TestCases) == 0 {
		return fmt.Errorf("at least one test case is required")
	}

	if job.Limits.TimeLimitMs <= 0 {
		return fmt.Errorf("timeLimitMs must be greater than 0")
	}

	if job.Limits.MemoryLimitMb <= 0 {
		return fmt.Errorf("memoryLimitMb must be greater than 0")
	}

	if len(job.SourceCode) > 100_000 {
		return fmt.Errorf("sourceCode is too large")
	}

	if len(job.TestCases) > 100 {
		return fmt.Errorf("too many test cases")
	}

	for i, tc := range job.TestCases {
		if len(tc.Input) > 100_000 {
			return fmt.Errorf("test case %d input is too large", i)
		}

		if len(tc.ExpectedOutput) > 100_000 {
			return fmt.Errorf("test case %d expectedOutput is too large", i)
		}
	}

	return nil
}
