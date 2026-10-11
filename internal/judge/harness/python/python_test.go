package python

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"yexjudge/internal/judge/harness"
)

func runGeneratedPython(t *testing.T, request harness.ModeRequest, input string) string {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	var generator harness.ModeGenerator
	switch request.Contract.Mode {
	case harness.ModeFunction:
		generator = NewFunctionGenerator()
	case harness.ModeClass:
		generator = NewClassGenerator()
	default:
		t.Fatalf("unsupported test mode %q", request.Contract.Mode)
	}
	program, err := generator.GenerateMode(request)
	if err != nil {
		t.Fatalf("GenerateMode() error = %v", err)
	}
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, program.SourceFile)
	if err := os.WriteFile(sourcePath, []byte(program.SourceCode), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(python, sourcePath)
	command.Stdin = strings.NewReader(input)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("generated Python harness failed: %v\n%s\n--- generated source ---\n%s", err, output, program.SourceCode)
	}
	return strings.TrimSpace(string(output))
}

func TestPythonFunctionHarnessSupportsRecursiveValues(t *testing.T) {
	got := runGeneratedPython(t, harness.ModeRequest{
		SourceCode: `class Solution:
    def sumNested(self, values: list[list[int]]) -> int:
        return sum(sum(row) for row in values)`,
		Contract: harness.ExecutionContract{
			Mode: harness.ModeFunction,
			Function: &harness.FunctionSpec{
				Name: "sumNested", ReturnType: "long long",
				Params: []harness.Parameter{{Name: "values", Type: "vector<vector<int>>"}},
			},
		},
		TestCases: []harness.TestCase{{ID: 4, Args: []json.RawMessage{json.RawMessage(`[[1,2],[3,4]]`)}, Expected: json.RawMessage(`10`)}},
	}, "4\n")
	if got != "10" {
		t.Fatalf("generated function output = %q, want 10", got)
	}
}

func TestPythonFunctionHarnessReportsMutationObservations(t *testing.T) {
	got := runGeneratedPython(t, harness.ModeRequest{
		SourceCode: `class Solution:
    def compact(self, values, other):
        values[1] = 8
        values[2] = 9
        other[0] = 4
        return 2`,
		Contract: harness.ExecutionContract{
			Mode: harness.ModeFunction,
			Function: &harness.FunctionSpec{
				Name: "compact", ReturnType: "int",
				Params: []harness.Parameter{{Name: "values", Type: "vector<int>&"}, {Name: "other", Type: "vector<int>&"}},
				Observations: []harness.Observation{
					{Kind: "return"},
					{Kind: "parameter", Parameter: 0, View: "prefix", LengthFromReturn: true},
					{Kind: "parameter", Parameter: 1},
				},
			},
		},
		TestCases: []harness.TestCase{{ID: 8, Args: []json.RawMessage{json.RawMessage(`[1,2,3]`), json.RawMessage(`[3,4]`)}, Expected: json.RawMessage(`{"return":2,"parameter":{"0":[1,8],"1":[4,4]}}`)}},
	}, "8\n")
	var actual map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &actual); err != nil {
		t.Fatalf("generated function output %q is not JSON: %v", got, err)
	}
	if string(actual["return"]) != "2" || string(actual["parameter"]) != `{"0":[1,8],"1":[4,4]}` {
		t.Fatalf("generated mutation output = %s, want return 2 and both parameter observations", got)
	}
}

func TestPythonFunctionHarnessBuildsAndChecksRuntimeIdentity(t *testing.T) {
	got := runGeneratedPython(t, harness.ModeRequest{
		SourceCode: `class Solution:
    def clone(self, head):
        if head is None:
            return None
        result = ListNode(head.val)
        source, target = head.next, result
        while source is not None:
            target.next = ListNode(source.val)
            target, source = target.next, source.next
        return result`,
		Contract: harness.ExecutionContract{
			Mode: harness.ModeFunction,
			Function: &harness.FunctionSpec{
				Name: "clone", ReturnType: "ListNode*",
				Params:         []harness.Parameter{{Name: "head", Type: "ListNode*"}},
				Postconditions: []harness.Postcondition{{Kind: "disjoint", Subject: "return", FromParameter: 0}},
			},
		},
		TestCases: []harness.TestCase{{ID: 9, Args: []json.RawMessage{json.RawMessage(`[1,2,3]`)}, Expected: json.RawMessage(`[1,2,3]`)}},
	}, "9\n")
	var actual map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &actual); err != nil {
		t.Fatalf("generated runtime output %q is not JSON: %v", got, err)
	}
	if string(actual["return"]) != `[1,2,3]` || !bytes.Contains(actual["postconditions"], []byte(`"0":true`)) {
		t.Fatalf("generated runtime output = %s, want a cloned list and true disjointness", got)
	}
}

