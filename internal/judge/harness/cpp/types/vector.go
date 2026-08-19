package types

import (
	"encoding/json"
	"fmt"
	"strings"
)

type vectorAdapter struct {
	element Adapter
}

func (a vectorAdapter) CanonicalName() string {
	return "vector<" + a.element.CanonicalName() + ">"
}
func (a vectorAdapter) CppType() string { return a.CanonicalName() }

func (a vectorAdapter) GenerateLiteral(raw json.RawMessage) (string, error) {
	values, err := arrayValues(raw)
	if err != nil {
		return "", err
	}

	literals := make([]string, 0, len(values))
	for i, value := range values {
		literal, err := a.element.GenerateLiteral(value)
		if err != nil {
			return "", fmt.Errorf("element %d: %w", i, err)
		}
		literals = append(literals, literal)
	}
	return a.CppType() + "{" + strings.Join(literals, ", ") + "}", nil
}

func (a vectorAdapter) SerializeExpression(valueExpression string) string {
	return "__serialize(" + valueExpression + ")"
}
func (a vectorAdapter) DeserializeExpression(inputExpression string) string {
	return inputExpression
}
func (a vectorAdapter) SupportSource() string {
	provider, ok := a.element.(SupportSourceAdapter)
	if !ok {
		return ""
	}
	return provider.SupportSource()
}
func (a vectorAdapter) ValidateJSON(raw json.RawMessage) error {
	_, err := a.CanonicalJSON(raw)
	return err
}
func (a vectorAdapter) CanonicalJSON(raw json.RawMessage) (string, error) {
	values, err := arrayValues(raw)
	if err != nil {
		return "", err
	}

	canonical := make([]string, 0, len(values))
	for i, value := range values {
		item, err := a.element.CanonicalJSON(value)
		if err != nil {
			return "", fmt.Errorf("element %d: %w", i, err)
		}
		canonical = append(canonical, item)
	}
	return "[" + strings.Join(canonical, ",") + "]", nil
}

func arrayValues(raw json.RawMessage) ([]json.RawMessage, error) {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("expected array: %w", err)
	}
	return values, nil
}
