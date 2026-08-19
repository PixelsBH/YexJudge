// Package harness contains language-independent contracts for generated judge drivers.
package harness

import "encoding/json"

type Parameter struct {
	Name string
	Type string
}

type FunctionSpec struct {
	Name           string
	ReturnType     string
	Params         []Parameter
	Observations   []Observation
	Postconditions []Postcondition
}

type ClassSpec struct {
	Name        string
	Constructor ConstructorSpec
	Operations  []OperationSpec
}

type ConstructorSpec struct {
	Params []Parameter
}

type OperationSpec struct {
	Name       string
	ReturnType string
	Params     []Parameter
}

type Observation struct {
	Kind             string
	Parameter        int
	View             string
	LengthFromReturn bool
}

type Postcondition struct {
	Kind          string
	Subject       string
	FromParameter int
}

type TestCase struct {
	ID              int
	Args            []json.RawMessage
	Expected        json.RawMessage
	ConstructorArgs []json.RawMessage
	Operations      []OperationCall
}

type OperationCall struct {
	Name string
	Args []json.RawMessage
}

type Request struct {
	SourceCode string
	Function   FunctionSpec
	TestCases  []TestCase
}

type Generator interface {
	Generate(Request) (string, error)
}
