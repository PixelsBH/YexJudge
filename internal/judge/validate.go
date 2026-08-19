package judge

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"yexjudge/internal/judge/harness"
	functiontypes "yexjudge/internal/judge/harness/cpp/types"
)

const (
	MaxTimeLimitMs   = 10_000
	MaxMemoryLimitMb = 512
)

func ValidateJob(job Job) error {
	if job.Language == "" {
		return fmt.Errorf("language is required")
	}

	requestedMode := job.ExecutionMode()
	if !requestedMode.Valid() {
		return fmt.Errorf("unsupported execution mode %q", requestedMode)
	}
	if requestedMode != harness.ModeStdin && requestedMode != harness.ModeFunction && requestedMode != harness.ModeClass {
		return fmt.Errorf("execution mode %q is not implemented", requestedMode)
	}
	if requestedMode == harness.ModeFunction && job.Function == nil {
		return fmt.Errorf("function metadata is required for function mode")
	}
	if requestedMode != harness.ModeFunction && job.Function != nil {
		return fmt.Errorf("function metadata is only valid in function mode")
	}
	if requestedMode == harness.ModeClass && job.Class == nil {
		return fmt.Errorf("class metadata is required for class mode")
	}
	if requestedMode != harness.ModeClass && job.Class != nil {
		return fmt.Errorf("class metadata is only valid in class mode")
	}

	if job.SourceCode == "" {
		return fmt.Errorf("sourceCode is required")
	}

	if len(job.TestCases) == 0 {
		return fmt.Errorf("at least one test case is required")
	}

	if job.Limits.TimeLimitMs <= 0 {
		return fmt.Errorf("timeLimitMs must be greater than 0")
	}
	if job.Limits.TimeLimitMs > MaxTimeLimitMs {
		return fmt.Errorf("timeLimitMs must not exceed %d", MaxTimeLimitMs)
	}

	if job.Limits.MemoryLimitMb <= 0 {
		return fmt.Errorf("memoryLimitMb must be greater than 0")
	}
	if job.Limits.MemoryLimitMb > MaxMemoryLimitMb {
		return fmt.Errorf("memoryLimitMb must not exceed %d", MaxMemoryLimitMb)
	}

	if len(job.SourceCode) > 100_000 {
		return fmt.Errorf("sourceCode is too large")
	}

	if len(job.TestCases) > 100 {
		return fmt.Errorf("too many test cases")
	}

	if job.Function != nil {
		return validateFunctionJob(job)
	}
	if job.Class != nil {
		if job.Language != "cpp" {
			return fmt.Errorf("class mode currently supports cpp only")
		}
		if _, err := buildCppClassHarness(job); err != nil {
			return fmt.Errorf("class metadata: %w", err)
		}
		return nil
	}

	for i, tc := range job.TestCases {
		if len(tc.Input) > 100_000 {
			return fmt.Errorf("test case %d input is too large", i)
		}

		if len(tc.ExpectedOutput) > 100_000 {
			return fmt.Errorf("test case %d expectedOutput is too large", i)
		}
	}

	return nil
}

