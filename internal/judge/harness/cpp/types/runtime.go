package types

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RuntimeAdapter describes a registered C++ runtime object. Its support source
// is emitted before user code so Solution signatures can use the hidden type.
// The adapter remains independent of invocation policy; the function generator
// only asks it for construction, serialization, and optional postconditions.
type RuntimeAdapter struct {
	name          string
	cppType       string
	literal       func(json.RawMessage) (string, error)
	serialize     string
	deserialize   func(string) string
	validate      func(json.RawMessage) error
	canonical     func(json.RawMessage) (string, error)
	support       string
	postcondition func(kind, valueExpression, otherExpression string) (string, error)
}

// RuntimeTypeSpec is the registration contract for a hidden C++ runtime type.
// A new type can be added without changing FunctionGenerator or ClassGenerator.
type RuntimeTypeSpec struct {
	CanonicalName           string
	CppType                 string
	GenerateLiteral         func(json.RawMessage) (string, error)
	SerializeFunction       string
	DeserializeExpression   func(string) string
	ValidateJSON            func(json.RawMessage) error
	CanonicalJSON           func(json.RawMessage) (string, error)
	SupportSource           string
	PostconditionExpression func(kind, valueExpression, otherExpression string) (string, error)
}

func NewRuntimeAdapter(spec RuntimeTypeSpec) (RuntimeAdapter, error) {
	if spec.CanonicalName == "" || spec.CppType == "" {
		return RuntimeAdapter{}, fmt.Errorf("runtime type canonical name and C++ type are required")
	}
	if spec.GenerateLiteral == nil || spec.ValidateJSON == nil || spec.CanonicalJSON == nil {
		return RuntimeAdapter{}, fmt.Errorf("runtime type %q requires literal and JSON handlers", spec.CanonicalName)
	}
	if spec.SerializeFunction == "" {
		return RuntimeAdapter{}, fmt.Errorf("runtime type %q requires a serializer", spec.CanonicalName)
	}
	return RuntimeAdapter{
		name:          spec.CanonicalName,
		cppType:       spec.CppType,
		literal:       spec.GenerateLiteral,
		serialize:     spec.SerializeFunction,
		deserialize:   spec.DeserializeExpression,
		validate:      spec.ValidateJSON,
		canonical:     spec.CanonicalJSON,
		support:       spec.SupportSource,
		postcondition: spec.PostconditionExpression,
	}, nil
}

func (a RuntimeAdapter) CanonicalName() string { return a.name }
func (a RuntimeAdapter) CppType() string       { return a.cppType }
func (a RuntimeAdapter) GenerateLiteral(raw json.RawMessage) (string, error) {
	return a.literal(raw)
}
func (a RuntimeAdapter) SerializeExpression(valueExpression string) string {
	return a.serialize + "(" + valueExpression + ")"
}
func (a RuntimeAdapter) DeserializeExpression(inputExpression string) string {
	if a.deserialize == nil {
		return inputExpression
	}
	return a.deserialize(inputExpression)
}
func (a RuntimeAdapter) ValidateJSON(raw json.RawMessage) error {
	return a.validate(raw)
}
func (a RuntimeAdapter) CanonicalJSON(raw json.RawMessage) (string, error) {
	return a.canonical(raw)
}
func (a RuntimeAdapter) SupportSource() string { return a.support }
func (a RuntimeAdapter) PostconditionExpression(kind, valueExpression, otherExpression string) (string, error) {
	if a.postcondition == nil {
		return "", fmt.Errorf("type %q does not support postcondition %q", a.name, kind)
	}
	return a.postcondition(kind, valueExpression, otherExpression)
}

type PostconditionAdapter interface {
	PostconditionExpression(kind, valueExpression, otherExpression string) (string, error)
}

type SupportSourceAdapter interface {
	SupportSource() string
}

func listNodeAdapter() RuntimeAdapter {
	return RuntimeAdapter{
		name:          "ListNode*",
		cppType:       "ListNode*",
		literal:       listLiteral,
		serialize:     "__serialize",
		validate:      validateListJSON,
		canonical:     canonicalListJSON,
		support:       listNodeSupportSource,
		postcondition: identityPostcondition("__disjoint"),
	}
}

