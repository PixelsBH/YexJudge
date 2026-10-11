package python

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"yexjudge/internal/judge/harness"
	functiontypes "yexjudge/internal/judge/harness/cpp/types"
)

type FunctionGenerator struct{}

var _ harness.ModeGenerator = (*FunctionGenerator)(nil)

func NewFunctionGenerator() *FunctionGenerator           { return &FunctionGenerator{} }
func (g *FunctionGenerator) Language() string            { return "python" }
func (g *FunctionGenerator) Mode() harness.ExecutionMode { return harness.ModeFunction }

func (g *FunctionGenerator) GenerateMode(request harness.ModeRequest) (harness.GeneratedProgram, error) {
	if err := harness.ValidateContract(request.Contract); err != nil {
		return harness.GeneratedProgram{}, err
	}
	if request.Contract.Mode != harness.ModeFunction {
		return harness.GeneratedProgram{}, fmt.Errorf("Python function generator cannot handle mode %q", request.Contract.Mode)
	}
	source, err := generateFunction(request.SourceCode, *request.Contract.Function, request.TestCases)
	if err != nil {
		return harness.GeneratedProgram{}, err
	}
	return harness.GeneratedProgram{SourceCode: source, SourceFile: "main.py"}, nil
}

func generateFunction(sourceCode string, function harness.FunctionSpec, testCases []harness.TestCase) (string, error) {
	if err := validateFunction(sourceCode, function, testCases); err != nil {
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

		args := make([]string, 0, len(testCase.Args))
		for i, raw := range testCase.Args {
			typeName, _ := pythonTypeName(function.Params[i].Type)
			args = append(args, fmt.Sprintf("__yexjudge_build_value(%s, __yexjudge_json.loads(%s))", strconv.Quote(typeName), strconv.Quote(string(raw))))
		}
		fmt.Fprintf(&source, "        __args = [%s]\n", strings.Join(args, ", "))
		source.WriteString("        __captured = __yexjudge_io.StringIO()\n")
		if isVoid(function.ReturnType) {
			fmt.Fprintf(&source, "        with __yexjudge_contextlib.redirect_stdout(__captured):\n            __solution = Solution()\n            __solution.%s(*__args)\n", function.Name)
			source.WriteString("        __output = ")
			output, err := generateObservedFunctionOutput(function)
			if err != nil {
				return "", err
			}
			source.WriteString(output)
			source.WriteString("\n")
		} else {
			fmt.Fprintf(&source, "        with __yexjudge_contextlib.redirect_stdout(__captured):\n            __solution = Solution()\n            __result = __solution.%s(*__args)\n", function.Name)
			returnType, _ := pythonTypeName(function.ReturnType)
			fmt.Fprintf(&source, "        __serialized_result = __yexjudge_serialize_value(%s, __result)\n", strconv.Quote(returnType))
			output, err := generateObservedFunctionOutput(function)
			if err != nil {
				return "", err
			}
			source.WriteString("        __output = ")
			source.WriteString(output)
			source.WriteString("\n")
		}
	}
	source.WriteString("    else:\n        return 1\n")
	source.WriteString("    __yexjudge_sys.stdout.write(__yexjudge_json.dumps(__output, separators=(\",\", \":\"), allow_nan=False) + \"\\n\")\n")
	source.WriteString("    return 0\n\nif __name__ == \"__main__\":\n    raise SystemExit(__yexjudge_main())\n")
	return source.String(), nil
}

func generateObservedFunctionOutput(function harness.FunctionSpec) (string, error) {
	if len(function.Observations) == 0 && len(function.Postconditions) == 0 {
		if isVoid(function.ReturnType) {
			return "None", nil
		}
		return "__serialized_result", nil
	}
	fields := make([]string, 0, len(function.Observations)+1)
	returnObserved := len(function.Observations) == 0
	for _, observation := range function.Observations {
		if observation.Kind == "return" {
			returnObserved = true
		}
	}
	if returnObserved {
		fields = append(fields, "\"return\": __serialized_result")
	}
	parameterFields := make([]string, 0, len(function.Observations))
	for _, observation := range function.Observations {
		if observation.Kind != "parameter" {
			continue
		}
		typeName, err := pythonTypeName(function.Params[observation.Parameter].Type)
		if err != nil {
			return "", err
		}
		valueName := fmt.Sprintf("__args[%d]", observation.Parameter)
		if observation.View == "prefix" {
			valueName = fmt.Sprintf("__args[%d][:max(0, int(__result))]", observation.Parameter)
		}
		serialized := fmt.Sprintf("__yexjudge_serialize_value(%s, %s)", strconv.Quote(typeName), valueName)
		parameterFields = append(parameterFields, fmt.Sprintf("\"%d\": %s", observation.Parameter, serialized))
	}
	if len(parameterFields) > 0 {
		fields = append(fields, "\"parameter\": {"+strings.Join(parameterFields, ", ")+"}")
	}
	if len(function.Postconditions) > 0 {
		conditions := make([]string, 0, len(function.Postconditions))
		returnType, _ := pythonTypeName(function.ReturnType)
		for i, condition := range function.Postconditions {
			conditions = append(conditions, fmt.Sprintf("\"%d\": __yexjudge_postcondition(%s, __result, __args[%d], %s)", i, strconv.Quote(condition.Kind), condition.FromParameter, strconv.Quote(returnType)))
		}
		fields = append(fields, "\"postconditions\": {"+strings.Join(conditions, ", ")+"}")
	}
	return "{" + strings.Join(fields, ", ") + "}", nil
}

