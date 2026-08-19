package cpp

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"yexjudge/internal/judge/harness"
	"yexjudge/internal/judge/harness/cpp/types"
)

// ClassGenerator emits one constructor call followed by a metadata-described
// sequence of operations. It deliberately does not know any class/problem
// semantics; operation names and signatures come entirely from the contract.
type ClassGenerator struct {
	registry *types.Registry
}

var _ harness.ModeGenerator = (*ClassGenerator)(nil)

func NewClassGenerator(registry *types.Registry) *ClassGenerator {
	if registry == nil {
		registry = types.DefaultRegistry()
	}
	return &ClassGenerator{registry: registry}
}

func (g *ClassGenerator) Mode() harness.ExecutionMode { return harness.ModeClass }

func (g *ClassGenerator) GenerateMode(request harness.ModeRequest) (harness.GeneratedProgram, error) {
	if err := harness.ValidateContract(request.Contract); err != nil {
		return harness.GeneratedProgram{}, err
	}
	if request.Contract.Mode != harness.ModeClass {
		return harness.GeneratedProgram{}, fmt.Errorf("C++ class generator cannot handle mode %q", request.Contract.Mode)
	}
	generated, err := g.Generate(ClassRequest{
		SourceCode: request.SourceCode,
		Class:      *request.Contract.Class,
		TestCases:  request.TestCases,
	})
	if err != nil {
		return harness.GeneratedProgram{}, err
	}
	return harness.GeneratedProgram{SourceCode: generated, SourceFile: "main.cpp"}, nil
}

type ClassRequest struct {
	SourceCode string
	Class      harness.ClassSpec
	TestCases  []harness.TestCase
}

func (g *ClassGenerator) Generate(request ClassRequest) (string, error) {
	if err := validateClassRequest(request, g.registry); err != nil {
		return "", err
	}

	adapters, err := resolveClassAdapters(request.Class, g.registry)
	if err != nil {
		return "", err
	}
	support := serializerSource + "\n" + g.registry.SupportSource(adapters...) + "\n"
	mainSource, err := generateClassMain(request, g.registry)
	if err != nil {
		return "", err
	}
	return generateHeaders() + support + request.SourceCode + "\n\n" + mainSource, nil
}

func resolveClassAdapters(class harness.ClassSpec, registry *types.Registry) ([]types.Adapter, error) {
	adapters := make([]types.Adapter, 0, len(class.Constructor.Params)+len(class.Operations)*2)
	for _, parameter := range class.Constructor.Params {
		adapter, err := registry.Resolve(parameter.Type)
		if err != nil {
			return nil, err
		}
		adapters = append(adapters, adapter)
	}
	for _, operation := range class.Operations {
		returnAdapter, err := registry.Resolve(operation.ReturnType)
		if err != nil {
			return nil, err
		}
		adapters = append(adapters, returnAdapter)
		for _, parameter := range operation.Params {
			adapter, err := registry.Resolve(parameter.Type)
			if err != nil {
				return nil, err
			}
			adapters = append(adapters, adapter)
		}
	}
	return adapters, nil
}

