package judge

type TestCase struct {
	ID             int    `json:"id"`
	Input          string `json:"input"`
	ExpectedOutput string `json:"expectedOutput"`
}

type Limits struct {
	TimeLimitMs   int `json:"timeLimitMs"`
	MemoryLimitMb int `json:"memoryLimitMb"`
}

type Job struct {
	Language   string     `json:"language"`
	SourceCode string     `json:"sourceCode"`
	TestCases  []TestCase `json:"testCases"`
	Limits     Limits     `json:"limits"`
}

type Status string

type Result struct {
	Status         Status    `json:"status"`
	RuntimeMs      int       `json:"runtimeMs,omitempty"`
	MemoryMb       int       `json:"memoryMb,omitempty"`
	FailedTestCase *TestCase `json:"failedTestCase,omitempty"`
	ErrorMessage   string    `json:"errorMessage,omitempty"`
}

const (
	Accepted            Status = "accepted"
	WrongAnswer         Status = "wrong_answer"
	TimeLimitExceeded   Status = "time_limit_exceeded"
	RuntimeError        Status = "runtime_error"
	CompilationError    Status = "compilation_error"
	MemoryLimitExceeded Status = "memory_limit_exceeded"
	ValidationError     Status = "validation_error"
)