func validateFunction(sourceCode string, function harness.FunctionSpec, testCases []harness.TestCase) error {
	if sourceCode == "" {
		return fmt.Errorf("sourceCode is required")
	}
	if len(testCases) == 0 {
		return fmt.Errorf("at least one test case is required")
	}
	if !pythonIdentifier(function.Name) {
		return fmt.Errorf("function name must be a valid Python identifier")
	}
	if function.ReturnType == "" {
		return fmt.Errorf("function returnType is required")
	}
	registry := functiontypes.DefaultRegistry()
	returnAdapter, err := registry.Resolve(function.ReturnType)
	if err != nil {
		return fmt.Errorf("return type: %w", err)
	}
	if _, _, err := normalizeType(function.ReturnType); err != nil {
		return fmt.Errorf("unsupported Python returnType %q: %w", function.ReturnType, err)
	}
	if len(function.Params) > 10 {
		return fmt.Errorf("too many function params")
	}
	parameterAdapters := make([]functiontypes.Adapter, len(function.Params))
	for i, parameter := range function.Params {
		if !pythonIdentifier(parameter.Name) {
			return fmt.Errorf("function param %d name must be a valid Python identifier", i)
		}
		adapter, err := registry.Resolve(parameter.Type)
		if err != nil {
			return fmt.Errorf("function param %d type: %w", i, err)
		}
		if _, _, err := normalizeType(parameter.Type); err != nil || adapter.CppType() == "void" {
			return fmt.Errorf("unsupported Python function param %d type %q", i, parameter.Type)
		}
		parameterAdapters[i] = adapter
	}
	returnObserved, parameterObservations, err := validatePythonObservations(function, returnAdapter, parameterAdapters)
	if err != nil {
		return err
	}
	if comparison := function.Comparison; comparison != nil && comparison.ReturnArrayOrder != "" {
		if comparison.ReturnArrayOrder != "unordered" {
			return fmt.Errorf("unsupported returnArrayOrder %q", comparison.ReturnArrayOrder)
		}
		if !returnObserved || !isContainerType(function.ReturnType) {
			return fmt.Errorf("unordered returnArrayOrder requires an observed vector return type")
		}
	}
	if err := validatePythonPostconditions(function, parameterAdapters); err != nil {
		return err
	}
	seenIDs := make(map[int]struct{}, len(testCases))
	for _, testCase := range testCases {
		if _, exists := seenIDs[testCase.ID]; exists {
			return fmt.Errorf("duplicate test case id %d", testCase.ID)
		}
		seenIDs[testCase.ID] = struct{}{}
		if len(testCase.Args) != len(function.Params) {
			return fmt.Errorf("test case %d args count must match function params count", testCase.ID)
		}
		for j, raw := range testCase.Args {
			if !json.Valid(raw) {
				return fmt.Errorf("test case %d argument %d must be valid JSON", testCase.ID, j)
			}
			if err := parameterAdapters[j].ValidateJSON(raw); err != nil {
				return fmt.Errorf("test case %d argument %d has wrong type: %w", testCase.ID, j, err)
			}
		}
		if len(testCase.Expected) == 0 || !json.Valid(testCase.Expected) {
			return fmt.Errorf("test case %d expected must be valid JSON", testCase.ID)
		}
		if err := validatePythonFunctionExpected(testCase.Expected, function, returnAdapter, parameterAdapters, returnObserved, parameterObservations); err != nil {
			return fmt.Errorf("test case %d expected: %w", testCase.ID, err)
		}
	}
	return nil
}

