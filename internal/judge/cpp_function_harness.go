package judge

import (
	"yexjudge/internal/judge/harness"
	"yexjudge/internal/judge/harness/cpp"
	"yexjudge/internal/judge/harness/cpp/types"
)

// buildCppFunctionHarness is the judge-layer entry point for the reusable
// C++ function mode generator.
func buildCppFunctionHarness(job Job) (string, error) {
	request := harness.Request{
		SourceCode: job.SourceCode,
		Function: harness.FunctionSpec{
			Name:           job.Function.Name,
			ReturnType:     job.Function.ReturnType,
			Params:         make([]harness.Parameter, 0, len(job.Function.Params)),
			Observations:   make([]harness.Observation, 0, len(job.Function.Observations)),
			Postconditions: make([]harness.Postcondition, 0, len(job.Function.Postconditions)),
		},
		TestCases: make([]harness.TestCase, 0, len(job.TestCases)),
	}

	for _, parameter := range job.Function.Params {
		request.Function.Params = append(request.Function.Params, harness.Parameter{
			Name: parameter.Name,
			Type: parameter.Type,
		})
	}
	for _, observation := range job.Function.Observations {
		request.Function.Observations = append(request.Function.Observations, harness.Observation{
			Kind:             observation.Kind,
			Parameter:        observation.Parameter,
			View:             observation.View,
			LengthFromReturn: observation.LengthFromReturn,
		})
	}
	for _, postcondition := range job.Function.Postconditions {
		request.Function.Postconditions = append(request.Function.Postconditions, harness.Postcondition{
			Kind:          postcondition.Kind,
			Subject:       postcondition.Subject,
			FromParameter: postcondition.FromParameter,
		})
	}
	for _, testCase := range job.TestCases {
		request.TestCases = append(request.TestCases, harness.TestCase{
			ID:       testCase.ID,
			Args:     testCase.Args,
			Expected: testCase.Expected,
		})
	}

	return cpp.NewFunctionGenerator(types.DefaultRegistry()).Generate(request)
}

func buildCppClassHarness(job Job) (string, error) {
	request := cpp.ClassRequest{
		SourceCode: job.SourceCode,
		Class: harness.ClassSpec{
			Name: job.Class.Name,
			Constructor: harness.ConstructorSpec{
				Params: make([]harness.Parameter, 0, len(job.Class.Constructor.Params)),
			},
			Operations: make([]harness.OperationSpec, 0, len(job.Class.Operations)),
		},
		TestCases: make([]harness.TestCase, 0, len(job.TestCases)),
	}
	for _, parameter := range job.Class.Constructor.Params {
		request.Class.Constructor.Params = append(request.Class.Constructor.Params, harness.Parameter{
			Name: parameter.Name,
			Type: parameter.Type,
		})
	}
	for _, operation := range job.Class.Operations {
		converted := harness.OperationSpec{
			Name:       operation.Name,
			ReturnType: operation.ReturnType,
			Params:     make([]harness.Parameter, 0, len(operation.Params)),
		}
		for _, parameter := range operation.Params {
			converted.Params = append(converted.Params, harness.Parameter{
				Name: parameter.Name,
				Type: parameter.Type,
			})
		}
		request.Class.Operations = append(request.Class.Operations, converted)
	}
	for _, testCase := range job.TestCases {
		operations := make([]harness.OperationCall, 0, len(testCase.Operations))
		for _, operation := range testCase.Operations {
			operations = append(operations, harness.OperationCall{Name: operation.Name, Args: operation.Args})
		}
		request.TestCases = append(request.TestCases, harness.TestCase{
			ID:              testCase.ID,
			Expected:        testCase.Expected,
			ConstructorArgs: testCase.ConstructorArgs,
			Operations:      operations,
		})
	}
	return cpp.NewClassGenerator(types.DefaultRegistry()).Generate(request)
}