func validateClassRequest(request ClassRequest, registry *types.Registry) error {
	if request.SourceCode == "" {
		return fmt.Errorf("sourceCode is required")
	}
	if !isCxxIdentifier(request.Class.Name) {
		return fmt.Errorf("class name must be a valid C++ identifier")
	}
	if len(request.Class.Operations) == 0 {
		return fmt.Errorf("at least one class operation is required")
	}
	seenOperations := make(map[string]struct{}, len(request.Class.Operations))
	for i, operation := range request.Class.Operations {
		if !isCxxIdentifier(operation.Name) {
			return fmt.Errorf("operation %d name must be a valid C++ identifier", i)
		}
		if _, exists := seenOperations[operation.Name]; exists {
			return fmt.Errorf("duplicate class operation %q", operation.Name)
		}
		seenOperations[operation.Name] = struct{}{}
		if operation.ReturnType == "" {
			return fmt.Errorf("operation %q returnType is required", operation.Name)
		}
		returnAdapter, err := registry.Resolve(operation.ReturnType)
		if err != nil {
			return fmt.Errorf("operation %q return type: %w", operation.Name, err)
		}
		if err := validateClassParameters(operation.Name, operation.Params, registry); err != nil {
			return err
		}
		if returnAdapter.CppType() == "void" {
			continue
		}
	}
	if err := validateClassParameters("constructor", request.Class.Constructor.Params, registry); err != nil {
		return err
	}

	operations := make(map[string]harness.OperationSpec, len(request.Class.Operations))
	for _, operation := range request.Class.Operations {
		operations[operation.Name] = operation
	}
	seenCaseIDs := make(map[int]struct{}, len(request.TestCases))
	for i, testCase := range request.TestCases {
		if _, exists := seenCaseIDs[testCase.ID]; exists {
			return fmt.Errorf("duplicate test case id %d", testCase.ID)
		}
		seenCaseIDs[testCase.ID] = struct{}{}
		if len(testCase.ConstructorArgs) != len(request.Class.Constructor.Params) {
			return fmt.Errorf("test case %d constructor args count must match constructor params count", i)
		}
		for j, argument := range testCase.ConstructorArgs {
			if !json.Valid(argument) {
				return fmt.Errorf("test case %d constructor argument %d must be valid JSON", testCase.ID, j)
			}
			adapter, err := registry.Resolve(request.Class.Constructor.Params[j].Type)
			if err != nil {
				return err
			}
			if err := adapter.ValidateJSON(argument); err != nil {
				return fmt.Errorf("test case %d constructor argument %d has wrong type: %w", testCase.ID, j, err)
			}
		}
		if len(testCase.Operations) == 0 {
			return fmt.Errorf("test case %d must contain at least one operation", testCase.ID)
		}
		if len(testCase.Expected) == 0 || !json.Valid(testCase.Expected) {
			return fmt.Errorf("test case %d expected must be a valid JSON array", testCase.ID)
		}
		var expected []json.RawMessage
		if err := json.Unmarshal(testCase.Expected, &expected); err != nil {
			return fmt.Errorf("test case %d expected must be a JSON array", testCase.ID)
		}
		if len(expected) != len(testCase.Operations) {
			return fmt.Errorf("test case %d expected count must match operation count", testCase.ID)
		}
		for operationIndex, call := range testCase.Operations {
			operation, ok := operations[call.Name]
			if !ok {
				return fmt.Errorf("test case %d operation %q is not declared", testCase.ID, call.Name)
			}
			if len(call.Args) != len(operation.Params) {
				return fmt.Errorf("test case %d operation %q args count must match metadata", testCase.ID, call.Name)
			}
			for argIndex, argument := range call.Args {
				if !json.Valid(argument) {
					return fmt.Errorf("test case %d operation %q argument %d must be valid JSON", testCase.ID, call.Name, argIndex)
				}
				adapter, err := registry.Resolve(operation.Params[argIndex].Type)
				if err != nil {
					return err
				}
				if err := adapter.ValidateJSON(argument); err != nil {
					return fmt.Errorf("test case %d operation %q argument %d has wrong type: %w", testCase.ID, call.Name, argIndex, err)
				}
			}
			returnAdapter, err := registry.Resolve(operation.ReturnType)
			if err != nil {
				return err
			}
			if returnAdapter.CppType() == "void" {
				if strings.TrimSpace(string(expected[operationIndex])) != "null" {
					return fmt.Errorf("test case %d operation %q expected result must be null", testCase.ID, call.Name)
				}
			} else if err := returnAdapter.ValidateJSON(expected[operationIndex]); err != nil {
				return fmt.Errorf("test case %d operation %q expected result has wrong type: %w", testCase.ID, call.Name, err)
			}
		}
	}
	return nil
}

func validateClassParameters(owner string, parameters []harness.Parameter, registry *types.Registry) error {
	for i, parameter := range parameters {
		if parameter.Name != "" && !isCxxIdentifier(parameter.Name) {
			return fmt.Errorf("%s parameter %d name must be a valid C++ identifier", owner, i)
		}
		if parameter.Type == "" {
			return fmt.Errorf("%s parameter %d type is required", owner, i)
		}
		adapter, err := registry.Resolve(parameter.Type)
		if err != nil {
			return fmt.Errorf("%s parameter %d type: %w", owner, i, err)
		}
		if adapter.CppType() == "void" {
			return fmt.Errorf("%s parameter %d cannot have void type", owner, i)
		}
	}
	return nil
}

