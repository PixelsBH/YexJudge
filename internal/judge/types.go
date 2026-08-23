package judge

import (
	"encoding/json"
	"time"

	"yexjudge/internal/judge/harness"
)

type TestCase struct {
	ID              int               `json:"id"`
	Input           string            `json:"input,omitempty"`
	ExpectedOutput  string            `json:"expectedOutput,omitempty"`
	ActualOutput    string            `json:"actualOutput,omitempty"`
	Args            []json.RawMessage `json:"args,omitempty"`
	Expected        json.RawMessage   `json:"expected,omitempty"`
	ConstructorArgs []json.RawMessage `json:"constructorArgs,omitempty"`
	Operations      []OperationCall   `json:"operations,omitempty"`
}

type FunctionParam struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type FunctionSpec struct {
	Name           string              `json:"name"`
	ReturnType     string              `json:"returnType"`
	Params         []FunctionParam     `json:"params"`
	Observations   []ObservationSpec   `json:"observations,omitempty"`
	Postconditions []PostconditionSpec `json:"postconditions,omitempty"`
}

type ClassSpec struct {
	Name        string               `json:"name"`
	Constructor ClassConstructorSpec `json:"constructor"`
	Operations  []ClassOperationSpec `json:"operations"`
}

type ClassConstructorSpec struct {
	Params []FunctionParam `json:"params"`
}

type ClassOperationSpec struct {
	Name       string          `json:"name"`
	ReturnType string          `json:"returnType"`
	Params     []FunctionParam `json:"params"`
}

type OperationCall struct {
	Name string            `json:"name"`
	Args []json.RawMessage `json:"args"`
}

type ObservationSpec struct {
	Kind             string `json:"kind"`
	Parameter        int    `json:"parameter,omitempty"`
	View             string `json:"view,omitempty"`
	LengthFromReturn bool   `json:"lengthFromReturn,omitempty"`
}

type PostconditionSpec struct {
	Kind          string `json:"kind"`
	Subject       string `json:"subject"`
	FromParameter int    `json:"fromParameter,omitempty"`
}

type Limits struct {
	TimeLimitMs   int `json:"timeLimitMs"`
	MemoryLimitMb int `json:"memoryLimitMb"`
}

type Job struct {
	Language   string        `json:"language"`
	Mode       string        `json:"mode,omitempty"`
	SourceCode string        `json:"sourceCode"`
	Function   *FunctionSpec `json:"function,omitempty"`
	Class      *ClassSpec    `json:"class,omitempty"`
	TestCases  []TestCase    `json:"testCases"`
	Limits     Limits        `json:"limits"`
}

// ExecutionMode preserves the original payload behavior while allowing new
// clients to select a mode explicitly. Function/class metadata implies its
// corresponding mode when Mode is omitted; all other jobs default to stdin.
func (j Job) ExecutionMode() harness.ExecutionMode {
	if j.Mode != "" {
		return harness.ExecutionMode(j.Mode)
	}
	if j.Function != nil {
		return harness.ModeFunction
	}
	if j.Class != nil {
		return harness.ModeClass
	}
	return harness.ModeStdin
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
	CreatedAt      *time.Time       `json:"createdAt,omitempty"`
	ID             string           `json:"id"`
	Job            Job              `json:"job"`
	Status         SubmissionStatus `json:"status"`
	Result         *Result          `json:"result,omitempty"`
	StartedAt      *time.Time       `json:"startedAt,omitempty"`
	AttemptCount   int              `json:"attemptCount"`
	LeaseExpiresAt *time.Time       `json:"leaseExpiresAt,omitempty"`
	FailureMessage string           `json:"failureMessage,omitempty"`
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

type Sandbox struct {
	ContainerName string
	restarted     bool
	needsReplace  bool
}

const (
	Accepted            Status = "accepted"
	WrongAnswer         Status = "wrong_answer"
	TimeLimitExceeded   Status = "time_limit_exceeded"
	RuntimeError        Status = "runtime_error"
	CompilationError    Status = "compilation_error"
	MemoryLimitExceeded Status = "memory_limit_exceeded"
	OutputLimitExceeded Status = "output_limit_exceeded"
	InfrastructureError Status = "infrastructure_error"
	ValidationError     Status = "validation_error"
)

const (
	SubmissionQueued   SubmissionStatus = "queued"
	SubmissionRunning  SubmissionStatus = "running"
	SubmissionFinished SubmissionStatus = "finished"
	SubmissionFailed   SubmissionStatus = "failed"
)