func randomListNodeAdapter(name string) RuntimeAdapter {
	return RuntimeAdapter{
		name:    name,
		cppType: name,
		literal: func(raw json.RawMessage) (string, error) {
			return randomListLiteral(raw, name)
		},
		serialize:     "__serialize",
		validate:      validateRandomListJSON,
		canonical:     canonicalRandomListJSON,
		support:       randomListSupportSource,
		postcondition: identityPostcondition("__disjoint"),
	}
}

func treeNodeAdapter() RuntimeAdapter {
	return RuntimeAdapter{
		name:          "TreeNode*",
		cppType:       "TreeNode*",
		literal:       treeLiteral,
		serialize:     "__serialize",
		validate:      validateTreeJSON,
		canonical:     canonicalTreeJSON,
		support:       treeNodeSupportSource,
		postcondition: identityPostcondition("__disjoint"),
	}
}

func graphNodeAdapter(name string) RuntimeAdapter {
	return RuntimeAdapter{
		name:    name,
		cppType: name,
		literal: func(raw json.RawMessage) (string, error) {
			return graphLiteral(raw, name)
		},
		serialize:     "__serialize",
		validate:      validateGraphJSON,
		canonical:     canonicalGraphJSON,
		support:       graphNodeSupportSource,
		postcondition: identityPostcondition("__disjoint"),
	}
}

func identityPostcondition(functionName string) func(string, string, string) (string, error) {
	return func(kind, valueExpression, otherExpression string) (string, error) {
		switch kind {
		case "disjoint":
			return fmt.Sprintf("%s(%s, %s)", functionName, valueExpression, otherExpression), nil
		case "same_as":
			return fmt.Sprintf("(%s == %s)", valueExpression, otherExpression), nil
		default:
			return "", fmt.Errorf("unsupported postcondition %q", kind)
		}
	}
}

func listLiteral(raw json.RawMessage) (string, error) {
	if strings.TrimSpace(string(raw)) == "null" {
		return "nullptr", nil
	}
	values, err := arrayValues(raw)
	if err != nil {
		return "", err
	}
	literals := make([]string, 0, len(values))
	for i, value := range values {
		literal, err := integerLiteral(32)(value)
		if err != nil {
			return "", fmt.Errorf("list value %d: %w", i, err)
		}
		literals = append(literals, literal)
	}
	return "__build_list(vector<int>{" + strings.Join(literals, ",") + "})", nil
}

func validateListJSON(raw json.RawMessage) error {
	_, err := canonicalListJSON(raw)
	return err
}

func canonicalListJSON(raw json.RawMessage) (string, error) {
	if strings.TrimSpace(string(raw)) == "null" {
		return "null", nil
	}
	values, err := arrayValues(raw)
	if err != nil {
		return "", err
	}
	if len(values) == 0 {
		return "null", nil
	}
	canonical := make([]string, 0, len(values))
	for i, value := range values {
		item, err := integerCanonical(32)(value)
		if err != nil {
			return "", fmt.Errorf("list value %d: %w", i, err)
		}
		canonical = append(canonical, item)
	}
	return "[" + strings.Join(canonical, ",") + "]", nil
}

type randomListJSON struct {
	Values []json.RawMessage `json:"values"`
	Random []json.RawMessage `json:"random"`
}

