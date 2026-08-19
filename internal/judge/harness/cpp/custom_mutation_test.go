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

func TestGeneratedCustomRuntimeMutationObservations(t *testing.T) {
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
			t.Fatalf("%s generated source did not compile: %v\n%s", name, err, output)
		}
		run := exec.Command(binaryPath)
		run.Stdin = strings.NewReader(input)
		output, err := run.CombinedOutput()
		if err != nil {
			t.Fatalf("%s generated harness failed: %v\n%s", name, err, output)
		}
		if got := string(output); got != want {
			t.Fatalf("%s mutation output = %q, want %q", name, got, want)
		}
	}

	treeSource, err := NewFunctionGenerator(nil).Generate(harness.Request{
		SourceCode: `class Solution {
public:
    void mutate(TreeNode* root) {
        if (root == nullptr) return;
        root->val += 10;
        swap(root->left, root->right);
    }
};`,
		Function: harness.FunctionSpec{
			Name:       "mutate",
			ReturnType: "void",
			Params:     []harness.Parameter{{Name: "root", Type: "TreeNode*"}},
			Observations: []harness.Observation{{
				Kind:      "parameter",
				Parameter: 0,
			}},
		},
		TestCases: []harness.TestCase{{
			ID:       1,
			Args:     []json.RawMessage{json.RawMessage(`[1,2,3]`)},
			Expected: json.RawMessage(`{"parameter":{"0":[11,3,2]}}`),
		}},
	})
	if err != nil {
		t.Fatalf("tree mutation Generate() error = %v", err)
	}
	runGenerated("tree", treeSource, "1\n", `{"parameter":{"0":[11,3,2]}}`)

	graphSource, err := NewFunctionGenerator(nil).Generate(harness.Request{
		SourceCode: `class Solution {
public:
    void mutate(GraphNode* root) {
        if (root == nullptr) return;
        root->val = 9;
        reverse(root->neighbors.begin(), root->neighbors.end());
    }
};`,
		Function: harness.FunctionSpec{
			Name:       "mutate",
			ReturnType: "void",
			Params:     []harness.Parameter{{Name: "root", Type: "GraphNode*"}},
			Observations: []harness.Observation{{
				Kind:      "parameter",
				Parameter: 0,
			}},
		},
		TestCases: []harness.TestCase{{
			ID:       2,
			Args:     []json.RawMessage{json.RawMessage(`{"values":[1,2,3],"neighbors":[[1,2],[2],[0]]}`)},
			Expected: json.RawMessage(`{"parameter":{"0":{"values":[9,3,2],"neighbors":[[1,2],[0],[1]]}}}`),
		}},
	})
	if err != nil {
		t.Fatalf("graph mutation Generate() error = %v", err)
	}
	runGenerated("graph", graphSource, "2\n", `{"parameter":{"0":{"values":[9,3,2],"neighbors":[[1,2],[0],[1]]}}}`)

	randomSource, err := NewFunctionGenerator(nil).Generate(harness.Request{
		SourceCode: `class Solution {
public:
    void mutate(Node* head) {
        if (head != nullptr) head->random = head;
    }
};`,
		Function: harness.FunctionSpec{
			Name:       "mutate",
			ReturnType: "void",
			Params:     []harness.Parameter{{Name: "head", Type: "Node*"}},
			Observations: []harness.Observation{{
				Kind:      "parameter",
				Parameter: 0,
			}},
		},
		TestCases: []harness.TestCase{{
			ID:       3,
			Args:     []json.RawMessage{json.RawMessage(`{"values":[7,13],"random":[null,0]}`)},
			Expected: json.RawMessage(`{"parameter":{"0":{"values":[7,13],"random":[0,0]}}}`),
		}},
	})
	if err != nil {
		t.Fatalf("random-list mutation Generate() error = %v", err)
	}
	runGenerated("random-list", randomSource, "3\n", `{"parameter":{"0":{"values":[7,13],"random":[0,0]}}}`)
}
