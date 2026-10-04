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
	MaxTimeLimitMs           = 10_000
	MaxMemoryLimitMb         = 512
	MaxSourceBytes           = 100_000
	MaxTestCaseBytes         = 100_000
	MaxJobPayloadBytes       = 10 * 1024 * 1024
	MaxMetadataStringBytes   = 128
	MaxClassOperations       = 64
	MaxClassCallsPerCase     = 1000
	MaxClassCallsPerJob      = 10_000
	MaxJSONNesting           = 64
	MaxJobRequestedRuntimeMs = 120_000
)

func ValidateJob(job Job) error {
	// Bound host-side parsing and harness generation before resolving types,
	// unmarshalling JSON, or constructing generated C++ source.
	if err := validateJobWork(job); err != nil {
		return err
	}
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

	if len(job.SourceCode) > MaxSourceBytes {
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

func validateJobWork(job Job) error {
	if len(job.SourceCode) > MaxSourceBytes {
		return fmt.Errorf("sourceCode is too large")
	}
	if len(job.TestCases) > 100 {
		return fmt.Errorf("too many test cases")
	}
	if len(job.Language) > MaxMetadataStringBytes || len(job.Mode) > MaxMetadataStringBytes {
		return fmt.Errorf("execution metadata is too large")
	}
	if job.Limits.TimeLimitMs > 0 && job.Limits.TimeLimitMs <= MaxTimeLimitMs &&
		len(job.TestCases)*job.Limits.TimeLimitMs > MaxJobRequestedRuntimeMs {
		return fmt.Errorf("total requested testcase runtime must not exceed %d ms", MaxJobRequestedRuntimeMs)
	}
	checkString := func(value string) error {
		if len(value) > MaxMetadataStringBytes {
			return fmt.Errorf("driver metadata string is too large")
		}
		return nil
	}
	checkParams := func(params []FunctionParam) error {
		if len(params) > 10 {
			return fmt.Errorf("too many driver parameters")
		}
		for _, param := range params {
			if len(param.Name) > MaxMetadataStringBytes || len(param.Type) > MaxMetadataStringBytes {
				return fmt.Errorf("driver parameter metadata is too large")
			}
		}
		return nil
	}
	if job.Function != nil {
		f := job.Function
		if err := checkString(f.Name); err != nil {
			return err
		}
		if err := checkString(f.ReturnType); err != nil {
			return err
		}
		if err := checkParams(f.Params); err != nil {
			return err
		}
		if len(f.Observations) > 11 || len(f.Postconditions) > 10 {
			return fmt.Errorf("too many driver observations or postconditions")
		}
		for _, observation := range f.Observations {
			if err := checkString(observation.Kind); err != nil {
				return err
			}
			if err := checkString(observation.View); err != nil {
				return err
			}
		}
		for _, postcondition := range f.Postconditions {
			if err := checkString(postcondition.Kind); err != nil {
				return err
			}
			if err := checkString(postcondition.Subject); err != nil {
				return err
			}
		}
		if f.Comparison != nil {
			if err := checkString(f.Comparison.ReturnArrayOrder); err != nil {
				return err
			}
		}
	}
	if job.Class != nil {
		c := job.Class
		if err := checkString(c.Name); err != nil {
			return err
		}
		if err := checkParams(c.Constructor.Params); err != nil {
			return err
		}
		if len(c.Operations) > MaxClassOperations {
			return fmt.Errorf("too many class operation declarations")
		}
		for _, operation := range c.Operations {
			if err := checkString(operation.Name); err != nil {
				return err
			}
			if err := checkString(operation.ReturnType); err != nil {
				return err
			}
			if err := checkParams(operation.Params); err != nil {
				return err
			}
		}
	}
	total, calls := len(job.SourceCode), 0
	for i, tc := range job.TestCases {
		if len(tc.Input) > MaxTestCaseBytes {
			return fmt.Errorf("test case %d input is too large", i)
		}
		if len(tc.ExpectedOutput) > MaxTestCaseBytes || len(tc.ActualOutput) > MaxTestCaseBytes {
			return fmt.Errorf("test case %d expectedOutput is too large", i)
		}
		if len(tc.Args) > 10 || len(tc.ConstructorArgs) > 10 || len(tc.Operations) > MaxClassCallsPerCase {
			return fmt.Errorf("test case %d contains too many arguments or operations", i)
		}
		calls += len(tc.Operations)
		if calls > MaxClassCallsPerJob {
			return fmt.Errorf("too many total class operation calls")
		}
		structuredSize := 0
		checkRaw := func(raw json.RawMessage) error {
			if len(raw) > MaxTestCaseBytes-structuredSize {
				return fmt.Errorf("test case %d is too large", i)
			}
			structuredSize += len(raw)
			if !jsonNestingBounded(raw) {
				return fmt.Errorf("test case %d JSON nesting exceeds limit", i)
			}
			return nil
		}
		if err := checkRaw(tc.Expected); err != nil {
			return err
		}
		for _, arg := range tc.Args {
			if err := checkRaw(arg); err != nil {
				return err
			}
		}
		for _, arg := range tc.ConstructorArgs {
			if err := checkRaw(arg); err != nil {
				return err
			}
		}
		for _, operation := range tc.Operations {
			if err := checkString(operation.Name); err != nil {
				return err
			}
			if len(operation.Args) > 10 {
				return fmt.Errorf("test case %d contains too many operation arguments", i)
			}
			structuredSize += len(operation.Name)
			if structuredSize > MaxTestCaseBytes {
				return fmt.Errorf("test case %d is too large", i)
			}
			for _, arg := range operation.Args {
				if err := checkRaw(arg); err != nil {
					return err
				}
			}
		}
		total += len(tc.Input) + len(tc.ExpectedOutput) + len(tc.ActualOutput) + structuredSize
		if total > MaxJobPayloadBytes {
			return fmt.Errorf("total job payload exceeds size limit")
		}
	}
	return nil
}

// This is only a work bound; semantic JSON validation still happens below.
func jsonNestingBounded(raw []byte) bool {
	depth := 0
	quoted, escaped := false, false
	for _, b := range raw {
		if quoted {
			if escaped {
				escaped = false
			} else if b == '\\' {
				escaped = true
			} else if b == '"' {
				quoted = false
			}
			continue
		}
		switch b {
		case '"':
			quoted = true
		case '[', '{':
			depth++
			if depth > MaxJSONNesting {
				return false
			}
		case ']', '}':
			depth--
		}
	}
	return true
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
	if err := validateFunctionComparison(job.Function.Comparison, returnType, returnObserved); err != nil {
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

func validateFunctionComparison(comparison *FunctionComparisonSpec, returnType functiontypes.Adapter, returnObserved bool) error {
	if comparison == nil || comparison.ReturnArrayOrder == "" {
		return nil
	}
	if comparison.ReturnArrayOrder != "unordered" {
		return fmt.Errorf("unsupported returnArrayOrder %q", comparison.ReturnArrayOrder)
	}
	if !returnObserved {
		return fmt.Errorf("unordered returnArrayOrder requires an observed return value")
	}
	if !strings.HasPrefix(returnType.CanonicalName(), "vector<") {
		return fmt.Errorf("unordered returnArrayOrder requires a vector return type")
	}
	return nil
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