func parseRandomListJSON(raw json.RawMessage) (randomListJSON, error) {
	if strings.TrimSpace(string(raw)) == "null" {
		return randomListJSON{}, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return randomListJSON{}, fmt.Errorf("expected random-list object")
	}
	valuesRaw, ok := object["values"]
	if !ok {
		return randomListJSON{}, fmt.Errorf("random-list values are required")
	}
	randomRaw, ok := object["random"]
	if !ok {
		return randomListJSON{}, fmt.Errorf("random-list random indices are required")
	}
	var result randomListJSON
	if err := json.Unmarshal(valuesRaw, &result.Values); err != nil {
		return randomListJSON{}, fmt.Errorf("random-list values must be an array")
	}
	if err := json.Unmarshal(randomRaw, &result.Random); err != nil {
		return randomListJSON{}, fmt.Errorf("random-list random must be an array")
	}
	if len(result.Values) != len(result.Random) {
		return randomListJSON{}, fmt.Errorf("random-list values and random lengths must match")
	}
	if len(result.Values) == 0 {
		return result, nil
	}
	for i, value := range result.Values {
		if _, err := integerCanonical(32)(value); err != nil {
			return randomListJSON{}, fmt.Errorf("random-list value %d: %w", i, err)
		}
	}
	for i, value := range result.Random {
		if strings.TrimSpace(string(value)) == "null" {
			continue
		}
		index, err := parseInteger(value, 32)
		if err != nil || index < 0 || index >= int64(len(result.Values)) {
			return randomListJSON{}, fmt.Errorf("random-list index %d is out of range", i)
		}
	}
	return result, nil
}

func validateRandomListJSON(raw json.RawMessage) error {
	_, err := parseRandomListJSON(raw)
	return err
}

func randomListLiteral(raw json.RawMessage, nodeType string) (string, error) {
	if strings.TrimSpace(string(raw)) == "null" {
		return "nullptr", nil
	}
	value, err := parseRandomListJSON(raw)
	if err != nil {
		return "", err
	}
	values := make([]string, 0, len(value.Values))
	indices := make([]string, 0, len(value.Random))
	for _, item := range value.Values {
		canonical, _ := integerCanonical(32)(item)
		values = append(values, canonical)
	}
	for _, item := range value.Random {
		if strings.TrimSpace(string(item)) == "null" {
			indices = append(indices, "-1")
		} else {
			index, _ := parseInteger(item, 32)
			indices = append(indices, fmt.Sprintf("%d", index))
		}
	}
	builder := "__build_random_list"
	if nodeType == "Node*" {
		builder = "__build_random_list_node"
	}
	return builder + "(vector<int>{" + strings.Join(values, ",") + "}, vector<int>{" + strings.Join(indices, ",") + "})", nil
}

func canonicalRandomListJSON(raw json.RawMessage) (string, error) {
	if strings.TrimSpace(string(raw)) == "null" {
		return "null", nil
	}
	value, err := parseRandomListJSON(raw)
	if err != nil {
		return "", err
	}
	if len(value.Values) == 0 {
		return "null", nil
	}
	values := make([]string, 0, len(value.Values))
	indices := make([]string, 0, len(value.Random))
	for _, item := range value.Values {
		canonical, _ := integerCanonical(32)(item)
		values = append(values, canonical)
	}
	for _, item := range value.Random {
		if strings.TrimSpace(string(item)) == "null" {
			indices = append(indices, "null")
		} else {
			index, _ := parseInteger(item, 32)
			indices = append(indices, fmt.Sprintf("%d", index))
		}
	}
	return `{"values":[` + strings.Join(values, ",") + `],"random":[` + strings.Join(indices, ",") + `]}`, nil
}

type graphJSON struct {
	Values    []json.RawMessage   `json:"values"`
	Neighbors [][]json.RawMessage `json:"neighbors"`
}

