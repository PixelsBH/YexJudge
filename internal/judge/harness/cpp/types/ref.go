package types

import "strings"

// ReferenceKind describes the parameter qualifier that does not change the
// value adapter used to construct or serialize a value.
type ReferenceKind uint8

const (
	NoReference ReferenceKind = iota
	LValueReference
	RValueReference
)

// TypeRef is the normalized representation of a supported C++ type
// expression. Arguments make container handlers recursive instead of forcing
// the registry to enumerate every concrete container combination.
type TypeRef struct {
	Name      string
	Arguments []TypeRef
	Const     bool
	Reference ReferenceKind
	Pointer   bool
}

// ValueName returns the canonical type name without const/reference
// qualifiers. Pointer identity remains part of the value type.
func (t TypeRef) ValueName() string {
	name := t.Name
	if len(t.Arguments) > 0 {
		arguments := make([]string, 0, len(t.Arguments))
		for _, argument := range t.Arguments {
			arguments = append(arguments, argument.ValueName())
		}
		name += "<" + strings.Join(arguments, ",") + ">"
	}
	if t.Pointer {
		name += "*"
	}
	return name
}

// CanonicalName is the registry-facing canonical value name. Parameter
// qualifiers are intentionally omitted because const/reference forms share
// the same construction and serialization adapter.
func (t TypeRef) CanonicalName() string {
	return t.ValueName()
}
