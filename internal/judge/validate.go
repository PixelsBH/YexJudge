package judge

import "fmt"

const (
	MaxTimeLimitMs   = 10_000
	MaxMemoryLimitMb = 512
)

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
	if job.Limits.TimeLimitMs > MaxTimeLimitMs {
		return fmt.Errorf("timeLimitMs must not exceed %d", MaxTimeLimitMs)
	}

	if job.Limits.MemoryLimitMb <= 0 {
		return fmt.Errorf("memoryLimitMb must be greater than 0")
	}
	if job.Limits.MemoryLimitMb > MaxMemoryLimitMb {
		return fmt.Errorf("memoryLimitMb must not exceed %d", MaxMemoryLimitMb)
	}

	if len(job.SourceCode) > 100_000 {
		return fmt.Errorf("sourceCode is too large")
	}

	if len(job.TestCases) > 100 {
		return fmt.Errorf("too many test cases")
	}

	if job.Function != nil {
		return validateFunctionJob(job)
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

func validateFunctionJob(job Job) error {
	if job.Language != "cpp" {
		return fmt.Errorf("function mode currently supports cpp only")
	}

	if job.Function.Name == "" {
		return fmt.Errorf("function name is required")
	}

	if job.Function.ReturnType == "" {
		return fmt.Errorf("function returnType is required")
	}
	if !isSupportedCppFunctionType(job.Function.ReturnType) {
		return fmt.Errorf("unsupported function returnType %q", job.Function.ReturnType)
	}

	if len(job.Function.Params) == 0 {
		return fmt.Errorf("function params are required")
	}

	if len(job.Function.Params) > 10 {
		return fmt.Errorf("too many function params")
	}

	for i, param := range job.Function.Params {
		if param.Name == "" {
			return fmt.Errorf("function param %d name is required", i)
		}
		if param.Type == "" {
			return fmt.Errorf("function param %d type is required", i)
		}
		if !isSupportedCppFunctionType(param.Type) {
			return fmt.Errorf("unsupported function param %d type %q", i, param.Type)
		}
	}

	for i, tc := range job.TestCases {
		if len(tc.Args) != len(job.Function.Params) {
			return fmt.Errorf("test case %d args count must match function params count", i)
		}
		if len(tc.Expected) == 0 {
			return fmt.Errorf("test case %d expected is required", i)
		}

		totalSize := len(tc.Expected)
		for _, arg := range tc.Args {
			totalSize += len(arg)
		}
		if totalSize > 100_000 {
			return fmt.Errorf("test case %d is too large", i)
		}
	}

	return nil
}

func isSupportedCppFunctionType(cppType string) bool {
	switch cppStorageType(cppType) {
	case "int",
		"long long",
		"double",
		"bool",
		"string",
		"vector<int>",
		"vector<long long>",
		"vector<double>",
		"vector<bool>",
		"vector<string>":
		return true
	default:
		return false
	}
}