func parseGraphJSON(raw json.RawMessage) (graphJSON, error) {
	if strings.TrimSpace(string(raw)) == "null" {
		return graphJSON{}, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return graphJSON{}, fmt.Errorf("expected graph object")
	}
	valuesRaw, ok := object["values"]
	if !ok {
		return graphJSON{}, fmt.Errorf("graph values are required")
	}
	neighborsRaw, ok := object["neighbors"]
	if !ok {
		return graphJSON{}, fmt.Errorf("graph neighbors are required")
	}
	var graph graphJSON
	if err := json.Unmarshal(valuesRaw, &graph.Values); err != nil {
		return graphJSON{}, fmt.Errorf("graph values must be an array")
	}
	if err := json.Unmarshal(neighborsRaw, &graph.Neighbors); err != nil {
		return graphJSON{}, fmt.Errorf("graph neighbors must be an array")
	}
	if len(graph.Values) != len(graph.Neighbors) {
		return graphJSON{}, fmt.Errorf("graph values and neighbors lengths must match")
	}
	for i, value := range graph.Values {
		if _, err := integerCanonical(32)(value); err != nil {
			return graphJSON{}, fmt.Errorf("graph value %d: %w", i, err)
		}
	}
	for i, neighbors := range graph.Neighbors {
		for j, neighbor := range neighbors {
			index, err := parseInteger(neighbor, 32)
			if err != nil || index < 0 || index >= int64(len(graph.Values)) {
				return graphJSON{}, fmt.Errorf("graph neighbor %d.%d is out of range", i, j)
			}
		}
	}
	if len(graph.Values) > 0 {
		reachable := make([]bool, len(graph.Values))
		pending := []int{0}
		for len(pending) > 0 {
			index := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			if reachable[index] {
				continue
			}
			reachable[index] = true
			for _, neighbor := range graph.Neighbors[index] {
				neighborIndex, _ := parseInteger(neighbor, 32)
				pending = append(pending, int(neighborIndex))
			}
		}
		for i, isReachable := range reachable {
			if !isReachable {
				return graphJSON{}, fmt.Errorf("graph node %d is unreachable from root", i)
			}
		}
	}
	return graph, nil
}

func validateGraphJSON(raw json.RawMessage) error {
	_, err := parseGraphJSON(raw)
	return err
}

func graphLiteral(raw json.RawMessage, nodeType string) (string, error) {
	if strings.TrimSpace(string(raw)) == "null" {
		return "nullptr", nil
	}
	graph, err := parseGraphJSON(raw)
	if err != nil {
		return "", err
	}
	values := make([]string, 0, len(graph.Values))
	for _, value := range graph.Values {
		canonical, _ := integerCanonical(32)(value)
		values = append(values, canonical)
	}
	neighborRows := make([]string, 0, len(graph.Neighbors))
	for _, neighbors := range graph.Neighbors {
		indices := make([]string, 0, len(neighbors))
		for _, neighbor := range neighbors {
			index, _ := parseInteger(neighbor, 32)
			indices = append(indices, fmt.Sprintf("%d", index))
		}
		neighborRows = append(neighborRows, "vector<int>{"+strings.Join(indices, ",")+"}")
	}
	builder := "__build_graph"
	if nodeType != "GraphNode*" {
		builder += "_" + strings.TrimSuffix(nodeType, "*")
	}
	return builder + "(vector<int>{" + strings.Join(values, ",") + "}, vector<vector<int>>{" + strings.Join(neighborRows, ",") + "})", nil
}

func canonicalGraphJSON(raw json.RawMessage) (string, error) {
	if strings.TrimSpace(string(raw)) == "null" {
		return "null", nil
	}
	graph, err := parseGraphJSON(raw)
	if err != nil {
		return "", err
	}
	if len(graph.Values) == 0 {
		return "null", nil
	}
	values := make([]string, 0, len(graph.Values))
	for _, value := range graph.Values {
		canonical, _ := integerCanonical(32)(value)
		values = append(values, canonical)
	}
	rows := make([]string, 0, len(graph.Neighbors))
	for _, neighbors := range graph.Neighbors {
		indices := make([]string, 0, len(neighbors))
		for _, neighbor := range neighbors {
			index, _ := parseInteger(neighbor, 32)
			indices = append(indices, fmt.Sprintf("%d", index))
		}
		rows = append(rows, "["+strings.Join(indices, ",")+"]")
	}
	return `{"values":[` + strings.Join(values, ",") + `],"neighbors":[` + strings.Join(rows, ",") + `]}`, nil
}

func treeLiteral(raw json.RawMessage) (string, error) {
	if strings.TrimSpace(string(raw)) == "null" {
		return "nullptr", nil
	}
	values, err := arrayValues(raw)
	if err != nil {
		return "", err
	}
	literals := make([]string, 0, len(values))
	for i, value := range values {
		if strings.TrimSpace(string(value)) == "null" {
			literals = append(literals, "nullopt")
			continue
		}
		literal, err := integerLiteral(32)(value)
		if err != nil {
			return "", fmt.Errorf("tree value %d: %w", i, err)
		}
		literals = append(literals, "optional<int>("+literal+")")
	}
	return "__build_tree(vector<optional<int>>{" + strings.Join(literals, ",") + "})", nil
}

