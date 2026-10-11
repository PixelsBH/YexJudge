package python

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"yexjudge/internal/judge/harness"
	functiontypes "yexjudge/internal/judge/harness/cpp/types"
)

type ClassGenerator struct{}

var _ harness.ModeGenerator = (*ClassGenerator)(nil)

func NewClassGenerator() *ClassGenerator              { return &ClassGenerator{} }
func (g *ClassGenerator) Language() string            { return "python" }
func (g *ClassGenerator) Mode() harness.ExecutionMode { return harness.ModeClass }

func (g *ClassGenerator) GenerateMode(request harness.ModeRequest) (harness.GeneratedProgram, error) {
	if err := harness.ValidateContract(request.Contract); err != nil {
		return harness.GeneratedProgram{}, err
	}
	if request.Contract.Mode != harness.ModeClass {
		return harness.GeneratedProgram{}, fmt.Errorf("Python class generator cannot handle mode %q", request.Contract.Mode)
	}
	source, err := generateClass(request.SourceCode, *request.Contract.Class, request.TestCases)
	if err != nil {
		return harness.GeneratedProgram{}, err
	}
	return harness.GeneratedProgram{SourceCode: source, SourceFile: "main.py"}, nil
}

func generateClass(sourceCode string, class harness.ClassSpec, testCases []harness.TestCase) (string, error) {
	operations, err := validateClass(sourceCode, class, testCases)
	if err != nil {
		return "", err
	}
	var source strings.Builder
	source.WriteString(runtimeSource)
	source.WriteString("\n\n")
	source.WriteString(sourceCode)
	source.WriteString("\n\n")
	source.WriteString("def __yexjudge_main():\n")
	source.WriteString("    __case_id = int(__yexjudge_sys.stdin.readline().strip())\n")
	for index, testCase := range testCases {
		keyword := "elif"
		if index == 0 {
			keyword = "if"
		}
		fmt.Fprintf(&source, "    %s __case_id == %d:\n", keyword, testCase.ID)
		constructorArgs := make([]string, 0, len(testCase.ConstructorArgs))
		for i, raw := range testCase.ConstructorArgs {
			typeName, _ := pythonTypeName(class.Constructor.Params[i].Type)
			constructorArgs = append(constructorArgs, fmt.Sprintf("__yexjudge_build_value(%s, __yexjudge_json.loads(%s))", strconv.Quote(typeName), strconv.Quote(string(raw))))
		}
		source.WriteString("        __captured = __yexjudge_io.StringIO()\n")
		fmt.Fprintf(&source, "        with __yexjudge_contextlib.redirect_stdout(__captured):\n            __object = %s(%s)\n", class.Name, strings.Join(constructorArgs, ", "))
		source.WriteString("            __output = []\n")
		for _, call := range testCase.Operations {
			operation := operations[call.Name]
			arguments := make([]string, 0, len(call.Args))
			for i, raw := range call.Args {
				typeName, _ := pythonTypeName(operation.Params[i].Type)
				arguments = append(arguments, fmt.Sprintf("__yexjudge_build_value(%s, __yexjudge_json.loads(%s))", strconv.Quote(typeName), strconv.Quote(string(raw))))
			}
			if isVoid(operation.ReturnType) {
				fmt.Fprintf(&source, "            __object.%s(%s)\n            __output.append(None)\n", call.Name, strings.Join(arguments, ", "))
			} else {
				returnType, _ := pythonTypeName(operation.ReturnType)
				fmt.Fprintf(&source, "            __operation_result = __object.%s(%s)\n", call.Name, strings.Join(arguments, ", "))
				fmt.Fprintf(&source, "            __output.append(__yexjudge_serialize_value(%s, __operation_result))\n", strconv.Quote(returnType))
			}
		}

	}
	source.WriteString("    else:\n        return 1\n")
	source.WriteString("    __yexjudge_sys.stdout.write(__yexjudge_json.dumps(__output, separators=(\",\", \":\"), allow_nan=False) + \"\\n\")\n")
	source.WriteString("    return 0\n\nif __name__ == \"__main__\":\n    raise SystemExit(__yexjudge_main())\n")
	return source.String(), nil
}

