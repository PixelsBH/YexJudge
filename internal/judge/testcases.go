package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	functiontypes "yexjudge/internal/judge/harness/cpp/types"
	"yexjudge/internal/judge/languages"
)

func runTestCases(
	ctx context.Context, executor Executor,
	sandbox *Sandbox, job Job, spec languages.Spec) (Result, error) {
	maxRuntimeMs := 0
	var maxMemoryBytes int64
	total := len(job.TestCases)
	passed := 0

	for _, tc := range job.TestCases {
		ctxRun, cancelRun := context.WithTimeout(
			ctx,
			time.Duration(job.Limits.TimeLimitMs)*time.Millisecond,
		)

		runRes, err := executor.RunTestCase(
			ctxRun,
			sandbox,
			testCaseInput(job, tc),
			spec,
		)

		cancelRun()

		if err != nil {
			return Result{}, err
		}
		if runRes == nil {
			return Result{}, fmt.Errorf("executor returned no run result for test case %d", tc.ID)
		}
		runtimeMs := int(runRes.TimeUsed.Milliseconds())
		if runRes.MemoryUsed > maxMemoryBytes {
			maxMemoryBytes = runRes.MemoryUsed
		}
		memoryMb := int(maxMemoryBytes / (1024 * 1024))
		if ctx.Err() != nil {
			return Result{
				Status:          InfrastructureError,
				PassedTestCases: passed,
				TotalTestCases:  total,
				RuntimeMs:       runtimeMs,
				MemoryMb:        memoryMb,
				FailedTestCase:  failedTestCase(tc, runRes.Stdout),
				ErrorMessage:    "program execution was canceled",
			}, nil
		}

		if runRes.OutputLimitExceeded {
			return Result{
				Status:          OutputLimitExceeded,
				PassedTestCases: passed,
				TotalTestCases:  total,
				RuntimeMs:       runtimeMs,
				MemoryMb:        memoryMb,
				FailedTestCase:  failedTestCase(tc, runRes.Stdout),
				ErrorMessage:    "program output exceeded the allowed limit",
			}, nil
		}

		if runRes.TimedOut {
			return Result{
				Status:          TimeLimitExceeded,
				PassedTestCases: passed,
				TotalTestCases:  total,
				RuntimeMs:       runtimeMs,
				MemoryMb:        memoryMb,
				FailedTestCase:  failedTestCase(tc, runRes.Stdout),
			}, nil
		}

		if runRes.ExitCode != 0 {
			return Result{
				Status:          RuntimeError,
				PassedTestCases: passed,
				TotalTestCases:  total,
				RuntimeMs:       runtimeMs,
				MemoryMb:        memoryMb,
				FailedTestCase:  failedTestCase(tc, runRes.Stdout),
				ErrorMessage:    runRes.Stderr,
			}, nil
		}

		output := strings.TrimSpace(runRes.Stdout)
		expected, err := testCaseExpectedOutput(job, tc)
		if err != nil {
			return Result{}, err
		}
		expected = strings.TrimSpace(expected)

		if !functionOutputsEqual(job, output, expected) {
			return Result{
				Status:          WrongAnswer,
				PassedTestCases: passed,
				TotalTestCases:  total,
				RuntimeMs:       runtimeMs,
				MemoryMb:        memoryMb,
				FailedTestCase:  failedTestCase(tc, output),
			}, nil
		}

		passed++
		if runtimeMs > maxRuntimeMs {
			maxRuntimeMs = runtimeMs
		}
	}

	return Result{
		Status:          Accepted,
		PassedTestCases: passed,
		TotalTestCases:  total,
		RuntimeMs:       maxRuntimeMs,
		MemoryMb:        int(maxMemoryBytes / (1024 * 1024)),
	}, nil
}

func functionOutputsEqual(job Job, actual, expected string) bool {
	if job.Function == nil || job.Function.Comparison == nil || job.Function.Comparison.ReturnArrayOrder != "unordered" {
		return actual == expected
	}

	registry := functiontypes.DefaultRegistry()
	actual, err := canonicalizeUnorderedFunctionOutput(actual, job.Function, registry)
	if err != nil {
		return false
	}
	expected, err = canonicalizeUnorderedFunctionOutput(expected, job.Function, registry)
	return err == nil && actual == expected
}

func canonicalizeUnorderedFunctionOutput(output string, function *FunctionSpec, registry *functiontypes.Registry) (string, error) {
	if len(function.Observations) == 0 && len(function.Postconditions) == 0 {
		return registry.CanonicalUnorderedArrayJSON(function.ReturnType, json.RawMessage(output))
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(output), &fields); err != nil {
		return "", err
	}
	returnValue, ok := fields["return"]
	if !ok {
		return "", fmt.Errorf("return observation is missing")
	}
	canonicalReturn, err := registry.CanonicalUnorderedArrayJSON(function.ReturnType, returnValue)
	if err != nil {
		return "", err
	}
	fields["return"] = json.RawMessage(canonicalReturn)
	canonicalOutput, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	return string(canonicalOutput), nil
}

func failedTestCase(tc TestCase, actualOutput string) *TestCase {
	tc.ActualOutput = strings.TrimSpace(actualOutput)
	return &tc
}

func testCaseInput(job Job, tc TestCase) string {
	if job.Function != nil || job.Class != nil {
		return strconv.Itoa(tc.ID)
	}

	return tc.Input
}