func validateTreeJSON(raw json.RawMessage) error {
	_, err := canonicalTreeJSON(raw)
	return err
}

func canonicalTreeJSON(raw json.RawMessage) (string, error) {
	if strings.TrimSpace(string(raw)) == "null" {
		return "null", nil
	}
	values, err := arrayValues(raw)
	if err != nil {
		return "", err
	}
	last := len(values) - 1
	for last >= 0 && strings.TrimSpace(string(values[last])) == "null" {
		last--
	}
	if last < 0 {
		return "null", nil
	}
	canonical := make([]string, 0, last+1)
	for i := 0; i <= last; i++ {
		if strings.TrimSpace(string(values[i])) == "null" {
			canonical = append(canonical, "null")
			continue
		}
		item, err := integerCanonical(32)(values[i])
		if err != nil {
			return "", fmt.Errorf("tree value %d: %w", i, err)
		}
		canonical = append(canonical, item)
	}
	return "[" + strings.Join(canonical, ",") + "]", nil
}

const listNodeSupportSource = `
struct ListNode {
    int val;
    ListNode* next;
    ListNode() : val(0), next(nullptr) {}
    ListNode(int value) : val(value), next(nullptr) {}
    ListNode(int value, ListNode* nextNode) : val(value), next(nextNode) {}
};

static ListNode* __build_list(const vector<int>& values) {
    ListNode dummy(0);
    ListNode* tail = &dummy;
    for (int value : values) {
        tail->next = new ListNode(value);
        tail = tail->next;
    }
    return dummy.next;
}

static string __serialize(ListNode* head) {
    if (head == nullptr) return "null";
    string out = "[";
    bool first = true;
    unordered_set<ListNode*> seen;
    for (ListNode* node = head; node != nullptr && !seen.count(node); node = node->next) {
        seen.insert(node);
        if (!first) out += ",";
        first = false;
        out += to_string(node->val);
    }
    out += "]";
    return out;
}

static bool __disjoint(ListNode* first, ListNode* second) {
    unordered_set<ListNode*> nodes;
    for (ListNode* node = first; node != nullptr && !nodes.count(node); node = node->next) {
        nodes.insert(node);
    }
    unordered_set<ListNode*> checked;
    for (ListNode* node = second; node != nullptr && !checked.count(node); node = node->next) {
        if (nodes.count(node)) return false;
        checked.insert(node);
    }
    return true;
}
`

