package types

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRegistryNormalizesReferenceTypes(t *testing.T) {
	registry := DefaultRegistry()

	adapter, err := registry.Resolve("const vector<int> &")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got, want := adapter.CanonicalName(), "vector<int>"; got != want {
		t.Fatalf("CanonicalName() = %q, want %q", got, want)
	}
}

func TestParseNormalizesRecursiveTypes(t *testing.T) {
	ref, err := Parse("const std::vector<vector<int>> &")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got, want := ref.CanonicalName(), "vector<vector<int>>"; got != want {
		t.Fatalf("CanonicalName() = %q, want %q", got, want)
	}
	if !ref.Const || ref.Reference != LValueReference {
		t.Fatalf("Parse() qualifiers = const:%v reference:%v", ref.Const, ref.Reference)
	}
}

func TestOptionalAdapterIsRecursiveAndNullable(t *testing.T) {
	registry := DefaultRegistry()
	adapter, err := registry.Resolve("optional<vector<int>>")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := adapter.CanonicalName(), "optional<vector<int>>"; got != want {
		t.Fatalf("CanonicalName() = %q, want %q", got, want)
	}
	if literal, err := adapter.GenerateLiteral(json.RawMessage(`null`)); err != nil || literal != "optional<vector<int>>{}" {
		t.Fatalf("null optional literal = %q, error = %v", literal, err)
	}
	if literal, err := adapter.GenerateLiteral(json.RawMessage(`[1,2]`)); err != nil || literal != "optional<vector<int>>{vector<int>{1, 2}}" {
		t.Fatalf("value optional literal = %q, error = %v", literal, err)
	}
	if canonical, err := adapter.CanonicalJSON(json.RawMessage(`null`)); err != nil || canonical != "null" {
		t.Fatalf("null optional canonical = %q, error = %v", canonical, err)
	}
}

func TestRecursiveVectorAdapter(t *testing.T) {
	adapter, err := DefaultRegistry().Resolve("vector<vector<int>>")
	if err != nil {
		t.Fatal(err)
	}

	literal, err := adapter.GenerateLiteral(json.RawMessage(`[[1,2],[3]]`))
	if err != nil {
		t.Fatalf("GenerateLiteral() error = %v", err)
	}
	if want := "vector<vector<int>>{vector<int>{1, 2}, vector<int>{3}}"; literal != want {
		t.Errorf("GenerateLiteral() = %q, want %q", literal, want)
	}
}

func TestVectorLiteralAndCanonicalJSON(t *testing.T) {
	registry := DefaultRegistry()
	adapter, err := registry.Resolve("vector<long long>&")
	if err != nil {
		t.Fatal(err)
	}

	raw := json.RawMessage(`[1, 2, -3]`)
	literal, err := adapter.GenerateLiteral(raw)
	if err != nil {
		t.Fatalf("GenerateLiteral() error = %v", err)
	}
	if want := "vector<long long>{1, 2, -3}"; literal != want {
		t.Errorf("GenerateLiteral() = %q, want %q", literal, want)
	}

	canonical, err := adapter.CanonicalJSON(json.RawMessage(`[1,2,-3]`))
	if err != nil {
		t.Fatalf("CanonicalJSON() error = %v", err)
	}
	if want := `[1,2,-3]`; canonical != want {
		t.Errorf("CanonicalJSON() = %q, want %q", canonical, want)
	}
}

func TestStringCanonicalJSONMatchesCxxJSONEscaping(t *testing.T) {
	adapter, err := DefaultRegistry().Resolve("string")
	if err != nil {
		t.Fatal(err)
	}

	canonical, err := adapter.CanonicalJSON(json.RawMessage(`"<&\\u0001"`))
	if err != nil {
		t.Fatalf("CanonicalJSON() error = %v", err)
	}
	if want := `"<&\\u0001"`; canonical != want {
		t.Errorf("CanonicalJSON() = %q, want %q", canonical, want)
	}
}

