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

func TestFunctionGeneratorBuildsGenericDriver(t *testing.T) {
	generator := NewFunctionGenerator(nil)
	source, err := generator.Generate(harness.Request{
		SourceCode: `class Solution {
public:
    vector<int> transform(vector<int>& values, bool enabled) {
        if (!enabled) return {};
        return values;
    }
};`,
		Function: harness.FunctionSpec{
			Name:       "transform",
			ReturnType: "vector<int>",
			Params: []harness.Parameter{
				{Name: "values", Type: "const vector<int>&"},
				{Name: "enabled", Type: "bool"},
			},
		},
		TestCases: []harness.TestCase{{
			ID:       7,
			Args:     []json.RawMessage{json.RawMessage(`[1,2,3]`), json.RawMessage(`true`)},
			Expected: json.RawMessage(`[1,2,3]`),
		}},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	for _, expected := range []string{
		"#include <bits/stdc++.h>",
		"class Solution",
		"vector<int> values = vector<int>{1, 2, 3};",
		"bool enabled = true;",
		"__solution.transform(values, enabled)",
		"cout << __serialize(__result);",
		"case 7:",
	} {
		if !strings.Contains(source, expected) {
			t.Errorf("generated source does not contain %q", expected)
		}
	}
}

func TestGeneratedFunctionHarnessCompilesAndRuns(t *testing.T) {
	gxx, err := exec.LookPath("g++")
	if err != nil {
		t.Skip("g++ is not installed")
	}

	source, err := NewFunctionGenerator(nil).Generate(harness.Request{
		SourceCode: `class Solution {
public:
    vector<int> transform(vector<int>& values) { return values; }
};`,
		Function: harness.FunctionSpec{
			Name:       "transform",
			ReturnType: "vector<int>",
			Params:     []harness.Parameter{{Name: "values", Type: "vector<int>&"}},
		},
		TestCases: []harness.TestCase{{
			ID:       3,
			Args:     []json.RawMessage{json.RawMessage(`[4,5]`)},
			Expected: json.RawMessage(`[4,5]`),
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
		t.Fatalf("generated source did not compile: %v\n%s", err, output)
	}

	run := exec.Command(binaryPath)
	run.Stdin = strings.NewReader("3\n")
	output, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("generated harness failed: %v\n%s", err, output)
	}
	if got, want := string(output), `[4,5]`; got != want {
		t.Fatalf("harness output = %q, want %q", got, want)
	}
}

func TestFunctionGeneratorEmitsMutationObservations(t *testing.T) {
	source, err := NewFunctionGenerator(nil).Generate(harness.Request{
		SourceCode: `class Solution {
public:
    int compact(vector<int>& values) {
        values[0] = 9;
        return 1;
    }
};`,
		Function: harness.FunctionSpec{
			Name:       "compact",
			ReturnType: "int",
			Params:     []harness.Parameter{{Name: "values", Type: "vector<int>&"}},
			Observations: []harness.Observation{{Kind: "return"}, {
				Kind:             "parameter",
				Parameter:        0,
				View:             "prefix",
				LengthFromReturn: true,
			}},
		},
		TestCases: []harness.TestCase{{
			ID:       1,
			Args:     []json.RawMessage{json.RawMessage(`[1,2,3]`)},
			Expected: json.RawMessage(`{"return":1,"parameter":{"0":[9]}}`),
		}},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	for _, expected := range []string{
		"__solution.compact(values)",
		"\\\"return\\\"",
		"__serialize_prefix(values, __result)",
		"\\\"parameter\\\"",
	} {
		if !strings.Contains(source, expected) {
			t.Errorf("generated source does not contain %q", expected)
		}
	}
}

func TestGeneratedMutationObservationHarnessCompilesAndRuns(t *testing.T) {
	gxx, err := exec.LookPath("g++")
	if err != nil {
		t.Skip("g++ is not installed")
	}

	source, err := NewFunctionGenerator(nil).Generate(harness.Request{
		SourceCode: `class Solution { public: int compact(vector<int>& values) { values[0] = 9; return 1; } };`,
		Function: harness.FunctionSpec{
			Name:       "compact",
			ReturnType: "int",
			Params:     []harness.Parameter{{Name: "values", Type: "vector<int>&"}},
			Observations: []harness.Observation{
				{Kind: "return"},
				{Kind: "parameter", Parameter: 0, View: "prefix", LengthFromReturn: true},
			},
		},
		TestCases: []harness.TestCase{{
			ID:       4,
			Args:     []json.RawMessage{json.RawMessage(`[1,2,3]`)},
			Expected: json.RawMessage(`{"return":1,"parameter":{"0":[9]}}`),
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
		t.Fatalf("generated mutation source did not compile: %v\n%s", err, output)
	}

	run := exec.Command(binaryPath)
	run.Stdin = strings.NewReader("4\n")
	output, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("generated mutation harness failed: %v\n%s", err, output)
	}
	if got, want := string(output), `{"return":1,"parameter":{"0":[9]}}`; got != want {
		t.Fatalf("mutation harness output = %q, want %q", got, want)
	}
}

func TestFunctionGeneratorSupportsVoidMutationObservation(t *testing.T) {
	_, err := NewFunctionGenerator(nil).Generate(harness.Request{
		SourceCode: `class Solution { public: void mutate(vector<int>& values) { values[0] = 8; } };`,
		Function: harness.FunctionSpec{
			Name:       "mutate",
			ReturnType: "void",
			Params:     []harness.Parameter{{Name: "values", Type: "vector<int>&"}},
			Observations: []harness.Observation{{
				Kind:      "parameter",
				Parameter: 0,
			}},
		},
		TestCases: []harness.TestCase{{
			ID:       1,
			Args:     []json.RawMessage{json.RawMessage(`[1,2]`)},
			Expected: json.RawMessage(`{"parameter":{"0":[8,2]}}`),
		}},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
}

func TestFunctionGeneratorSupportsZeroParameterFunctions(t *testing.T) {
	generator := NewFunctionGenerator(nil)
	_, err := generator.Generate(harness.Request{
		SourceCode: `class Solution { public: int value() { return 42; } };`,
		Function: harness.FunctionSpec{
			Name:       "value",
			ReturnType: "int",
		},
		TestCases: []harness.TestCase{{
			ID:       1,
			Expected: json.RawMessage(`42`),
		}},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
}

func TestGeneratedListTreeAndRandomListHarnesses(t *testing.T) {
	gxx, err := exec.LookPath("g++")
	if err != nil {
		t.Skip("g++ is not installed")
	}

	runGenerated := func(name, source, input, want string) {
		t.Helper()
		directory := t.TempDir()
		sourcePath := filepath.Join(directory, "main.cpp")
		binaryPath := filepath.Join(directory, "main")
		if err := os.WriteFile(sourcePath, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		compile := exec.Command(gxx, "-std=c++17", sourcePath, "-o", binaryPath)
		if output, err := compile.CombinedOutput(); err != nil {
			t.Fatalf("%s generated source did not compile: %v\\n%s", name, err, output)
		}
		run := exec.Command(binaryPath)
		run.Stdin = strings.NewReader(input)
		output, err := run.CombinedOutput()
		if err != nil {
			t.Fatalf("%s generated harness failed: %v\\n%s", name, err, output)
		}
		if got := string(output); got != want {
			t.Fatalf("%s harness output = %q, want %q", name, got, want)
		}
	}

	listSource, err := NewFunctionGenerator(nil).Generate(harness.Request{
		SourceCode: `class Solution {
public:
    ListNode* reverse(ListNode* head) {
        ListNode* previous = nullptr;
        while (head != nullptr) {
            ListNode* next = head->next;
            head->next = previous;
            previous = head;
            head = next;
        }
        return previous;
    }
};`,
		Function: harness.FunctionSpec{
			Name:       "reverse",
			ReturnType: "ListNode*",
			Params:     []harness.Parameter{{Name: "head", Type: "ListNode*"}},
		},
		TestCases: []harness.TestCase{{
			ID:       1,
			Args:     []json.RawMessage{json.RawMessage(`[1,2,3]`)},
			Expected: json.RawMessage(`[3,2,1]`),
		}},
	})
	if err != nil {
		t.Fatalf("list Generate() error = %v", err)
	}
	runGenerated("list", listSource, "1\\n", `[3,2,1]`)

	treeSource, err := NewFunctionGenerator(nil).Generate(harness.Request{
		SourceCode: `class Solution {
public:
    TreeNode* invert(TreeNode* root) {
        if (root == nullptr) return nullptr;
        swap(root->left, root->right);
        invert(root->left);
        invert(root->right);
        return root;
    }
};`,
		Function: harness.FunctionSpec{
			Name:       "invert",
			ReturnType: "TreeNode*",
			Params:     []harness.Parameter{{Name: "root", Type: "TreeNode*"}},
		},
		TestCases: []harness.TestCase{{
			ID:       2,
			Args:     []json.RawMessage{json.RawMessage(`[1,2,3,null,4]`)},
			Expected: json.RawMessage(`[1,3,2,null,null,4]`),
		}},
	})
	if err != nil {
		t.Fatalf("tree Generate() error = %v", err)
	}
	runGenerated("tree", treeSource, "2\\n", `[1,3,2,null,null,4]`)

	randomSource, err := NewFunctionGenerator(nil).Generate(harness.Request{
		SourceCode: `class Solution {
public:
    Node* clone(Node* head) {
        if (head == nullptr) return nullptr;
        unordered_map<Node*, Node*> copies;
        for (Node* node = head; node != nullptr; node = node->next) {
            copies[node] = new Node(node->val);
        }
        for (Node* node = head; node != nullptr; node = node->next) {
            copies[node]->next = node->next == nullptr ? nullptr : copies[node->next];
            copies[node]->random = node->random == nullptr ? nullptr : copies[node->random];
        }
        return copies[head];
    }
};`,
		Function: harness.FunctionSpec{
			Name:       "clone",
			ReturnType: "Node*",
			Params:     []harness.Parameter{{Name: "head", Type: "Node*"}},
			Postconditions: []harness.Postcondition{{
				Kind:          "disjoint",
				Subject:       "return",
				FromParameter: 0,
			}},
		},
		TestCases: []harness.TestCase{{
			ID:       3,
			Args:     []json.RawMessage{json.RawMessage(`{"values":[7,13,11,10,1],"random":[null,0,4,2,0]}`)},
			Expected: json.RawMessage(`{"values":[7,13,11,10,1],"random":[null,0,4,2,0]}`),
		}},
	})
	if err != nil {
		t.Fatalf("random-list Generate() error = %v", err)
	}
	runGenerated("random-list", randomSource, "3\n", `{"return":{"values":[7,13,11,10,1],"random":[null,0,4,2,0]},"postconditions":{"0":true}}`)

	aliasSource, err := NewFunctionGenerator(nil).Generate(harness.Request{
		SourceCode: `class Solution { public: Node* clone(Node* head) { return head; } };`,
		Function: harness.FunctionSpec{
			Name:       "clone",
			ReturnType: "Node*",
			Params:     []harness.Parameter{{Name: "head", Type: "Node*"}},
			Postconditions: []harness.Postcondition{{
				Kind:          "disjoint",
				Subject:       "return",
				FromParameter: 0,
			}},
		},
		TestCases: []harness.TestCase{{
			ID:       4,
			Args:     []json.RawMessage{json.RawMessage(`{"values":[1,2],"random":[null,0]}`)},
			Expected: json.RawMessage(`{"values":[1,2],"random":[null,0]}`),
		}},
	})
	if err != nil {
		t.Fatalf("alias Generate() error = %v", err)
	}
	runGenerated("alias", aliasSource, "4\n", `{"return":{"values":[1,2],"random":[null,0]},"postconditions":{"0":false}}`)
}

func TestGeneratedGraphHarnessCompilesAndRuns(t *testing.T) {
	gxx, err := exec.LookPath("g++")
	if err != nil {
		t.Skip("g++ is not installed")
	}
	source, err := NewFunctionGenerator(nil).Generate(harness.Request{
		SourceCode: `class Solution {
public:
    GraphNode* clone(GraphNode* root) {
        if (root == nullptr) return nullptr;
        unordered_map<GraphNode*, GraphNode*> copies;
        queue<GraphNode*> pending;
        pending.push(root);
        copies[root] = new GraphNode(root->val);
        while (!pending.empty()) {
            GraphNode* node = pending.front();
            pending.pop();
            for (GraphNode* neighbor : node->neighbors) {
                if (!copies.count(neighbor)) {
                    copies[neighbor] = new GraphNode(neighbor->val);
                    pending.push(neighbor);
                }
                copies[node]->neighbors.push_back(copies[neighbor]);
            }
        }
        return copies[root];
    }
};`,
		Function: harness.FunctionSpec{
			Name:       "clone",
			ReturnType: "GraphNode*",
			Params:     []harness.Parameter{{Name: "root", Type: "GraphNode*"}},
			Postconditions: []harness.Postcondition{{
				Kind:          "disjoint",
				Subject:       "return",
				FromParameter: 0,
			}},
		},
		TestCases: []harness.TestCase{{
			ID:       1,
			Args:     []json.RawMessage{json.RawMessage(`{"values":[1,2,3],"neighbors":[[1,2],[2],[0]]}`)},
			Expected: json.RawMessage(`{"values":[1,2,3],"neighbors":[[1,2],[2],[0]]}`),
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
		t.Fatalf("graph generated source did not compile: %v\n%s", err, output)
	}
	run := exec.Command(binaryPath)
	run.Stdin = strings.NewReader("1\n")
	output, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("graph generated harness failed: %v\n%s", err, output)
	}
	if got, want := string(output), `{"return":{"values":[1,2,3],"neighbors":[[1,2],[2],[0]]},"postconditions":{"0":true}}`; got != want {
		t.Fatalf("graph harness output = %q, want %q", got, want)
	}
}

func TestGeneratedCustomListMutationObservation(t *testing.T) {
	gxx, err := exec.LookPath("g++")
	if err != nil {
		t.Skip("g++ is not installed")
	}
	source, err := NewFunctionGenerator(nil).Generate(harness.Request{
		SourceCode: `class Solution { public: void update(ListNode* head) { if (head != nullptr) head->val = 9; } };`,
		Function: harness.FunctionSpec{
			Name:       "update",
			ReturnType: "void",
			Params:     []harness.Parameter{{Name: "head", Type: "ListNode*"}},
			Observations: []harness.Observation{{
				Kind:      "parameter",
				Parameter: 0,
			}},
		},
		TestCases: []harness.TestCase{{
			ID:       1,
			Args:     []json.RawMessage{json.RawMessage(`[1,2]`)},
			Expected: json.RawMessage(`{"parameter":{"0":[9,2]}}`),
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
		t.Fatalf("custom mutation source did not compile: %v\n%s", err, output)
	}
	run := exec.Command(binaryPath)
	run.Stdin = strings.NewReader("1\n")
	output, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("custom mutation harness failed: %v\n%s", err, output)
	}
	if got, want := string(output), `{"parameter":{"0":[9,2]}}`; got != want {
		t.Fatalf("custom mutation output = %q, want %q", got, want)
	}
}

func TestGeneratedOptionalHarnessCompilesAndRuns(t *testing.T) {
	gxx, err := exec.LookPath("g++")
	if err != nil {
		t.Skip("g++ is not installed")
	}
	source, err := NewFunctionGenerator(nil).Generate(harness.Request{
		SourceCode: `class Solution {
public:
    optional<int> maybe(int value) {
        if (value < 0) return nullopt;
        return value;
    }
};`,
		Function: harness.FunctionSpec{
			Name:       "maybe",
			ReturnType: "optional<int>",
			Params:     []harness.Parameter{{Name: "value", Type: "int"}},
		},
		TestCases: []harness.TestCase{
			{ID: 1, Args: []json.RawMessage{json.RawMessage(`-1`)}, Expected: json.RawMessage(`null`)},
			{ID: 2, Args: []json.RawMessage{json.RawMessage(`4`)}, Expected: json.RawMessage(`4`)},
		},
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
		t.Fatalf("optional generated source did not compile: %v\n%s", err, output)
	}
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "1\n", want: "null"},
		{input: "2\n", want: "4"},
	} {
		run := exec.Command(binaryPath)
		run.Stdin = strings.NewReader(test.input)
		output, err := run.CombinedOutput()
		if err != nil {
			t.Fatalf("optional generated harness failed: %v\n%s", err, output)
		}
		if got := string(output); got != test.want {
			t.Fatalf("optional harness output = %q, want %q", got, test.want)
		}
	}
}

func TestFunctionGeneratorEmitsSameAsPostcondition(t *testing.T) {
	source, err := NewFunctionGenerator(nil).Generate(harness.Request{
		SourceCode: `class Solution { public: ListNode* reuse(ListNode* head) { return head; } };`,
		Function: harness.FunctionSpec{
			Name:       "reuse",
			ReturnType: "ListNode*",
			Params:     []harness.Parameter{{Name: "head", Type: "ListNode*"}},
			Postconditions: []harness.Postcondition{{
				Kind:          "same_as",
				Subject:       "return",
				FromParameter: 0,
			}},
		},
		TestCases: []harness.TestCase{{
			ID:       1,
			Args:     []json.RawMessage{json.RawMessage(`[1]`)},
			Expected: json.RawMessage(`[1]`),
		}},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !strings.Contains(source, `(__result == head)`) {
		t.Fatal("generated source does not contain same_as identity check")
	}
}