const randomListSupportSource = `
struct RandomListNode {
    int val;
    RandomListNode* next;
    RandomListNode* random;
    RandomListNode() : val(0), next(nullptr), random(nullptr) {}
    RandomListNode(int value) : val(value), next(nullptr), random(nullptr) {}
};

struct Node {
    int val;
    Node* next;
    Node* random;
    Node() : val(0), next(nullptr), random(nullptr) {}
    Node(int value) : val(value), next(nullptr), random(nullptr) {}
};

template <typename NodeType>
static NodeType* __build_random_list_typed(const vector<int>& values, const vector<int>& randomIndices) {
    vector<NodeType*> nodes;
    nodes.reserve(values.size());
    for (int value : values) nodes.push_back(new NodeType(value));
    for (size_t i = 1; i < nodes.size(); i++) nodes[i - 1]->next = nodes[i];
    for (size_t i = 0; i < nodes.size(); i++) {
        int index = randomIndices[i];
        nodes[i]->random = (index < 0 ? nullptr : nodes[static_cast<size_t>(index)]);
    }
    return nodes.empty() ? nullptr : nodes[0];
}

static RandomListNode* __build_random_list(const vector<int>& values, const vector<int>& randomIndices) {
    return __build_random_list_typed<RandomListNode>(values, randomIndices);
}

static Node* __build_random_list_node(const vector<int>& values, const vector<int>& randomIndices) {
    return __build_random_list_typed<Node>(values, randomIndices);
}

template <typename NodeType>
static string __serialize_random_list_typed(NodeType* head) {
    if (head == nullptr) return "null";
    vector<NodeType*> nodes;
    unordered_map<NodeType*, int> indexes;
    for (NodeType* node = head; node != nullptr && !indexes.count(node); node = node->next) {
        indexes[node] = static_cast<int>(nodes.size());
        nodes.push_back(node);
    }
    string out = "{\"values\":[";
    for (size_t i = 0; i < nodes.size(); i++) {
        if (i > 0) out += ",";
        out += to_string(nodes[i]->val);
    }
    out += "],\"random\":[";
    for (size_t i = 0; i < nodes.size(); i++) {
        if (i > 0) out += ",";
        if (nodes[i]->random == nullptr) out += "null";
        else {
            auto it = indexes.find(nodes[i]->random);
            out += (it == indexes.end() ? "null" : to_string(it->second));
        }
    }
    out += "]}";
    return out;
}

static string __serialize(RandomListNode* head) {
    return __serialize_random_list_typed(head);
}

static string __serialize(Node* head) {
    return __serialize_random_list_typed(head);
}

template <typename NodeType>
static bool __disjoint_random_list_typed(NodeType* first, NodeType* second) {
    unordered_set<NodeType*> nodes;
    vector<NodeType*> pending;
    if (first != nullptr) pending.push_back(first);
    while (!pending.empty()) {
        NodeType* node = pending.back();
        pending.pop_back();
        if (node == nullptr || nodes.count(node)) continue;
        nodes.insert(node);
        pending.push_back(node->next);
        pending.push_back(node->random);
    }
    unordered_set<NodeType*> visited;
    pending.clear();
    if (second != nullptr) pending.push_back(second);
    while (!pending.empty()) {
        NodeType* node = pending.back();
        pending.pop_back();
        if (node == nullptr || visited.count(node)) continue;
        if (nodes.count(node)) return false;
        visited.insert(node);
        pending.push_back(node->next);
        pending.push_back(node->random);
    }
    return true;
}

static bool __disjoint(RandomListNode* first, RandomListNode* second) {
    return __disjoint_random_list_typed(first, second);
}

static bool __disjoint(Node* first, Node* second) {
    return __disjoint_random_list_typed(first, second);
}
`

const graphNodeSupportSource = `
struct GraphNode {
    int val;
    vector<GraphNode*> neighbors;
    GraphNode() : val(0), neighbors() {}
    GraphNode(int value) : val(value), neighbors() {}
};

static GraphNode* __build_graph(const vector<int>& values, const vector<vector<int>>& neighborIndices) {
    if (values.empty()) return nullptr;
    vector<GraphNode*> nodes;
    nodes.reserve(values.size());
    for (int value : values) nodes.push_back(new GraphNode(value));
    for (size_t i = 0; i < nodes.size(); i++) {
        for (int neighbor : neighborIndices[i]) {
            nodes[i]->neighbors.push_back(nodes[static_cast<size_t>(neighbor)]);
        }
    }
    return nodes[0];
}

static string __serialize(GraphNode* root) {
    if (root == nullptr) return "null";
    vector<GraphNode*> nodes;
    unordered_map<GraphNode*, int> indexes;
    vector<GraphNode*> pending{root};
    size_t pendingIndex = 0;
    while (pendingIndex < pending.size()) {
        GraphNode* node = pending[pendingIndex++];
        if (node == nullptr || indexes.count(node)) continue;
        indexes[node] = static_cast<int>(nodes.size());
        nodes.push_back(node);
        for (GraphNode* neighbor : node->neighbors) pending.push_back(neighbor);
    }
    string out = "{\"values\":[";
    for (size_t i = 0; i < nodes.size(); i++) {
        if (i > 0) out += ",";
        out += to_string(nodes[i]->val);
    }
    out += "],\"neighbors\":[";
    for (size_t i = 0; i < nodes.size(); i++) {
        if (i > 0) out += ",";
        out += "[";
        for (size_t j = 0; j < nodes[i]->neighbors.size(); j++) {
            if (j > 0) out += ",";
            out += to_string(indexes[nodes[i]->neighbors[j]]);
        }
        out += "]";
    }
    out += "]}";
    return out;
}

static bool __disjoint(GraphNode* first, GraphNode* second) {
    unordered_set<GraphNode*> nodes;
    vector<GraphNode*> pending;
    if (first != nullptr) pending.push_back(first);
    while (!pending.empty()) {
        GraphNode* node = pending.back();
        pending.pop_back();
        if (node == nullptr || nodes.count(node)) continue;
        nodes.insert(node);
        for (GraphNode* neighbor : node->neighbors) pending.push_back(neighbor);
    }
    unordered_set<GraphNode*> visited;
    if (second != nullptr) pending.push_back(second);
    while (!pending.empty()) {
        GraphNode* node = pending.back();
        pending.pop_back();
        if (node == nullptr || visited.count(node)) continue;
        if (nodes.count(node)) return false;
        visited.insert(node);
        for (GraphNode* neighbor : node->neighbors) pending.push_back(neighbor);
    }
    return true;
}
`

