package cpp

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"yexjudge/internal/judge/harness"
)

func TestGeneratedClassHarnessExecutesGenericOperationSequence(t *testing.T) {
	gxx, err := exec.LookPath("g++")
	if err != nil {
		t.Skip("g++ is not installed")
	}

	source, err := NewClassGenerator(nil).Generate(ClassRequest{
		SourceCode: `class Counter {
	int value;
public:
	Counter(int initial) : value(initial) {}
	void add(int amount) { value += amount; }
	int get() { return value; }
};`,
		Class: harness.ClassSpec{
			Name: "Counter",
			Constructor: harness.ConstructorSpec{
				Params: []harness.Parameter{{Name: "initial", Type: "int"}},
			},
			Operations: []harness.OperationSpec{
				{Name: "add", ReturnType: "void", Params: []harness.Parameter{{Name: "amount", Type: "int"}}},
				{Name: "get", ReturnType: "int"},
			},
		},
		TestCases: []harness.TestCase{{
			ID:              1,
			ConstructorArgs: []json.RawMessage{json.RawMessage(`3`)},
			Operations: []harness.OperationCall{
				{Name: "add", Args: []json.RawMessage{json.RawMessage(`4`)}},
				{Name: "get"},
				{Name: "add", Args: []json.RawMessage{json.RawMessage(`-2`)}},
				{Name: "get"},
			},
			Expected: json.RawMessage(`[null,7,null,5]`),
		}},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "main.cpp")
	binaryPath := filepath.Join(directory, "main")
	if err := os.WriteFile(sourcePath, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	compile := exec.Command(gxx, "-std=c++17", sourcePath, "-o", binaryPath)
	if output, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("generated class source did not compile: %v\n%s", err, output)
	}

	run := exec.Command(binaryPath)
	run.Stdin = strings.NewReader("1\n")
	output, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("generated class harness failed: %v\n%s", err, output)
	}
	if got, want := string(output), `[null,7,null,5]`; got != want {
		t.Fatalf("class harness output = %q, want %q", got, want)
	}
}

func TestClassGeneratorRejectsUnknownOperationCalls(t *testing.T) {
	_, err := NewClassGenerator(nil).Generate(ClassRequest{
		SourceCode: "class Counter {};",
		Class: harness.ClassSpec{
			Name:       "Counter",
			Operations: []harness.OperationSpec{{Name: "get", ReturnType: "int"}},
		},
		TestCases: []harness.TestCase{{
			ID:              1,
			Operations:      []harness.OperationCall{{Name: "missing"}},
			Expected:        json.RawMessage(`[0]`),
			ConstructorArgs: []json.RawMessage{},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("Generate() error = %v, want undeclared operation error", err)
	}
}