func validateFunctionJob(job Job) error {
	if job.Language != "cpp" {
		return fmt.Errorf("function mode currently supports cpp only")
	}

	if !isCppIdentifier(job.Function.Name) {
		return fmt.Errorf("function name must be a valid C++ identifier")
	}
	if job.Function.ReturnType == "" {
		return fmt.Errorf("function returnType is required")
	}

	registry := functiontypes.DefaultRegistry()
	returnType, err := registry.Resolve(job.Function.ReturnType)
	if err != nil {
		return fmt.Errorf("unsupported function returnType %q", job.Function.ReturnType)
	}

	if len(job.Function.Params) > 10 {
		return fmt.Errorf("too many function params")
	}

	paramTypes := make([]functiontypes.Adapter, len(job.Function.Params))
	for i, param := range job.Function.Params {
		if !isCppIdentifier(param.Name) {
			return fmt.Errorf("function param %d name must be a valid C++ identifier", i)
		}
		if param.Type == "" {
			return fmt.Errorf("function param %d type is required", i)
		}
		adapter, resolveErr := registry.Resolve(param.Type)
		if resolveErr != nil || adapter.CppType() == "void" {
			return fmt.Errorf("unsupported function param %d type %q", i, param.Type)
		}
		paramTypes[i] = adapter
	}

	returnObserved, parameterObservations, err := validateFunctionObservations(*job.Function, returnType, paramTypes)
	if err != nil {
		return err
	}
	if err := validateFunctionPostconditions(*job.Function, returnType, paramTypes); err != nil {
		return err
	}

	seenIDs := make(map[int]struct{}, len(job.TestCases))
	for i, tc := range job.TestCases {
		if _, seen := seenIDs[tc.ID]; seen {
			return fmt.Errorf("duplicate test case id %d", tc.ID)
		}
		seenIDs[tc.ID] = struct{}{}

		if len(tc.Args) != len(paramTypes) {
			return fmt.Errorf("test case %d args count must match function params count", i)
		}
		if len(tc.Expected) == 0 {
			return fmt.Errorf("test case %d expected is required", i)
		}
		if !json.Valid(tc.Expected) {
			return fmt.Errorf("test case %d expected must be valid JSON", i)
		}
		if err := validateFunctionExpected(tc.Expected, returnType, paramTypes, len(job.Function.Observations) > 0, returnObserved, parameterObservations, job.Function.Postconditions); err != nil {
			return fmt.Errorf("test case %d expected: %w", i, err)
		}

		totalSize := len(tc.Expected)
		for argIndex, arg := range tc.Args {
			totalSize += len(arg)
			if !json.Valid(arg) {
				return fmt.Errorf("test case %d argument %d must be valid JSON", i, argIndex)
			}
			if err := paramTypes[argIndex].ValidateJSON(arg); err != nil {
				return fmt.Errorf("test case %d argument %d has wrong type: %w", i, argIndex, err)
			}
		}
		if totalSize > 100_000 {
			return fmt.Errorf("test case %d is too large", i)
		}
	}

	return nil
}

func validateFunctionObservations(function FunctionSpec, returnType functiontypes.Adapter, paramTypes []functiontypes.Adapter) (bool, map[int]ObservationSpec, error) {
	if len(function.Observations) == 0 {
		if returnType.CppType() == "void" {
			return false, nil, fmt.Errorf("void functions require at least one observation")
		}
		return true, nil, nil
	}

	returnObserved := false
	parameterObservations := make(map[int]ObservationSpec, len(function.Observations))
	for i, observation := range function.Observations {
		switch observation.Kind {
		case "return":
			if returnObserved {
				return false, nil, fmt.Errorf("observation %d duplicates the return observation", i)
			}
			if returnType.CppType() == "void" {
				return false, nil, fmt.Errorf("void functions cannot observe a return value")
			}
			returnObserved = true
		case "parameter":
			if observation.Parameter < 0 || observation.Parameter >= len(paramTypes) {
				return false, nil, fmt.Errorf("observation %d references invalid parameter %d", i, observation.Parameter)
			}
			if _, exists := parameterObservations[observation.Parameter]; exists {
				return false, nil, fmt.Errorf("observation %d duplicates parameter %d", i, observation.Parameter)
			}
			view := observation.View
			if view == "" {
				view = "full"
			}
			if view != "full" && view != "prefix" {
				return false, nil, fmt.Errorf("observation %d has unsupported view %q", i, observation.View)
			}
			if view == "prefix" {
				if !observation.LengthFromReturn {
					return false, nil, fmt.Errorf("observation %d prefix view requires lengthFromReturn", i)
				}
				if !strings.HasPrefix(functiontypes.Normalize(function.Params[observation.Parameter].Type), "vector<") {
					return false, nil, fmt.Errorf("observation %d prefix view requires a vector parameter", i)
				}
				if returnType.CppType() != "int" && returnType.CppType() != "long long" {
					return false, nil, fmt.Errorf("observation %d prefix length requires int or long long return type", i)
				}
			} else if observation.LengthFromReturn {
				return false, nil, fmt.Errorf("observation %d lengthFromReturn requires prefix view", i)
			}
			parameterObservations[observation.Parameter] = observation
		default:
			return false, nil, fmt.Errorf("observation %d has unsupported kind %q", i, observation.Kind)
		}
	}
	return returnObserved, parameterObservations, nil
}

