package types

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type scalarAdapter struct {
	name      string
	cppType   string
	literal   func(json.RawMessage) (string, error)
	canonical func(json.RawMessage) (string, error)
}

type voidAdapter struct{}

func (voidAdapter) CanonicalName() string { return "void" }
func (voidAdapter) CppType() string       { return "void" }
func (voidAdapter) GenerateLiteral(json.RawMessage) (string, error) {
	return "", fmt.Errorf("void cannot be used as a parameter type")
}
func (voidAdapter) SerializeExpression(string) string { return "" }
func (voidAdapter) DeserializeExpression(inputExpression string) string {
	return inputExpression
}
func (voidAdapter) ValidateJSON(json.RawMessage) error {
	return fmt.Errorf("void does not have a JSON value")
}
func (voidAdapter) CanonicalJSON(json.RawMessage) (string, error) {
	return "", fmt.Errorf("void does not have a JSON value")
}

func (a scalarAdapter) CanonicalName() string { return a.name }
func (a scalarAdapter) CppType() string       { return a.cppType }
func (a scalarAdapter) GenerateLiteral(raw json.RawMessage) (string, error) {
	return a.literal(raw)
}
func (a scalarAdapter) SerializeExpression(valueExpression string) string {
	return "__serialize(" + valueExpression + ")"
}
func (a scalarAdapter) DeserializeExpression(inputExpression string) string {
	return inputExpression
}
func (a scalarAdapter) ValidateJSON(raw json.RawMessage) error {
	_, err := a.canonical(raw)
	return err
}
func (a scalarAdapter) CanonicalJSON(raw json.RawMessage) (string, error) {
	return a.canonical(raw)
}

func integerLiteral(bits int) func(json.RawMessage) (string, error) {
	return func(raw json.RawMessage) (string, error) {
		value, err := parseInteger(raw, bits)
		if err != nil {
			return "", err
		}
		return strconv.FormatInt(value, 10), nil
	}
}

func integerCanonical(bits int) func(json.RawMessage) (string, error) {
	return integerLiteral(bits)
}

func parseInteger(raw json.RawMessage, bits int) (int64, error) {
	var value json.Number
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, fmt.Errorf("expected integer: %w", err)
	}
	parsed, err := strconv.ParseInt(value.String(), 10, bits)
	if err != nil {
		return 0, fmt.Errorf("expected %d-bit integer: %w", bits, err)
	}
	return parsed, nil
}

func floatingLiteral(raw json.RawMessage) (string, error) {
	value, err := parseFloat(raw)
	if err != nil {
		return "", err
	}
	return strconv.FormatFloat(value, 'g', -1, 64), nil
}

func floatingCanonical(raw json.RawMessage) (string, error) {
	return floatingLiteral(raw)
}

func parseFloat(raw json.RawMessage) (float64, error) {
	var value json.Number
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, fmt.Errorf("expected number: %w", err)
	}
	parsed, err := strconv.ParseFloat(value.String(), 64)
	if err != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) {
		return 0, fmt.Errorf("expected finite number")
	}
	return parsed, nil
}

func booleanLiteral(raw json.RawMessage) (string, error) {
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("expected boolean: %w", err)
	}
	if value {
		return "true", nil
	}
	return "false", nil
}

func booleanCanonical(raw json.RawMessage) (string, error) {
	return booleanLiteral(raw)
}

func stringLiteral(raw json.RawMessage) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("expected string: %w", err)
	}
	return strconv.Quote(value), nil
}

func stringCanonical(raw json.RawMessage) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("expected string: %w", err)
	}

	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(encoded.String(), "\n"), nil
}