func TestAdaptersRejectWrongJSONShapes(t *testing.T) {
	registry := DefaultRegistry()
	adapter, err := registry.Resolve("int")
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.ValidateJSON(json.RawMessage(`"not an integer"`)); err == nil {
		t.Fatal("ValidateJSON() accepted a string for int")
	}

	adapter, err = registry.Resolve("vector<bool>")
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.ValidateJSON(json.RawMessage(`true`)); err == nil {
		t.Fatal("ValidateJSON() accepted a scalar for vector<bool>")
	}
}

func TestRuntimeAdaptersCanBeRegisteredWithoutGeneratorChanges(t *testing.T) {
	adapter, err := NewRuntimeAdapter(RuntimeTypeSpec{
		CanonicalName:     "ExternalNode*",
		CppType:           "ExternalNode*",
		GenerateLiteral:   func(json.RawMessage) (string, error) { return "nullptr", nil },
		SerializeFunction: "__serialize",
		ValidateJSON:      func(json.RawMessage) error { return nil },
		CanonicalJSON:     func(json.RawMessage) (string, error) { return "null", nil },
		SupportSource:     "struct ExternalNode {};",
	})
	if err != nil {
		t.Fatalf("NewRuntimeAdapter() error = %v", err)
	}
	registry := NewRegistry()
	if err := registry.RegisterAdapter(adapter); err != nil {
		t.Fatalf("RegisterAdapter() error = %v", err)
	}
	resolved, err := registry.Resolve("ExternalNode*")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.CppType() != "ExternalNode*" || !strings.Contains(registry.SupportSource(resolved), "ExternalNode") {
		t.Fatalf("registered runtime adapter was not resolved/emitted: %#v", resolved)
	}
}

func TestCustomRuntimeAdaptersCanonicalizeValues(t *testing.T) {
	registry := DefaultRegistry()

	list, err := registry.Resolve("const ListNode*")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := list.CanonicalName(), "ListNode*"; got != want {
		t.Fatalf("list canonical name = %q, want %q", got, want)
	}
	if got, err := list.CanonicalJSON(json.RawMessage(`[1,2,null]`)); err == nil {
		t.Fatalf("list accepted malformed value and returned %q", got)
	}
	if got, err := list.CanonicalJSON(json.RawMessage(`[1,2]`)); err != nil || got != `[1,2]` {
		t.Fatalf("list canonical JSON = %q, error = %v", got, err)
	}
	if got, err := list.CanonicalJSON(json.RawMessage(`[]`)); err != nil || got != `null` {
		t.Fatalf("empty list canonical JSON = %q, error = %v", got, err)
	}

	random, err := registry.Resolve("Node*")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := random.CanonicalJSON(json.RawMessage(`{"values":[7,13],"random":[null,0]}`)); err != nil || got != `{"values":[7,13],"random":[null,0]}` {
		t.Fatalf("random-list canonical JSON = %q, error = %v", got, err)
	}
	if got, err := random.CanonicalJSON(json.RawMessage(`{"values":[],"random":[]}`)); err != nil || got != `null` {
		t.Fatalf("empty random-list canonical JSON = %q, error = %v", got, err)
	}

	tree, err := registry.Resolve("TreeNode*")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := tree.CanonicalJSON(json.RawMessage(`[1,null,2,null]`)); err != nil || got != `[1,null,2]` {
		t.Fatalf("tree canonical JSON = %q, error = %v", got, err)
	}

	graph, err := registry.Resolve("GraphNode*")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := graph.CanonicalJSON(json.RawMessage(`{"values":[1,2],"neighbors":[[1],[0]]}`)); err != nil || got != `{"values":[1,2],"neighbors":[[1],[0]]}` {
		t.Fatalf("graph canonical JSON = %q, error = %v", got, err)
	}
	if _, err := graph.CanonicalJSON(json.RawMessage(`{"values":[1,2,3],"neighbors":[[1],[0],[]]}`)); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("graph accepted unreachable node: %v", err)
	}
}
