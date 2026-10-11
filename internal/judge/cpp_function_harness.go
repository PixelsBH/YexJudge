package judge

import (
	"fmt"

	"yexjudge/internal/judge/harness"
	"yexjudge/internal/judge/harness/cpp"

	pythonharness "yexjudge/internal/judge/harness/python"
)

var executionModeRegistry = harness.NewModeRegistry(
	cpp.NewFunctionGenerator(nil),
	cpp.NewClassGenerator(nil),
	pythonharness.NewFunctionGenerator(),
	pythonharness.NewClassGenerator(),
)

func generateModeProgram(job Job) (harness.GeneratedProgram, error) {
	mode := job.ExecutionMode()
	generator, ok := executionModeRegistry.Get(job.Language, mode)
	if !ok {
		return harness.GeneratedProgram{}, fmt.Errorf("%s mode %q is not supported", job.Language, mode)
	}
	request := harness.ModeRequest{SourceCode: job.SourceCode, Contract: harness.ExecutionContract{Mode: mode}}
	if job.Function != nil {
		function := harness.FunctionSpec{
			Name: job.Function.Name, ReturnType: job.Function.ReturnType,
			Params:         make([]harness.Parameter, 0, len(job.Function.Params)),
			Observations:   make([]harness.Observation, 0, len(job.Function.Observations)),
			Postconditions: make([]harness.Postcondition, 0, len(job.Function.Postconditions)),
		}
		for _, parameter := range job.Function.Params {
			function.Params = append(function.Params, harness.Parameter{Name: parameter.Name, Type: parameter.Type})
		}
		for _, observation := range job.Function.Observations {
			function.Observations = append(function.Observations, harness.Observation{
				Kind: observation.Kind, Parameter: observation.Parameter, View: observation.View,
				LengthFromReturn: observation.LengthFromReturn,
			})
		}
		for _, condition := range job.Function.Postconditions {
			function.Postconditions = append(function.Postconditions, harness.Postcondition{
				Kind: condition.Kind, Subject: condition.Subject, FromParameter: condition.FromParameter,
			})
		}
		if job.Function.Comparison != nil {
			function.Comparison = &harness.FunctionComparison{ReturnArrayOrder: job.Function.Comparison.ReturnArrayOrder}
		}
		request.Contract.Function = &function
	}
	if job.Class != nil {
		class := harness.ClassSpec{
			Name:        job.Class.Name,
			Constructor: harness.ConstructorSpec{Params: make([]harness.Parameter, 0, len(job.Class.Constructor.Params))},
			Operations:  make([]harness.OperationSpec, 0, len(job.Class.Operations)),
		}
		for _, parameter := range job.Class.Constructor.Params {
			class.Constructor.Params = append(class.Constructor.Params, harness.Parameter{Name: parameter.Name, Type: parameter.Type})
		}
		for _, operation := range job.Class.Operations {
			converted := harness.OperationSpec{Name: operation.Name, ReturnType: operation.ReturnType, Params: make([]harness.Parameter, 0, len(operation.Params))}
			for _, parameter := range operation.Params {
				converted.Params = append(converted.Params, harness.Parameter{Name: parameter.Name, Type: parameter.Type})
			}
			class.Operations = append(class.Operations, converted)
		}
		request.Contract.Class = &class
	}
	request.TestCases = make([]harness.TestCase, 0, len(job.TestCases))
	for _, testCase := range job.TestCases {
		converted := harness.TestCase{
			ID: testCase.ID, Args: testCase.Args, Expected: testCase.Expected,
			ConstructorArgs: testCase.ConstructorArgs,
		}
		for _, operation := range testCase.Operations {
			converted.Operations = append(converted.Operations, harness.OperationCall{Name: operation.Name, Args: operation.Args})
		}
		request.TestCases = append(request.TestCases, converted)
	}
	return generator.GenerateMode(request)
}

// buildCppFunctionHarness is the judge-layer entry point for the reusable
// C++ function mode generator.
func buildCppFunctionHarness(job Job) (string, error) {
	program, err := generateModeProgram(job)
	return program.SourceCode, err
}

func buildCppClassHarness(job Job) (string, error) {
	program, err := generateModeProgram(job)
	return program.SourceCode, err
}