func validatePythonObservations(function harness.FunctionSpec, returnAdapter functiontypes.Adapter, parameterAdapters []functiontypes.Adapter) (bool, map[int]harness.Observation, error) {
	if len(function.Observations) == 0 {
		if isVoid(function.ReturnType) {
			return false, nil, fmt.Errorf("void functions require at least one observation")
		}
		return true, nil, nil
	}
	returnObserved := false
	parameters := make(map[int]harness.Observation, len(function.Observations))
	for index, observation := range function.Observations {
		switch observation.Kind {
		case "return":
			if returnObserved || isVoid(function.ReturnType) {
				return false, nil, fmt.Errorf("observation %d cannot observe the function return", index)
			}
			returnObserved = true
		case "parameter":
			if observation.Parameter < 0 || observation.Parameter >= len(parameterAdapters) {
				return false, nil, fmt.Errorf("observation %d references invalid parameter %d", index, observation.Parameter)
			}
			if _, exists := parameters[observation.Parameter]; exists {
				return false, nil, fmt.Errorf("observation %d duplicates parameter %d", index, observation.Parameter)
			}
			view := observation.View
			if view == "" {
				view = "full"
			}
			if view != "full" && view != "prefix" {
				return false, nil, fmt.Errorf("observation %d has unsupported view %q", index, observation.View)
			}
			if view == "prefix" {
				if !observation.LengthFromReturn || !isContainerType(function.Params[observation.Parameter].Type) {
					return false, nil, fmt.Errorf("observation %d prefix view requires a vector and lengthFromReturn", index)
				}
				if returnAdapter.CppType() != "int" && returnAdapter.CppType() != "long long" {
					return false, nil, fmt.Errorf("observation %d prefix length requires int or long long return type", index)
				}
			} else if observation.LengthFromReturn {
				return false, nil, fmt.Errorf("observation %d lengthFromReturn requires prefix view", index)
			}
			parameters[observation.Parameter] = observation
		default:
			return false, nil, fmt.Errorf("observation %d has unsupported kind %q", index, observation.Kind)
		}
	}
	return returnObserved, parameters, nil
}

func validatePythonPostconditions(function harness.FunctionSpec, parameterAdapters []functiontypes.Adapter) error {
	if len(function.Postconditions) == 0 {
		return nil
	}
	returnType, _, err := normalizeType(function.ReturnType)
	if err != nil || !returnType.Pointer {
		return fmt.Errorf("Python identity postconditions require a custom runtime return type")
	}
	seen := make(map[string]struct{}, len(function.Postconditions))
	for i, condition := range function.Postconditions {
		if condition.Kind != "disjoint" && condition.Kind != "same_as" {
			return fmt.Errorf("postcondition %d has unsupported kind %q", i, condition.Kind)
		}
		if condition.Subject != "return" {
			return fmt.Errorf("postcondition %d has unsupported subject %q", i, condition.Subject)
		}
		if condition.FromParameter < 0 || condition.FromParameter >= len(parameterAdapters) {
			return fmt.Errorf("postcondition %d references invalid parameter %d", i, condition.FromParameter)
		}
		parameterType, _, err := normalizeType(function.Params[condition.FromParameter].Type)
		if err != nil || parameterType.CanonicalName() != returnType.CanonicalName() {
			return fmt.Errorf("postcondition %d requires matching custom runtime pointer types", i)
		}
		key := condition.Kind + ":" + strconv.Itoa(condition.FromParameter)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("postcondition %d duplicates %s", i, key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validatePythonFunctionExpected(raw json.RawMessage, function harness.FunctionSpec, returnAdapter functiontypes.Adapter, parameterAdapters []functiontypes.Adapter, returnObserved bool, observations map[int]harness.Observation) error {
	if len(function.Observations) == 0 {
		if err := returnAdapter.ValidateJSON(raw); err != nil {
			return fmt.Errorf("wrong return type: %w", err)
		}
		return nil
	}
	var expected map[string]json.RawMessage
	if err := json.Unmarshal(raw, &expected); err != nil || expected == nil {
		return fmt.Errorf("expected an observation object")
	}
	if returnValue, exists := expected["return"]; returnObserved {
		if !exists {
			return fmt.Errorf("return observation is missing")
		}
		if err := returnAdapter.ValidateJSON(returnValue); err != nil {
			return fmt.Errorf("wrong return type: %w", err)
		}
	} else if exists {
		return fmt.Errorf("unexpected return observation")
	}
	parameterValue, hasParameters := expected["parameter"]
	if len(observations) == 0 && hasParameters {
		return fmt.Errorf("unexpected parameter observations")
	}
	if len(observations) > 0 {
		var values map[string]json.RawMessage
		if !hasParameters || json.Unmarshal(parameterValue, &values) != nil || values == nil {
			return fmt.Errorf("parameter observations are missing or invalid")
		}
		for parameter := range observations {
			value, exists := values[strconv.Itoa(parameter)]
			if !exists {
				return fmt.Errorf("parameter observation %d is missing", parameter)
			}
			if err := parameterAdapters[parameter].ValidateJSON(value); err != nil {
				return fmt.Errorf("parameter observation %d has wrong type: %w", parameter, err)
			}
		}
		if len(values) != len(observations) {
			return fmt.Errorf("unexpected parameter observations")
		}
	}
	if conditions, exists := expected["postconditions"]; exists {
		var values map[string]json.RawMessage
		if json.Unmarshal(conditions, &values) != nil || values == nil || len(values) != len(function.Postconditions) {
			return fmt.Errorf("postconditions do not match function metadata")
		}
		for i := range function.Postconditions {
			if string(values[strconv.Itoa(i)]) != "true" {
				return fmt.Errorf("postcondition %d must be true", i)
			}
		}
	}
	for key := range expected {
		if key != "return" && key != "parameter" && key != "postconditions" {
			return fmt.Errorf("unexpected observation %q", key)
		}
	}
	return nil
}
