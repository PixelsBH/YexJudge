package judge

import (
	"encoding/json"
	"strings"
	"testing"
)

func validFunctionJob() Job {
	return Job{
		Language:   "cpp",
		SourceCode: "class Solution {};",
		Function: &FunctionSpec{
			Name:       "value",
			ReturnType: "int",
		},
		TestCases: []TestCase{{
			ID:       1,
			Expected: json.RawMessage(`42`),
		}},
		Limits: Limits{TimeLimitMs: 1000, MemoryLimitMb: 128},
	}
}

func TestValidateJobAcceptsFunctionMetadataAndTypedJSON(t *testing.T) {
	job := validFunctionJob()
	job.Function.Params = []FunctionParam{{Name: "values", Type: "const vector<int>&"}}
	job.TestCases[0].Args = []json.RawMessage{json.RawMessage(`[1,2,3]`)}
	if err := ValidateJob(job); err != nil {
		t.Fatalf("ValidateJob() error = %v", err)
	}
}

func TestValidateJobRejectsMalformedFunctionValues(t *testing.T) {
	job := validFunctionJob()
	job.Function.Params = []FunctionParam{{Name: "value", Type: "int"}}
	job.TestCases[0].Args = []json.RawMessage{json.RawMessage(`"wrong"`)}
	if err := ValidateJob(job); err == nil || !strings.Contains(err.Error(), "wrong type") {
		t.Fatalf("ValidateJob() error = %v, want wrong type error", err)
	}
}

func TestValidateJobRejectsUnsafeFunctionMetadata(t *testing.T) {
	job := validFunctionJob()
	job.Function.Name = "value; system(\"bad\")"
	if err := ValidateJob(job); err == nil {
		t.Fatal("ValidateJob() accepted an unsafe function name")
	}
}

func TestValidateJobSupportsExplicitAndLegacyModes(t *testing.T) {
	legacy := validFunctionJob()
	if err := ValidateJob(legacy); err != nil {
		t.Fatalf("legacy function mode error = %v", err)
	}

	explicit := validFunctionJob()
	explicit.Mode = "function"
	if err := ValidateJob(explicit); err != nil {
		t.Fatalf("explicit function mode error = %v", err)
	}

	stdin := validFunctionJob()
	stdin.Mode = "stdin"
	if err := ValidateJob(stdin); err == nil {
		t.Fatal("ValidateJob() accepted function metadata in stdin mode")
	}

	unknown := validFunctionJob()
	unknown.Mode = "unknown"
	if err := ValidateJob(unknown); err == nil {
		t.Fatal("ValidateJob() accepted an unknown execution mode")
	}
}

func TestValidateJobAcceptsMutationObservations(t *testing.T) {
	job := validFunctionJob()
	job.Function.ReturnType = "int"
	job.Function.Params = []FunctionParam{{Name: "values", Type: "vector<int>&"}}
	job.Function.Observations = []ObservationSpec{
		{Kind: "return"},
		{Kind: "parameter", Parameter: 0, View: "prefix", LengthFromReturn: true},
	}
	job.TestCases[0].Args = []json.RawMessage{json.RawMessage(`[1,2,3]`)}
	job.TestCases[0].Expected = json.RawMessage(`{"return":1,"parameter":{"0":[1]}}`)
	if err := ValidateJob(job); err != nil {
		t.Fatalf("ValidateJob() error = %v", err)
	}
	got, err := testCaseExpectedOutput(job, job.TestCases[0])
	if err != nil {
		t.Fatalf("testCaseExpectedOutput() error = %v", err)
	}
	if want := `{"return":1,"parameter":{"0":[1]}}`; got != want {
		t.Fatalf("canonical expected = %q, want %q", got, want)
	}
}

func TestValidateJobAcceptsVoidMutationObservation(t *testing.T) {
	job := validFunctionJob()
	job.Function.ReturnType = "void"
	job.Function.Params = []FunctionParam{{Name: "values", Type: "vector<int>&"}}
	job.Function.Observations = []ObservationSpec{{Kind: "parameter", Parameter: 0}}
	job.TestCases[0].Args = []json.RawMessage{json.RawMessage(`[1,2]`)}
	job.TestCases[0].Expected = json.RawMessage(`{"parameter":{"0":[8,2]}}`)
	if err := ValidateJob(job); err != nil {
		t.Fatalf("ValidateJob() error = %v", err)
	}
}

func TestValidateJobRejectsObservationShapeMismatch(t *testing.T) {
	job := validFunctionJob()
	job.Function.Params = []FunctionParam{{Name: "values", Type: "vector<int>&"}}
	job.Function.Observations = []ObservationSpec{{Kind: "parameter", Parameter: 0}}
	job.TestCases[0].Args = []json.RawMessage{json.RawMessage(`[1,2]`)}
	job.TestCases[0].Expected = json.RawMessage(`2`)
	if err := ValidateJob(job); err == nil || !strings.Contains(err.Error(), "observation object") {
		t.Fatalf("ValidateJob() error = %v, want observation object error", err)
	}
}

func TestValidateJobRejectsVoidWithoutObservation(t *testing.T) {
	job := validFunctionJob()
	job.Function.ReturnType = "void"
	if err := ValidateJob(job); err == nil || !strings.Contains(err.Error(), "require at least one observation") {
		t.Fatalf("ValidateJob() error = %v, want void observation error", err)
	}
}

