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

type SubmissionStatus string

type Result struct {
	Status         Status    `json:"status"`
	RuntimeMs      int       `json:"runtimeMs,omitempty"`
	MemoryMb       int       `json:"memoryMb,omitempty"`
	FailedTestCase *TestCase `json:"failedTestCase,omitempty"`
	ErrorMessage   string    `json:"errorMessage,omitempty"`
}

type Submission struct {
	ID     string           `json:"id"`
	Job    Job              `json:"job"`
	Status SubmissionStatus `json:"status"`
	Result *Result          `json:"result,omitempty"`
}

type SubmissionAcceptedResponse struct {
	SubmissionID string           `json:"submissionId"`
	Status       SubmissionStatus `json:"status"`
}

type SubmissionResponse struct {
	ID     string           `json:"id"`
	Status SubmissionStatus `json:"status"`
	Result *Result          `json:"result,omitempty"`
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

const (
	SubmissionQueued   SubmissionStatus = "queued"
	SubmissionRunning  SubmissionStatus = "running"
	SubmissionFinished SubmissionStatus = "finished"
	SubmissionFailed   SubmissionStatus = "failed"
)