const treeNodeSupportSource = `
struct TreeNode {
    int val;
    TreeNode* left;
    TreeNode* right;
    TreeNode() : val(0), left(nullptr), right(nullptr) {}
    TreeNode(int value) : val(value), left(nullptr), right(nullptr) {}
    TreeNode(int value, TreeNode* leftNode, TreeNode* rightNode)
        : val(value), left(leftNode), right(rightNode) {}
};

static TreeNode* __build_tree(const vector<optional<int>>& values) {
    if (values.empty() || !values[0].has_value()) return nullptr;
    vector<TreeNode*> nodes(values.size(), nullptr);
    for (size_t i = 0; i < values.size(); i++) {
        if (values[i].has_value()) nodes[i] = new TreeNode(*values[i]);
    }
    size_t child = 1;
    for (size_t i = 0; i < values.size() && child < values.size(); i++) {
        if (nodes[i] == nullptr) continue;
        if (child < values.size()) nodes[i]->left = nodes[child++];
        if (child < values.size()) nodes[i]->right = nodes[child++];
    }
    return nodes[0];
}

static string __serialize(TreeNode* root) {
    if (root == nullptr) return "null";
    vector<TreeNode*> queue{root};
    unordered_set<TreeNode*> seen;
    size_t index = 0;
    while (index < queue.size() && queue.size() <= 200000) {
        TreeNode* node = queue[index++];
        if (node == nullptr || seen.count(node)) continue;
        seen.insert(node);
        queue.push_back(node->left);
        queue.push_back(node->right);
    }
    size_t outputCount = 0;
    for (size_t i = 0; i < queue.size(); i++) {
        if (queue[i] != nullptr) outputCount = i + 1;
    }
    string out = "[";
    for (size_t i = 0; i < outputCount; i++) {
        if (i > 0) out += ",";
        out += (queue[i] == nullptr ? "null" : to_string(queue[i]->val));
    }
    out += "]";
    return out;
}

static bool __disjoint(TreeNode* first, TreeNode* second) {
    unordered_set<TreeNode*> nodes;
    vector<TreeNode*> pending;
    if (first != nullptr) pending.push_back(first);
    while (!pending.empty()) {
        TreeNode* node = pending.back();
        pending.pop_back();
        if (node == nullptr || nodes.count(node)) continue;
        nodes.insert(node);
        pending.push_back(node->left);
        pending.push_back(node->right);
    }
    unordered_set<TreeNode*> visited;
    if (second != nullptr) pending.push_back(second);
    while (!pending.empty()) {
        TreeNode* node = pending.back();
        pending.pop_back();
        if (node == nullptr || visited.count(node)) continue;
        if (nodes.count(node)) return false;
        visited.insert(node);
        pending.push_back(node->left);
        pending.push_back(node->right);
    }
    return true;
}
`