func TestValidateJobRejectsDuplicateFunctionCaseIDs(t *testing.T) {
	job := validFunctionJob()
	job.TestCases = append(job.TestCases, TestCase{ID: 1, Expected: json.RawMessage(`43`)})
	if err := ValidateJob(job); err == nil || !strings.Contains(err.Error(), "duplicate test case id") {
		t.Fatalf("ValidateJob() error = %v, want duplicate ID error", err)
	}
}

func TestValidateJobAcceptsCustomRuntimeTypesAndDisjointPostcondition(t *testing.T) {
	job := validFunctionJob()
	job.Function.Name = "copy"
	job.Function.ReturnType = "Node*"
	job.Function.Params = []FunctionParam{{Name: "head", Type: "Node*"}}
	job.Function.Postconditions = []PostconditionSpec{{
		Kind:          "disjoint",
		Subject:       "return",
		FromParameter: 0,
	}}
	job.TestCases[0].Args = []json.RawMessage{json.RawMessage(`{"values":[1,2],"random":[null,0]}`)}
	job.TestCases[0].Expected = json.RawMessage(`{"values":[1,2],"random":[null,0]}`)
	if err := ValidateJob(job); err != nil {
		t.Fatalf("ValidateJob() error = %v", err)
	}
	got, err := testCaseExpectedOutput(job, job.TestCases[0])
	if err != nil {
		t.Fatalf("testCaseExpectedOutput() error = %v", err)
	}
	if want := `{"return":{"values":[1,2],"random":[null,0]},"postconditions":{"0":true}}`; got != want {
		t.Fatalf("canonical expected = %q, want %q", got, want)
	}
}

func TestValidateJobRejectsUnsupportedCustomPostcondition(t *testing.T) {
	job := validFunctionJob()
	job.Function.ReturnType = "ListNode*"
	job.Function.Params = []FunctionParam{{Name: "head", Type: "ListNode*"}}
	job.Function.Postconditions = []PostconditionSpec{{
		Kind:          "same_address",
		Subject:       "return",
		FromParameter: 0,
	}}
	job.TestCases[0].Args = []json.RawMessage{json.RawMessage(`[1]`)}
	job.TestCases[0].Expected = json.RawMessage(`[1]`)
	if err := ValidateJob(job); err == nil || !strings.Contains(err.Error(), "unsupported kind") {
		t.Fatalf("ValidateJob() error = %v, want unsupported postcondition error", err)
	}
}

func TestValidateJobRejectsLimitsAndOversizedPayloads(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Job)
		want   string
	}{
		{name: "zero time limit", mutate: func(job *Job) { job.Limits.TimeLimitMs = 0 }, want: "timeLimitMs"},
		{name: "time limit too high", mutate: func(job *Job) { job.Limits.TimeLimitMs = MaxTimeLimitMs + 1 }, want: "timeLimitMs"},
		{name: "zero memory limit", mutate: func(job *Job) { job.Limits.MemoryLimitMb = 0 }, want: "memoryLimitMb"},
		{name: "memory limit too high", mutate: func(job *Job) { job.Limits.MemoryLimitMb = MaxMemoryLimitMb + 1 }, want: "memoryLimitMb"},
		{name: "source too large", mutate: func(job *Job) { job.SourceCode = strings.Repeat("x", 100_001) }, want: "sourceCode is too large"},
		{name: "too many test cases", mutate: func(job *Job) {
			job.TestCases = make([]TestCase, 101)
			for i := range job.TestCases {
				job.TestCases[i] = TestCase{ID: i + 1, ExpectedOutput: ""}
			}
		}, want: "too many test cases"},
		{name: "stdin input too large", mutate: func(job *Job) { job.TestCases[0].Input = strings.Repeat("x", 100_001) }, want: "input is too large"},
		{name: "stdin expected too large", mutate: func(job *Job) { job.TestCases[0].ExpectedOutput = strings.Repeat("x", 100_001) }, want: "expectedOutput is too large"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			job := Job{
				Language:   "python",
				SourceCode: "print(1)",
				TestCases:  []TestCase{{ID: 1, ExpectedOutput: ""}},
				Limits:     Limits{TimeLimitMs: 1000, MemoryLimitMb: 128},
			}
			test.mutate(&job)
			if err := ValidateJob(job); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateJob() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateJobAcceptsGenericClassMode(t *testing.T) {
	job := Job{
		Language:   "cpp",
		Mode:       "class",
		SourceCode: "class Counter {};",
		Class: &ClassSpec{
			Name: "Counter",
			Constructor: ClassConstructorSpec{
				Params: []FunctionParam{{Name: "initial", Type: "int"}},
			},
			Operations: []ClassOperationSpec{
				{Name: "add", ReturnType: "void", Params: []FunctionParam{{Name: "amount", Type: "int"}}},
				{Name: "get", ReturnType: "int"},
			},
		},
		TestCases: []TestCase{{
			ID:              1,
			ConstructorArgs: []json.RawMessage{json.RawMessage(`3`)},
			Operations: []OperationCall{
				{Name: "add", Args: []json.RawMessage{json.RawMessage(`4`)}},
				{Name: "get"},
			},
			Expected: json.RawMessage(`[null,7]`),
		}},
		Limits: Limits{TimeLimitMs: 1000, MemoryLimitMb: 128},
	}
	if err := ValidateJob(job); err != nil {
		t.Fatalf("ValidateJob() error = %v", err)
	}
	got, err := testCaseExpectedOutput(job, job.TestCases[0])
	if err != nil {
		t.Fatalf("testCaseExpectedOutput() error = %v", err)
	}
	if want := `[null,7]`; got != want {
		t.Fatalf("canonical class expected = %q, want %q", got, want)
	}
}