func testCaseExpectedOutput(job Job, tc TestCase) (string, error) {
	if job.Function == nil && job.Class == nil {
		return tc.ExpectedOutput, nil
	}
	if job.Class != nil {
		return canonicalClassExpectedOutput(job, tc)
	}

	registry := functiontypes.DefaultRegistry()
	returnAdapter, err := registry.Resolve(job.Function.ReturnType)
	if err != nil {
		return "", fmt.Errorf("test case %d return type: %w", tc.ID, err)
	}
	if len(job.Function.Observations) == 0 && len(job.Function.Postconditions) == 0 {
		expected, err := returnAdapter.CanonicalJSON(tc.Expected)
		if err != nil {
			return "", fmt.Errorf("test case %d expected: %w", tc.ID, err)
		}
		return expected, nil
	}
	if len(job.Function.Observations) == 0 {
		return canonicalReturnWithPostconditions(tc.Expected, returnAdapter, job.Function.Postconditions, tc.ID)
	}

	var expectedObject map[string]json.RawMessage
	if err := json.Unmarshal(tc.Expected, &expectedObject); err != nil || expectedObject == nil {
		return "", fmt.Errorf("test case %d expected must be an observation object", tc.ID)
	}

	var canonical strings.Builder
	canonical.WriteString("{")
	firstField := true
	parameterObservations := make([]ObservationSpec, 0, len(job.Function.Observations))
	for _, observation := range job.Function.Observations {
		if observation.Kind == "return" {
			value, ok := expectedObject["return"]
			if !ok {
				return "", fmt.Errorf("test case %d return observation is missing", tc.ID)
			}
			canonicalValue, err := returnAdapter.CanonicalJSON(value)
			if err != nil {
				return "", fmt.Errorf("test case %d return observation: %w", tc.ID, err)
			}
			canonical.WriteString("\"return\":")
			canonical.WriteString(canonicalValue)
			firstField = false
		} else if observation.Kind == "parameter" {
			parameterObservations = append(parameterObservations, observation)
		}
	}

	if len(parameterObservations) > 0 {
		parameterObject, ok := expectedObject["parameter"]
		if !ok {
			return "", fmt.Errorf("test case %d parameter observations are missing", tc.ID)
		}
		var values map[string]json.RawMessage
		if err := json.Unmarshal(parameterObject, &values); err != nil || values == nil {
			return "", fmt.Errorf("test case %d parameter observations must be an object", tc.ID)
		}
		if !firstField {
			canonical.WriteString(",")
		}
		canonical.WriteString("\"parameter\":{")
		for i, observation := range parameterObservations {
			key := strconv.Itoa(observation.Parameter)
			value, ok := values[key]
			if !ok {
				return "", fmt.Errorf("test case %d parameter observation %d is missing", tc.ID, observation.Parameter)
			}
			adapter, err := registry.Resolve(job.Function.Params[observation.Parameter].Type)
			if err != nil {
				return "", err
			}
			canonicalValue, err := adapter.CanonicalJSON(value)
			if err != nil {
				return "", fmt.Errorf("test case %d parameter observation %d: %w", tc.ID, observation.Parameter, err)
			}
			if i > 0 {
				canonical.WriteString(",")
			}
			canonical.WriteString(strconv.Quote(key))
			canonical.WriteString(":")
			canonical.WriteString(canonicalValue)
		}
		canonical.WriteString("}")
		firstField = false
	}
	if len(job.Function.Postconditions) > 0 {
		if !firstField {
			canonical.WriteString(",")
		}
		canonical.WriteString(`"postconditions":{`)
		for i := range job.Function.Postconditions {
			if i > 0 {
				canonical.WriteString(",")
			}
			canonical.WriteString(strconv.Quote(strconv.Itoa(i)))
			canonical.WriteString(":true")
		}
		canonical.WriteString("}")
	}
	canonical.WriteString("}")
	return canonical.String(), nil
}

func canonicalClassExpectedOutput(job Job, tc TestCase) (string, error) {
	var expected []json.RawMessage
	if err := json.Unmarshal(tc.Expected, &expected); err != nil {
		return "", fmt.Errorf("test case %d expected must be a JSON array: %w", tc.ID, err)
	}
	if len(expected) != len(tc.Operations) {
		return "", fmt.Errorf("test case %d expected count must match operation count", tc.ID)
	}
	registry := functiontypes.DefaultRegistry()
	operations := make(map[string]ClassOperationSpec, len(job.Class.Operations))
	for _, operation := range job.Class.Operations {
		operations[operation.Name] = operation
	}
	canonical := make([]string, 0, len(expected))
	for i, call := range tc.Operations {
		operation := operations[call.Name]
		adapter, err := registry.Resolve(operation.ReturnType)
		if err != nil {
			return "", err
		}
		if adapter.CppType() == "void" {
			canonical = append(canonical, "null")
			continue
		}
		value, err := adapter.CanonicalJSON(expected[i])
		if err != nil {
			return "", fmt.Errorf("test case %d operation %q expected: %w", tc.ID, call.Name, err)
		}
		canonical = append(canonical, value)
	}
	return "[" + strings.Join(canonical, ",") + "]", nil
}

func canonicalReturnWithPostconditions(raw json.RawMessage, returnAdapter functiontypes.Adapter, postconditions []PostconditionSpec, testCaseID int) (string, error) {
	canonicalValue, err := returnAdapter.CanonicalJSON(raw)
	if err != nil {
		return "", fmt.Errorf("test case %d expected: %w", testCaseID, err)
	}
	var output strings.Builder
	output.WriteString(`{"return":`)
	output.WriteString(canonicalValue)
	output.WriteString(`,"postconditions":{`)
	for i := range postconditions {
		if i > 0 {
			output.WriteString(",")
		}
		output.WriteString(strconv.Quote(strconv.Itoa(i)))
		output.WriteString(":true")
	}
	output.WriteString("}}")
	return output.String(), nil
}