func generateClassMain(request ClassRequest, registry *types.Registry) (string, error) {
	var source strings.Builder
	source.WriteString("int main() {\n")
	source.WriteString("    int __case_id;\n")
	source.WriteString("    if (!(cin >> __case_id)) return 1;\n")
	source.WriteString("    switch (__case_id) {\n")
	for _, testCase := range request.TestCases {
		caseSource, err := generateClassCase(request.Class, testCase, registry)
		if err != nil {
			return "", err
		}
		source.WriteString(caseSource)
	}
	source.WriteString("    default: return 1;\n")
	source.WriteString("    }\n")
	source.WriteString("    return 0;\n")
	source.WriteString("}\n")
	return source.String(), nil
}

func generateClassCase(class harness.ClassSpec, testCase harness.TestCase, registry *types.Registry) (string, error) {
	var source strings.Builder
	fmt.Fprintf(&source, "    case %d: {\n", testCase.ID)

	constructorArguments := make([]string, 0, len(class.Constructor.Params))
	for i, parameter := range class.Constructor.Params {
		adapter, err := registry.Resolve(parameter.Type)
		if err != nil {
			return "", err
		}
		name := parameter.Name
		if name == "" {
			name = fmt.Sprintf("__constructor_arg_%d", i)
		}
		literal, err := adapter.GenerateLiteral(testCase.ConstructorArgs[i])
		if err != nil {
			return "", fmt.Errorf("test case %d constructor argument %d: %w", testCase.ID, i, err)
		}
		fmt.Fprintf(&source, "        %s %s = %s;\n", adapter.CppType(), name, literal)
		constructorArguments = append(constructorArguments, name)
	}
	fmt.Fprintf(&source, "        %s __object(%s);\n", class.Name, strings.Join(constructorArguments, ", "))
	source.WriteString("        cout << \"[\";\n")
	for operationIndex, call := range testCase.Operations {
		if operationIndex > 0 {
			source.WriteString("        cout << \",\";\n")
		}
		operation := findClassOperation(class, call.Name)
		arguments := make([]string, 0, len(operation.Params))
		for i, parameter := range operation.Params {
			adapter, err := registry.Resolve(parameter.Type)
			if err != nil {
				return "", err
			}
			name := fmt.Sprintf("__operation_%d_arg_%d", operationIndex, i)
			literal, err := adapter.GenerateLiteral(call.Args[i])
			if err != nil {
				return "", fmt.Errorf("test case %d operation %q argument %d: %w", testCase.ID, call.Name, i, err)
			}
			fmt.Fprintf(&source, "        %s %s = %s;\n", adapter.CppType(), name, literal)
			arguments = append(arguments, name)
		}
		returnAdapter, err := registry.Resolve(operation.ReturnType)
		if err != nil {
			return "", err
		}
		invocation := fmt.Sprintf("__object.%s(%s)", operation.Name, strings.Join(arguments, ", "))
		if returnAdapter.CppType() == "void" {
			fmt.Fprintf(&source, "        %s;\n", invocation)
			source.WriteString("        cout << \"null\";\n")
		} else {
			fmt.Fprintf(&source, "        auto __operation_result_%d = %s;\n", operationIndex, invocation)
			fmt.Fprintf(&source, "        cout << %s;\n", returnAdapter.SerializeExpression(fmt.Sprintf("__operation_result_%d", operationIndex)))
		}
	}
	source.WriteString("        cout << \"]\";\n")
	source.WriteString("        break;\n    }\n")
	return source.String(), nil
}

func findClassOperation(class harness.ClassSpec, name string) harness.OperationSpec {
	for _, operation := range class.Operations {
		if operation.Name == name {
			return operation
		}
	}
	return harness.OperationSpec{}
}

func isCxxIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for index, character := range value {
		if unicode.IsLetter(character) || character == '_' || (index > 0 && unicode.IsDigit(character)) {
			continue
		}
		return false
	}
	return true
}
