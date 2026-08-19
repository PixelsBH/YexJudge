package types

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Adapter describes the code-generation and value-conversion behavior of one
// supported C++ value type. DeserializeExpression is intentionally part of
// the contract even though the first function driver receives values as Go
// JSON; future drivers can use it for runtime decoding without changing the
// generator interface.
type Adapter interface {
	CanonicalName() string
	CppType() string
	GenerateLiteral(json.RawMessage) (string, error)
	SerializeExpression(valueExpression string) string
	DeserializeExpression(inputExpression string) string
	ValidateJSON(json.RawMessage) error
	CanonicalJSON(json.RawMessage) (string, error)
}

type AdapterFactory func(arguments []Adapter) (Adapter, error)

type Registry struct {
	adapters  map[string]Adapter
	factories map[string]AdapterFactory
}

func NewRegistry(adapters ...Adapter) *Registry {
	registry := &Registry{
		adapters:  make(map[string]Adapter, len(adapters)),
		factories: make(map[string]AdapterFactory),
	}
	for _, adapter := range adapters {
		// Built-in registries use unique names. Custom registries can use
		// RegisterAdapter when they need duplicate-name validation.
		registry.adapters[adapter.CanonicalName()] = adapter
	}
	return registry
}

func (r *Registry) RegisterAdapter(adapter Adapter) error {
	if adapter == nil || adapter.CanonicalName() == "" {
		return fmt.Errorf("type adapter and canonical name are required")
	}
	name := adapter.CanonicalName()
	if _, exists := r.adapters[name]; exists {
		return fmt.Errorf("type adapter %q is already registered", name)
	}
	r.adapters[name] = adapter
	return nil
}

func (r *Registry) RegisterFactory(name string, factory AdapterFactory) error {
	if name == "" || factory == nil {
		return fmt.Errorf("type factory name and factory are required")
	}
	if _, exists := r.factories[name]; exists {
		return fmt.Errorf("type factory %q is already registered", name)
	}
	r.factories[name] = factory
	return nil
}

func DefaultRegistry() *Registry {
	intType := scalarAdapter{name: "int", cppType: "int", literal: integerLiteral(32), canonical: integerCanonical(32)}
	longLongType := scalarAdapter{name: "long long", cppType: "long long", literal: integerLiteral(64), canonical: integerCanonical(64)}
	doubleType := scalarAdapter{name: "double", cppType: "double", literal: floatingLiteral, canonical: floatingCanonical}
	boolType := scalarAdapter{name: "bool", cppType: "bool", literal: booleanLiteral, canonical: booleanCanonical}
	stringType := scalarAdapter{name: "string", cppType: "string", literal: stringLiteral, canonical: stringCanonical}

	registry := NewRegistry(
		intType,
		longLongType,
		doubleType,
		boolType,
		stringType,
		voidAdapter{},
		listNodeAdapter(),
		randomListNodeAdapter("RandomListNode*"),
		randomListNodeAdapter("Node*"),
		treeNodeAdapter(),
		graphNodeAdapter("GraphNode*"),
	)
	if err := registry.RegisterFactory("vector", func(arguments []Adapter) (Adapter, error) {
		if len(arguments) != 1 {
			return nil, fmt.Errorf("vector requires exactly one type argument")
		}
		return vectorAdapter{element: arguments[0]}, nil
	}); err != nil {
		panic(err)
	}
	if err := registry.RegisterFactory("optional", optionalFactory); err != nil {
		panic(err)
	}
	return registry
}

func (r *Registry) Resolve(declaredType string) (Adapter, error) {
	ref, err := Parse(declaredType)
	if err != nil {
		return nil, fmt.Errorf("invalid C++ type %q: %w", declaredType, err)
	}
	return r.resolveRef(ref)
}

func (r *Registry) resolveRef(ref TypeRef) (Adapter, error) {
	if len(ref.Arguments) > 0 {
		factory, ok := r.factories[ref.Name]
		if !ok {
			return nil, fmt.Errorf("unsupported C++ type %q", ref.CanonicalName())
		}
		arguments := make([]Adapter, 0, len(ref.Arguments))
		for _, argumentRef := range ref.Arguments {
			argument, err := r.resolveRef(argumentRef)
			if err != nil {
				return nil, err
			}
			arguments = append(arguments, argument)
		}
		// Qualifiers are valid for both parameters and values. They do not
		// change the adapter selected for construction or serialization.
		return factory(arguments)
	}

	adapter, ok := r.adapters[ref.CanonicalName()]
	if !ok {
		return nil, fmt.Errorf("unsupported C++ type %q", ref.CanonicalName())
	}
	return adapter, nil
}

func (r *Registry) CanonicalJSON(declaredType string, raw json.RawMessage) (string, error) {
	adapter, err := r.Resolve(declaredType)
	if err != nil {
		return "", err
	}
	return adapter.CanonicalJSON(raw)
}

// SupportSource returns each selected runtime type's helper declarations
// exactly once. Primitive serializers remain in the C++ backend; custom type
// declarations and graph helpers are contributed by their adapters. When no
// adapters are supplied, all registered runtime sources are returned for
// registry-level inspection.
func (r *Registry) SupportSource(selected ...Adapter) string {
	adapters := selected
	if len(adapters) == 0 {
		keys := make([]string, 0, len(r.adapters))
		for key := range r.adapters {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		adapters = make([]Adapter, 0, len(keys))
		for _, key := range keys {
			adapters = append(adapters, r.adapters[key])
		}
	}

	seen := make(map[string]struct{})
	var sources []string
	for _, adapter := range adapters {
		provider, ok := adapter.(SupportSourceAdapter)
		if !ok {
			continue
		}
		source := provider.SupportSource()
		if source == "" {
			continue
		}
		if _, exists := seen[source]; exists {
			continue
		}
		seen[source] = struct{}{}
		sources = append(sources, source)
	}
	return strings.Join(sources, "\n")
}

// Normalize returns the canonical value name while ignoring const/reference
// qualifiers. It is retained as a compatibility helper for the judge layer.
func Normalize(declaredType string) string {
	ref, err := Parse(declaredType)
	if err != nil {
		return declaredType
	}
	return ref.CanonicalName()
}
