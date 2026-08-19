package harness

import "testing"

type testModeGenerator struct {
	mode ExecutionMode
}

func (g testModeGenerator) Mode() ExecutionMode { return g.mode }
func (g testModeGenerator) GenerateMode(ModeRequest) (GeneratedProgram, error) {
	return GeneratedProgram{}, nil
}

func TestModeRegistryResolvesRegisteredModes(t *testing.T) {
	registry := NewModeRegistry(testModeGenerator{mode: ModeFunction})
	if _, ok := registry.Get(ModeFunction); !ok {
		t.Fatal("ModeRegistry did not return the registered function mode")
	}
	if _, ok := registry.Get(ModeClass); ok {
		t.Fatal("ModeRegistry returned an unregistered class mode")
	}
}

func TestValidateContractKeepsModesIndependent(t *testing.T) {
	if err := ValidateContract(ExecutionContract{Mode: ModeFunction}); err == nil {
		t.Fatal("ValidateContract accepted function mode without a function contract")
	}
	if err := ValidateContract(ExecutionContract{Mode: ModeStdin, Function: &FunctionSpec{}}); err == nil {
		t.Fatal("ValidateContract accepted a function contract for stdin mode")
	}
	if err := ValidateContract(ExecutionContract{
		Mode:  ModeClass,
		Class: &ClassSpec{Name: "Example"},
	}); err != nil {
		t.Fatalf("ValidateContract rejected a class contract: %v", err)
	}
	if err := ValidateContract(ExecutionContract{Mode: ModeClass}); err == nil {
		t.Fatal("ValidateContract accepted class mode without a class contract")
	}
}