func TestPythonFunctionHarnessReportsSameObjectIdentity(t *testing.T) {
	got := runGeneratedPython(t, harness.ModeRequest{
		SourceCode: `class Solution:
    def keep(self, head):
        return head`,
		Contract: harness.ExecutionContract{
			Mode: harness.ModeFunction,
			Function: &harness.FunctionSpec{
				Name: "keep", ReturnType: "ListNode*",
				Params:         []harness.Parameter{{Name: "head", Type: "ListNode*"}},
				Postconditions: []harness.Postcondition{{Kind: "same_as", Subject: "return", FromParameter: 0}},
			},
		},
		TestCases: []harness.TestCase{{ID: 6, Args: []json.RawMessage{json.RawMessage(`[1,2]`)}, Expected: json.RawMessage(`[1,2]`)}},
	}, "6\n")
	var actual map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &actual); err != nil {
		t.Fatalf("generated identity output %q is not JSON: %v", got, err)
	}
	if !bytes.Contains(actual["postconditions"], []byte(`"0":true`)) {
		t.Fatalf("generated identity output = %s, want true same_as postcondition", got)
	}
}

func TestPythonRuntimeTypesRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		typeName string
		value    string
		want     string
	}{
		{name: "tree", typeName: "TreeNode*", value: `[1,2,3,null,null,4,5]`},
		{name: "random list", typeName: "RandomListNode*", value: `{"values":[7,13],"random":[null,0]}`},
		{name: "random node", typeName: "Node*", value: `{"values":[1,2],"random":[1,null]}`},
		{name: "graph", typeName: "GraphNode*", value: `{"values":[1,2,3],"neighbors":[[2,1],[0],[0]]}`, want: `{"values":[1,3,2],"neighbors":[[1,2],[0],[0]]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := runGeneratedPython(t, harness.ModeRequest{
				SourceCode: `class Solution:
    def echo(self, root):
        return root`,
				Contract: harness.ExecutionContract{
					Mode: harness.ModeFunction,
					Function: &harness.FunctionSpec{
						Name: "echo", ReturnType: test.typeName,
						Params: []harness.Parameter{{Name: "root", Type: test.typeName}},
					},
				},
				TestCases: []harness.TestCase{{ID: 5, Args: []json.RawMessage{json.RawMessage(test.value)}, Expected: json.RawMessage(test.value)}},
			}, "5\n")
			want := test.want
			if want == "" {
				want = test.value
			}
			if got != want {
				t.Fatalf("round-trip output = %s, want %s", got, want)
			}
		})
	}
}

func TestPythonClassHarnessExecutesOperationSequences(t *testing.T) {
	got := runGeneratedPython(t, harness.ModeRequest{
		SourceCode: `class Counter:
    def __init__(self, initial):
        self.value = initial
    def add(self, amount):
        self.value += amount
    def get(self):
        return self.value`,
		Contract: harness.ExecutionContract{
			Mode: harness.ModeClass,
			Class: &harness.ClassSpec{
				Name:        "Counter",
				Constructor: harness.ConstructorSpec{Params: []harness.Parameter{{Name: "initial", Type: "int"}}},
				Operations: []harness.OperationSpec{
					{Name: "add", ReturnType: "void", Params: []harness.Parameter{{Name: "amount", Type: "int"}}},
					{Name: "get", ReturnType: "int"},
				},
			},
		},
		TestCases: []harness.TestCase{{
			ID:              12,
			ConstructorArgs: []json.RawMessage{json.RawMessage(`3`)},
			Operations: []harness.OperationCall{
				{Name: "add", Args: []json.RawMessage{json.RawMessage(`4`)}},
				{Name: "get"},
			},
			Expected: json.RawMessage(`[null,7]`),
		}},
	}, "12\n")
	if got != `[null,7]` {
		t.Fatalf("generated class output = %q, want [null,7]", got)
	}
}

func TestPythonMetadataRejectsUnsupportedTypes(t *testing.T) {
	_, err := NewFunctionGenerator().GenerateMode(harness.ModeRequest{
		SourceCode: "class Solution: pass",
		Contract: harness.ExecutionContract{
			Mode:     harness.ModeFunction,
			Function: &harness.FunctionSpec{Name: "run", ReturnType: "set<int>"},
		},
		TestCases: []harness.TestCase{{ID: 1, Expected: json.RawMessage(`[]`)}},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("GenerateMode() error = %v, want an unsupported-type validation error", err)
	}
}