func validateClass(sourceCode string, class harness.ClassSpec, testCases []harness.TestCase) (map[string]harness.OperationSpec, error) {
	if sourceCode == "" {
		return nil, fmt.Errorf("sourceCode is required")
	}
	if len(testCases) == 0 {
		return nil, fmt.Errorf("at least one test case is required")
	}
	if !pythonIdentifier(class.Name) {
		return nil, fmt.Errorf("class name must be a valid Python identifier")
	}
	if len(class.Operations) == 0 {
		return nil, fmt.Errorf("at least one class operation is required")
	}
	registry := functiontypes.DefaultRegistry()
	if err := validateClassParams("constructor", class.Constructor.Params, registry, true); err != nil {
		return nil, err
	}
	operations := make(map[string]harness.OperationSpec, len(class.Operations))
	for index, operation := range class.Operations {
		if !pythonIdentifier(operation.Name) {
			return nil, fmt.Errorf("operation %d name must be a valid Python identifier", index)
		}
		if _, exists := operations[operation.Name]; exists {
			return nil, fmt.Errorf("duplicate class operation %q", operation.Name)
		}
		if operation.ReturnType == "" {
			return nil, fmt.Errorf("operation %q returnType is required", operation.Name)
		}
		if _, err := registry.Resolve(operation.ReturnType); err != nil {
			return nil, fmt.Errorf("operation %q return type: %w", operation.Name, err)
		}
		if _, _, err := normalizeType(operation.ReturnType); err != nil {
			return nil, fmt.Errorf("operation %q has unsupported Python return type %q: %w", operation.Name, operation.ReturnType, err)
		}
		if err := validateClassParams("operation "+operation.Name, operation.Params, registry, false); err != nil {
			return nil, err
		}
		operations[operation.Name] = operation
	}
	seenIDs := make(map[int]struct{}, len(testCases))
	for _, testCase := range testCases {
		if _, exists := seenIDs[testCase.ID]; exists {
			return nil, fmt.Errorf("duplicate test case id %d", testCase.ID)
		}
		seenIDs[testCase.ID] = struct{}{}
		if len(testCase.ConstructorArgs) != len(class.Constructor.Params) {
			return nil, fmt.Errorf("test case %d constructor args count must match constructor params count", testCase.ID)
		}
		for i, argument := range testCase.ConstructorArgs {
			if !json.Valid(argument) {
				return nil, fmt.Errorf("test case %d constructor argument %d must be valid JSON", testCase.ID, i)
			}
			adapter, _ := registry.Resolve(class.Constructor.Params[i].Type)
			if err := adapter.ValidateJSON(argument); err != nil {
				return nil, fmt.Errorf("test case %d constructor argument %d has wrong type: %w", testCase.ID, i, err)
			}
		}
		if len(testCase.Operations) == 0 {
			return nil, fmt.Errorf("test case %d must contain at least one operation", testCase.ID)
		}
		var expected []json.RawMessage
		if len(testCase.Expected) == 0 || json.Unmarshal(testCase.Expected, &expected) != nil {
			return nil, fmt.Errorf("test case %d expected must be a JSON array", testCase.ID)
		}
		if len(expected) != len(testCase.Operations) {
			return nil, fmt.Errorf("test case %d expected count must match operation count", testCase.ID)
		}
		for index, call := range testCase.Operations {
			operation, exists := operations[call.Name]
			if !exists {
				return nil, fmt.Errorf("test case %d operation %q is not declared", testCase.ID, call.Name)
			}
			if len(call.Args) != len(operation.Params) {
				return nil, fmt.Errorf("test case %d operation %q args count must match metadata", testCase.ID, call.Name)
			}
			for argumentIndex, argument := range call.Args {
				if !json.Valid(argument) {
					return nil, fmt.Errorf("test case %d operation %q argument %d must be valid JSON", testCase.ID, call.Name, argumentIndex)
				}
				adapter, _ := registry.Resolve(operation.Params[argumentIndex].Type)
				if err := adapter.ValidateJSON(argument); err != nil {
					return nil, fmt.Errorf("test case %d operation %q argument %d has wrong type: %w", testCase.ID, call.Name, argumentIndex, err)
				}
			}
			returnAdapter, _ := registry.Resolve(operation.ReturnType)
			if isVoid(operation.ReturnType) {
				if strings.TrimSpace(string(expected[index])) != "null" {
					return nil, fmt.Errorf("test case %d operation %q expected result must be null", testCase.ID, call.Name)
				}
			} else if err := returnAdapter.ValidateJSON(expected[index]); err != nil {
				return nil, fmt.Errorf("test case %d operation %q expected result has wrong type: %w", testCase.ID, call.Name, err)
			}
		}
	}
	return operations, nil
}

func validateClassParams(owner string, parameters []harness.Parameter, registry *functiontypes.Registry, allowEmptyName bool) error {
	for index, parameter := range parameters {
		if parameter.Name == "" && allowEmptyName {
			// Class calls are positional; constructor names are descriptive only.
		} else if !pythonIdentifier(parameter.Name) {
			return fmt.Errorf("%s parameter %d name must be a valid Python identifier", owner, index)
		}
		if parameter.Type == "" {
			return fmt.Errorf("%s parameter %d type is required", owner, index)
		}
		adapter, err := registry.Resolve(parameter.Type)
		if err != nil {
			return fmt.Errorf("%s parameter %d type: %w", owner, index, err)
		}
		if _, _, err := normalizeType(parameter.Type); err != nil || adapter.CppType() == "void" {
			return fmt.Errorf("unsupported %s parameter %d type %q", owner, index, parameter.Type)
		}
	}
	return nil
}
