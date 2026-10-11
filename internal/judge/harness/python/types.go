package python

import (
	"fmt"
	"strings"

	functiontypes "yexjudge/internal/judge/harness/cpp/types"
)

func normalizeType(declared string) (functiontypes.TypeRef, string, error) {
	ref, err := functiontypes.Parse(declared)
	if err != nil {
		return functiontypes.TypeRef{}, "", err
	}
	if err := validateType(ref, true); err != nil {
		return functiontypes.TypeRef{}, "", err
	}
	return ref, ref.CanonicalName(), nil
}

func validateType(ref functiontypes.TypeRef, allowVoid bool) error {
	if ref.Pointer {
		if len(ref.Arguments) != 0 {
			return fmt.Errorf("pointer containers are not supported")
		}
		switch ref.Name {
		case "ListNode", "TreeNode", "RandomListNode", "Node", "GraphNode":
			return nil
		default:
			return fmt.Errorf("unsupported Python runtime type %q", ref.CanonicalName())
		}
	}
	if len(ref.Arguments) > 0 {
		if ref.Name != "vector" && ref.Name != "optional" {
			return fmt.Errorf("unsupported Python container type %q", ref.Name)
		}
		if len(ref.Arguments) != 1 {
			return fmt.Errorf("%s requires exactly one type argument", ref.Name)
		}
		if err := validateType(ref.Arguments[0], false); err != nil {
			return err
		}
		return nil
	}
	switch ref.Name {
	case "int", "long long", "double", "bool", "string":
		return nil
	case "void":
		if allowVoid {
			return nil
		}
		return fmt.Errorf("void is not a value type")
	default:
		return fmt.Errorf("unsupported Python type %q", ref.CanonicalName())
	}
}

func isVoid(declared string) bool {
	ref, err := functiontypes.Parse(declared)
	return err == nil && ref.Name == "void" && len(ref.Arguments) == 0 && !ref.Pointer
}

func pythonTypeName(declared string) (string, error) {
	ref, err := functiontypes.Parse(declared)
	if err != nil {
		return "", err
	}
	if err := validateType(ref, true); err != nil {
		return "", err
	}
	return ref.CanonicalName(), nil
}

func isContainerType(declared string) bool {
	ref, err := functiontypes.Parse(declared)
	return err == nil && ref.Name == "vector" && len(ref.Arguments) == 1 && !ref.Pointer
}

func pythonIdentifier(value string) bool {
	if value == "" || !(value[0] == '_' || value[0] >= 'A' && value[0] <= 'Z' || value[0] >= 'a' && value[0] <= 'z') {
		return false
	}
	for i := 1; i < len(value); i++ {
		char := value[i]
		if char != '_' && !(char >= 'A' && char <= 'Z') && !(char >= 'a' && char <= 'z') && !(char >= '0' && char <= '9') {
			return false
		}
	}
	_, keyword := pythonKeywords[value]
	return !keyword
}

var pythonKeywords = func() map[string]struct{} {
	words := strings.Fields("False None True and as assert async await break class continue def del elif else except finally for from global if import in is lambda nonlocal not or pass raise return try while with yield")
	keywords := make(map[string]struct{}, len(words))
	for _, word := range words {
		keywords[word] = struct{}{}
	}
	return keywords
}()
