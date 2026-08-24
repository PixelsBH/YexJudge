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

func TestGeneratedLRUCacheHarnessExecutesMultipleOperationSequences(t *testing.T) {
	gxx, err := exec.LookPath("g++")
	if err != nil {
		t.Skip("g++ is not installed")
	}

	source, err := NewClassGenerator(nil).Generate(ClassRequest{
		SourceCode: `class LRUCache {
	int capacity;
	list<pair<int, int>> items;
	unordered_map<int, list<pair<int, int>>::iterator> index;

public:
	LRUCache(int capacity) : capacity(capacity) {}

	int get(int key) {
		auto found = index.find(key);
		if (found == index.end()) return -1;
		items.splice(items.begin(), items, found->second);
		return found->second->second;
	}

	void put(int key, int value) {
		if (capacity <= 0) return;
		auto found = index.find(key);
		if (found != index.end()) {
			found->second->second = value;
			items.splice(items.begin(), items, found->second);
			return;
		}
		items.emplace_front(key, value);
		index[key] = items.begin();
		if (static_cast<int>(index.size()) > capacity) {
			auto leastRecent = prev(items.end());
			index.erase(leastRecent->first);
			items.pop_back();
		}
	}
};`,
		Class: harness.ClassSpec{
			Name: "LRUCache",
			Constructor: harness.ConstructorSpec{
				Params: []harness.Parameter{{Name: "capacity", Type: "int"}},
			},
			Operations: []harness.OperationSpec{
				{Name: "get", ReturnType: "int", Params: []harness.Parameter{{Name: "key", Type: "int"}}},
				{Name: "put", ReturnType: "void", Params: []harness.Parameter{{Name: "key", Type: "int"}, {Name: "value", Type: "int"}}},
			},
		},
		TestCases: []harness.TestCase{
			{
				ID:              1,
				ConstructorArgs: []json.RawMessage{json.RawMessage(`2`)},
				Operations: []harness.OperationCall{
					{Name: "put", Args: []json.RawMessage{json.RawMessage(`1`), json.RawMessage(`1`)}},
					{Name: "put", Args: []json.RawMessage{json.RawMessage(`2`), json.RawMessage(`2`)}},
					{Name: "get", Args: []json.RawMessage{json.RawMessage(`1`)}},
					{Name: "put", Args: []json.RawMessage{json.RawMessage(`3`), json.RawMessage(`3`)}},
					{Name: "get", Args: []json.RawMessage{json.RawMessage(`2`)}},
					{Name: "put", Args: []json.RawMessage{json.RawMessage(`4`), json.RawMessage(`4`)}},
					{Name: "get", Args: []json.RawMessage{json.RawMessage(`1`)}},
					{Name: "get", Args: []json.RawMessage{json.RawMessage(`3`)}},
					{Name: "get", Args: []json.RawMessage{json.RawMessage(`4`)}},
				},
				Expected: json.RawMessage(`[null,null,1,null,-1,null,-1,3,4]`),
			},
			{
				ID:              2,
				ConstructorArgs: []json.RawMessage{json.RawMessage(`1`)},
				Operations: []harness.OperationCall{
					{Name: "put", Args: []json.RawMessage{json.RawMessage(`10`), json.RawMessage(`10`)}},
					{Name: "get", Args: []json.RawMessage{json.RawMessage(`10`)}},
					{Name: "put", Args: []json.RawMessage{json.RawMessage(`20`), json.RawMessage(`20`)}},
					{Name: "get", Args: []json.RawMessage{json.RawMessage(`10`)}},
					{Name: "get", Args: []json.RawMessage{json.RawMessage(`20`)}},
				},
				Expected: json.RawMessage(`[null,10,null,-1,20]`),
			},
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
		t.Fatalf("generated LRU source did not compile: %v\n%s", err, output)
	}

	for _, testCase := range []struct {
		id   string
		want string
	}{
		{id: "1", want: `[null,null,1,null,-1,null,-1,3,4]`},
		{id: "2", want: `[null,10,null,-1,20]`},
	} {
		run := exec.Command(binaryPath)
		run.Stdin = strings.NewReader(testCase.id + "\n")
		output, err := run.CombinedOutput()
		if err != nil {
			t.Fatalf("generated LRU harness case %s failed: %v\n%s", testCase.id, err, output)
		}
		if got := string(output); got != testCase.want {
			t.Fatalf("LRU harness case %s output = %q, want %q", testCase.id, got, testCase.want)
		}
	}
}
