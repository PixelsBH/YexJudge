package cpp

import (
	"encoding/json"
	"fmt"
	"strings"

	"yexjudge/internal/judge/harness"
	"yexjudge/internal/judge/harness/cpp/types"
)

type FunctionGenerator struct {
	registry *types.Registry
}

var _ harness.ModeGenerator = (*FunctionGenerator)(nil)

func NewFunctionGenerator(registry *types.Registry) *FunctionGenerator {
	if registry == nil {
		registry = types.DefaultRegistry()
	}
	return &FunctionGenerator{registry: registry}
}

func (g *FunctionGenerator) Mode() harness.ExecutionMode {
	return harness.ModeFunction
}

func (g *FunctionGenerator) GenerateMode(request harness.ModeRequest) (harness.GeneratedProgram, error) {
	if err := harness.ValidateContract(request.Contract); err != nil {
		return harness.GeneratedProgram{}, err
	}
	if request.Contract.Mode != harness.ModeFunction {
		return harness.GeneratedProgram{}, fmt.Errorf("C++ function generator cannot handle mode %q", request.Contract.Mode)
	}

	generated, err := g.Generate(harness.Request{
		SourceCode: request.SourceCode,
		Function:   *request.Contract.Function,
		TestCases:  request.TestCases,
	})
	if err != nil {
		return harness.GeneratedProgram{}, err
	}
	return harness.GeneratedProgram{SourceCode: generated, SourceFile: "main.cpp"}, nil
}

func (g *FunctionGenerator) Generate(request harness.Request) (string, error) {
	if err := validateRequest(request, g.registry); err != nil {
		return "", err
	}

	supportAdapters, err := resolveFunctionAdapters(request.Function, g.registry)
	if err != nil {
		return "", err
	}

	var source strings.Builder
	source.WriteString(generateHeaders())
	supportSource, err := generateSupport(g.registry, supportAdapters)
	if err != nil {
		return "", err
	}
	source.WriteString(supportSource)
	source.WriteString(request.SourceCode)
	source.WriteString("\n\n")

	mainSource, err := generateMain(request, g.registry)
	if err != nil {
		return "", err
	}
	source.WriteString(mainSource)
	return source.String(), nil
}

func generateHeaders() string {
	return "#include <bits/stdc++.h>\nusing namespace std;\n\n"
}

func resolveFunctionAdapters(function harness.FunctionSpec, registry *types.Registry) ([]types.Adapter, error) {
	adapters := make([]types.Adapter, 0, len(function.Params)+1)
	returnAdapter, err := registry.Resolve(function.ReturnType)
	if err != nil {
		return nil, err
	}
	adapters = append(adapters, returnAdapter)
	for _, parameter := range function.Params {
		adapter, err := registry.Resolve(parameter.Type)
		if err != nil {
			return nil, err
		}
		adapters = append(adapters, adapter)
	}
	return adapters, nil
}

func generateSupport(registry *types.Registry, adapters []types.Adapter) (string, error) {
	return serializerSource + "\n" + registry.SupportSource(adapters...) + "\n", nil
}

func validateRequest(request harness.Request, registry *types.Registry) error {
	if request.SourceCode == "" {
		return fmt.Errorf("sourceCode is required")
	}
	if request.Function.Name == "" {
		return fmt.Errorf("function name is required")
	}
	if request.Function.ReturnType == "" {
		return fmt.Errorf("function returnType is required")
	}
	returnAdapter, err := registry.Resolve(request.Function.ReturnType)
	if err != nil {
		return fmt.Errorf("return type: %w", err)
	}
	if err := validateObservations(request.Function, returnAdapter); err != nil {
		return err
	}

	parameterAdapters := make([]types.Adapter, len(request.Function.Params))
	for i, parameter := range request.Function.Params {
		if parameter.Name == "" {
			return fmt.Errorf("parameter %d name is required", i)
		}
		adapter, err := registry.Resolve(parameter.Type)
		if err != nil {
			return fmt.Errorf("parameter %d type: %w", i, err)
		}
		if adapter.CppType() == "void" {
			return fmt.Errorf("parameter %d cannot have void type", i)
		}
		parameterAdapters[i] = adapter
	}
	if err := validatePostconditions(request.Function, returnAdapter, parameterAdapters); err != nil {
		return err
	}
	for i, testCase := range request.TestCases {
		if len(testCase.Args) != len(request.Function.Params) {
			return fmt.Errorf("test case %d args count must match function params count", i)
		}
		for j, argument := range testCase.Args {
			if !json.Valid(argument) {
				return fmt.Errorf("test case %d argument %d is invalid JSON", testCase.ID, j)
			}
		}
		if len(testCase.Expected) == 0 || !json.Valid(testCase.Expected) {
			return fmt.Errorf("test case %d expected must be valid JSON", testCase.ID)
		}
	}
	return nil
}

