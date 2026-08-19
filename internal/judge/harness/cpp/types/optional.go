package types

import (
	"encoding/json"
	"fmt"
	"strings"
)

type optionalAdapter struct {
	element Adapter
}

func (a optionalAdapter) CanonicalName() string {
	return "optional<" + a.element.CanonicalName() + ">"
}
func (a optionalAdapter) CppType() string { return a.CanonicalName() }

func (a optionalAdapter) GenerateLiteral(raw json.RawMessage) (string, error) {
	if strings.TrimSpace(string(raw)) == "null" {
		return a.CppType() + "{}", nil
	}
	literal, err := a.element.GenerateLiteral(raw)
	if err != nil {
		return "", err
	}
	return a.CppType() + "{" + literal + "}", nil
}

func (a optionalAdapter) SerializeExpression(valueExpression string) string {
	return "__serialize(" + valueExpression + ")"
}
func (a optionalAdapter) DeserializeExpression(inputExpression string) string {
	return inputExpression
}
func (a optionalAdapter) SupportSource() string {
	provider, ok := a.element.(SupportSourceAdapter)
	if !ok {
		return ""
	}
	return provider.SupportSource()
}
func (a optionalAdapter) ValidateJSON(raw json.RawMessage) error {
	if strings.TrimSpace(string(raw)) == "null" {
		return nil
	}
	return a.element.ValidateJSON(raw)
}
func (a optionalAdapter) CanonicalJSON(raw json.RawMessage) (string, error) {
	if strings.TrimSpace(string(raw)) == "null" {
		return "null", nil
	}
	return a.element.CanonicalJSON(raw)
}

func optionalFactory(arguments []Adapter) (Adapter, error) {
	if len(arguments) != 1 {
		return nil, fmt.Errorf("optional requires exactly one type argument")
	}
	return optionalAdapter{element: arguments[0]}, nil
}