func validateFunctionPostconditions(function FunctionSpec, returnType functiontypes.Adapter, paramTypes []functiontypes.Adapter) error {
	if len(function.Postconditions) == 0 {
		return nil
	}
	if returnType.CppType() == "void" {
		return fmt.Errorf("postconditions require a non-void return type")
	}
	if _, ok := returnType.(functiontypes.PostconditionAdapter); !ok {
		return fmt.Errorf("return type %q does not support postconditions", returnType.CanonicalName())
	}
	seen := make(map[string]struct{}, len(function.Postconditions))
	for i, postcondition := range function.Postconditions {
		if postcondition.Kind != "disjoint" && postcondition.Kind != "same_as" {
			return fmt.Errorf("postcondition %d has unsupported kind %q", i, postcondition.Kind)
		}
		if postcondition.Subject != "return" {
			return fmt.Errorf("postcondition %d has unsupported subject %q", i, postcondition.Subject)
		}
		if postcondition.FromParameter < 0 || postcondition.FromParameter >= len(paramTypes) {
			return fmt.Errorf("postcondition %d references invalid parameter %d", i, postcondition.FromParameter)
		}
		if paramTypes[postcondition.FromParameter].CppType() != returnType.CppType() {
			return fmt.Errorf("postcondition %d requires matching return and parameter pointer types", i)
		}
		key := postcondition.Kind + ":" + strconv.Itoa(postcondition.FromParameter)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("postcondition %d duplicates %s", i, key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateFunctionExpected(raw json.RawMessage, returnType functiontypes.Adapter, paramTypes []functiontypes.Adapter, explicitObservations bool, returnObserved bool, parameterObservations map[int]ObservationSpec, postconditions []PostconditionSpec) error {
	if !explicitObservations && len(postconditions) == 0 && returnObserved {
		if err := returnType.ValidateJSON(raw); err != nil {
			return fmt.Errorf("wrong return type: %w", err)
		}
		return nil
	}

	if !explicitObservations && len(postconditions) > 0 {
		if err := returnType.ValidateJSON(raw); err != nil {
			return fmt.Errorf("wrong return type: %w", err)
		}
		return nil
	}

	var expected map[string]json.RawMessage
	if err := json.Unmarshal(raw, &expected); err != nil || expected == nil {
		return fmt.Errorf("expected an observation object")
	}

	returnValue, hasReturn := expected["return"]
	if returnObserved {
		if !hasReturn {
			return fmt.Errorf("return observation is missing")
		}
		if err := returnType.ValidateJSON(returnValue); err != nil {
			return fmt.Errorf("wrong return type: %w", err)
		}
	} else if hasReturn {
		return fmt.Errorf("unexpected return observation")
	}

	parameterValue, hasParameters := expected["parameter"]
	if len(parameterObservations) == 0 {
		if hasParameters {
			return fmt.Errorf("unexpected parameter observations")
		}
	} else {
		if !hasParameters {
			return fmt.Errorf("parameter observations are missing")
		}
		var expectedParameters map[string]json.RawMessage
		if err := json.Unmarshal(parameterValue, &expectedParameters); err != nil || expectedParameters == nil {
			return fmt.Errorf("parameter observation must be an object")
		}
		for parameterIndex := range parameterObservations {
			key := strconv.Itoa(parameterIndex)
			value, ok := expectedParameters[key]
			if !ok {
				return fmt.Errorf("parameter observation %d is missing", parameterIndex)
			}
			if err := paramTypes[parameterIndex].ValidateJSON(value); err != nil {
				return fmt.Errorf("parameter observation %d has wrong type: %w", parameterIndex, err)
			}

		}
		for key := range expectedParameters {
			parameterIndex, err := strconv.Atoi(key)
			if err != nil {
				return fmt.Errorf("parameter observation key %q is invalid", key)
			}
			if _, ok := parameterObservations[parameterIndex]; !ok {
				return fmt.Errorf("unexpected parameter observation %d", parameterIndex)
			}
		}
	}

	if postconditionsValue, hasPostconditions := expected["postconditions"]; hasPostconditions {
		var values map[string]json.RawMessage
		if err := json.Unmarshal(postconditionsValue, &values); err != nil || values == nil {
			return fmt.Errorf("postconditions must be an object")
		}
		if len(values) != len(postconditions) {
			return fmt.Errorf("postconditions count does not match function metadata")
		}
		for i := range postconditions {
			value, ok := values[strconv.Itoa(i)]
			if !ok || string(value) != "true" {
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

func isSupportedCppFunctionType(cppType string) bool {
	_, err := functiontypes.DefaultRegistry().Resolve(cppType)
	return err == nil
}

func isCppIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for i, char := range value {
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(i > 0 && char >= '0' && char <= '9') ||
			char == '_' {
			continue
		}
		return false
	}
	return true
}