func validatePostconditions(function harness.FunctionSpec, returnAdapter types.Adapter, parameterAdapters []types.Adapter) error {
	if len(function.Postconditions) == 0 {
		return nil
	}
	if returnAdapter.CppType() == "void" {
		return fmt.Errorf("postconditions require a non-void return type")
	}
	if _, ok := returnAdapter.(types.PostconditionAdapter); !ok {
		return fmt.Errorf("return type %q does not support postconditions", returnAdapter.CanonicalName())
	}
	seen := make(map[string]struct{}, len(function.Postconditions))
	for i, postcondition := range function.Postconditions {
		if postcondition.Kind == "" {
			return fmt.Errorf("postcondition %d kind is required", i)
		}
		if postcondition.Kind != "disjoint" && postcondition.Kind != "same_as" {
			return fmt.Errorf("postcondition %d has unsupported kind %q", i, postcondition.Kind)
		}
		if postcondition.Subject != "return" {
			return fmt.Errorf("postcondition %d has unsupported subject %q", i, postcondition.Subject)
		}
		if postcondition.FromParameter < 0 || postcondition.FromParameter >= len(parameterAdapters) {
			return fmt.Errorf("postcondition %d references invalid parameter %d", i, postcondition.FromParameter)
		}
		parameterAdapter := parameterAdapters[postcondition.FromParameter]
		if parameterAdapter.CppType() != returnAdapter.CppType() {
			return fmt.Errorf("postcondition %d requires matching return and parameter pointer types", i)
		}
		key := fmt.Sprintf("%s:%d", postcondition.Kind, postcondition.FromParameter)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("postcondition %d duplicates %s", i, key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateObservations(function harness.FunctionSpec, returnAdapter types.Adapter) error {
	if len(function.Observations) == 0 {
		if returnAdapter.CppType() == "void" {
			return fmt.Errorf("void functions require at least one observation")
		}
		return nil
	}

	returnSeen := false
	parameterSeen := make(map[int]struct{}, len(function.Observations))
	for i, observation := range function.Observations {
		switch observation.Kind {
		case "return":
			if returnSeen {
				return fmt.Errorf("observation %d duplicates the return observation", i)
			}
			if returnAdapter.CppType() == "void" {
				return fmt.Errorf("void functions cannot observe a return value")
			}
			returnSeen = true
		case "parameter":
			if observation.Parameter < 0 || observation.Parameter >= len(function.Params) {
				return fmt.Errorf("observation %d references invalid parameter %d", i, observation.Parameter)
			}
			if _, seen := parameterSeen[observation.Parameter]; seen {
				return fmt.Errorf("observation %d duplicates parameter %d", i, observation.Parameter)
			}
			parameterSeen[observation.Parameter] = struct{}{}

			view := observation.View
			if view == "" {
				view = "full"
			}
			if view != "full" && view != "prefix" {
				return fmt.Errorf("observation %d has unsupported view %q", i, observation.View)
			}
			if view == "prefix" {
				if !observation.LengthFromReturn {
					return fmt.Errorf("observation %d prefix view requires lengthFromReturn", i)
				}
				if !strings.HasPrefix(types.Normalize(function.Params[observation.Parameter].Type), "vector<") {
					return fmt.Errorf("observation %d prefix view requires a vector parameter", i)
				}
				if returnAdapter.CppType() != "int" && returnAdapter.CppType() != "long long" {
					return fmt.Errorf("observation %d prefix length requires int or long long return type", i)
				}
			} else if observation.LengthFromReturn {
				return fmt.Errorf("observation %d lengthFromReturn requires prefix view", i)
			}
		default:
			return fmt.Errorf("observation %d has unsupported kind %q", i, observation.Kind)
		}
	}
	return nil
}

func generateMain(request harness.Request, registry *types.Registry) (string, error) {
	var source strings.Builder
	source.WriteString("int main() {\n")
	source.WriteString("    int __case_id;\n")
	source.WriteString("    if (!(cin >> __case_id)) return 1;\n")
	source.WriteString("    Solution __solution;\n")
	source.WriteString("    switch (__case_id) {\n")

	for _, testCase := range request.TestCases {
		caseSource, err := generateCase(request.Function, testCase, registry)
		if err != nil {
			return "", err
		}
		source.WriteString(caseSource)
	}

	source.WriteString("    default:\n")
	source.WriteString("        return 1;\n")
	source.WriteString("    }\n")
	source.WriteString("    return 0;\n")
	source.WriteString("}\n")
	return source.String(), nil
}

func generateCase(function harness.FunctionSpec, testCase harness.TestCase, registry *types.Registry) (string, error) {
	var source strings.Builder
	fmt.Fprintf(&source, "    case %d: {\n", testCase.ID)

	arguments := make([]string, 0, len(function.Params))
	adapters := make([]types.Adapter, 0, len(function.Params))
	for i, parameter := range function.Params {
		adapter, err := registry.Resolve(parameter.Type)
		if err != nil {
			return "", err
		}
		literal, err := adapter.GenerateLiteral(testCase.Args[i])
		if err != nil {
			return "", fmt.Errorf("test case %d parameter %s: %w", testCase.ID, parameter.Name, err)
		}
		fmt.Fprintf(&source, "        %s %s = %s;\n", adapter.CppType(), parameter.Name, literal)
		arguments = append(arguments, parameter.Name)
		adapters = append(adapters, adapter)
	}

	returnAdapter, err := registry.Resolve(function.ReturnType)
	if err != nil {
		return "", err
	}
	invocation := fmt.Sprintf("__solution.%s(%s)", function.Name, strings.Join(arguments, ", "))
	if returnAdapter.CppType() == "void" {
		fmt.Fprintf(&source, "        %s;\n", invocation)
	} else {
		fmt.Fprintf(&source, "        auto __result = %s;\n", invocation)
	}

	observationSource, err := generateObservations(function, adapters, returnAdapter)
	if err != nil {
		return "", err
	}
	source.WriteString(observationSource)
	source.WriteString("        break;\n")
	source.WriteString("    }\n")
	return source.String(), nil
}

func generateObservations(function harness.FunctionSpec, adapters []types.Adapter, returnAdapter types.Adapter) (string, error) {
	if len(function.Observations) == 0 && len(function.Postconditions) == 0 {
		return fmt.Sprintf("        cout << %s;\n", returnAdapter.SerializeExpression("__result")), nil
	}

	returnObservation := len(function.Observations) == 0
	parameterObservations := make([]harness.Observation, 0, len(function.Observations))
	for _, observation := range function.Observations {
		if observation.Kind == "return" {
			returnObservation = true
		} else {
			parameterObservations = append(parameterObservations, observation)
		}
	}

	var source strings.Builder
	source.WriteString("        cout << \"{\"")
	if returnObservation {
		source.WriteString(" << \"\\\"return\\\":\" << ")
		source.WriteString(returnAdapter.SerializeExpression("__result"))
	}
	if len(parameterObservations) > 0 {
		if returnObservation {
			source.WriteString(" << \",\"")
		}
		source.WriteString(" << \"\\\"parameter\\\":{\"")
		for i, observation := range parameterObservations {
			if i > 0 {
				source.WriteString(" << \",\"")
			}
			adapter := adapters[observation.Parameter]
			valueExpression := ""
			if observation.View == "prefix" {
				valueExpression = fmt.Sprintf("__serialize_prefix(%s, __result)", function.Params[observation.Parameter].Name)
			} else {
				valueExpression = adapter.SerializeExpression(function.Params[observation.Parameter].Name)
			}
			fmt.Fprintf(&source, " << \"\\\"%d\\\":\" << %s", observation.Parameter, valueExpression)
		}
		source.WriteString(" << \"}\"")
	}
	if len(function.Postconditions) > 0 {
		if returnObservation || len(parameterObservations) > 0 {
			source.WriteString(" << \",\"")
		}
		source.WriteString(" << \"\\\"postconditions\\\":{\"")
		for i, postcondition := range function.Postconditions {
			if i > 0 {
				source.WriteString(" << \",\"")
			}
			adapter, ok := returnAdapter.(types.PostconditionAdapter)
			if !ok {
				return "", fmt.Errorf("return type %q does not support postconditions", returnAdapter.CanonicalName())
			}
			valueExpression, err := adapter.PostconditionExpression(
				postcondition.Kind,
				"__result",
				function.Params[postcondition.FromParameter].Name,
			)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&source, " << \"\\\"%d\\\":\" << __serialize(%s)", i, valueExpression)
		}
		source.WriteString(" << \"}\"")
	}
	source.WriteString(" << \"}\";\n")
	return source.String(), nil
}
