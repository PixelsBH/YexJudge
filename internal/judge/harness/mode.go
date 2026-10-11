package harness

import "fmt"

type ExecutionMode string

const (
	ModeStdin       ExecutionMode = "stdin"
	ModeFunction    ExecutionMode = "function"
	ModeClass       ExecutionMode = "class"
	ModeInteractive ExecutionMode = "interactive"
	ModeSQL         ExecutionMode = "sql"
	ModeShell       ExecutionMode = "shell"
)

func (m ExecutionMode) Valid() bool {
	switch m {
	case ModeStdin, ModeFunction, ModeClass, ModeInteractive, ModeSQL, ModeShell:
		return true
	default:
		return false
	}
}

type ExecutionContract struct {
	Mode     ExecutionMode
	Function *FunctionSpec
	Class    *ClassSpec
}

type ModeRequest struct {
	SourceCode string
	Contract   ExecutionContract
	TestCases  []TestCase
}

type GeneratedProgram struct {
	SourceCode string
	SourceFile string
}

type ModeGenerator interface {
	Language() string
	Mode() ExecutionMode
	GenerateMode(ModeRequest) (GeneratedProgram, error)
}

type modeKey struct {
	language string
	mode     ExecutionMode
}

type ModeRegistry struct {
	generators map[modeKey]ModeGenerator
}

func NewModeRegistry(generators ...ModeGenerator) *ModeRegistry {
	registry := &ModeRegistry{generators: make(map[modeKey]ModeGenerator, len(generators))}
	for _, generator := range generators {
		registry.generators[modeKey{language: generator.Language(), mode: generator.Mode()}] = generator
	}
	return registry
}

func (r *ModeRegistry) Get(language string, mode ExecutionMode) (ModeGenerator, bool) {
	generator, ok := r.generators[modeKey{language: language, mode: mode}]
	return generator, ok
}

func ValidateContract(contract ExecutionContract) error {
	if !contract.Mode.Valid() {
		return fmt.Errorf("unsupported execution mode %q", contract.Mode)
	}
	if contract.Mode == ModeFunction && contract.Function == nil {
		return fmt.Errorf("function contract is required for function mode")
	}
	if contract.Mode != ModeFunction && contract.Function != nil {
		return fmt.Errorf("function contract is only valid in function mode")
	}
	if contract.Mode == ModeClass && contract.Class == nil {
		return fmt.Errorf("class contract is required for class mode")
	}
	if contract.Mode != ModeClass && contract.Class != nil {
		return fmt.Errorf("class contract is only valid in class mode")
	}
	return nil
}
