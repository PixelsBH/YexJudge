package languages

import (
	"reflect"
	"testing"
)

func TestCppCompileCommandUsesPCHFlagsAndImage(t *testing.T) {
	command := (Cpp{}).CompileCommand()
	if len(command) < 2 || command[0] != "g++" || !reflect.DeepEqual(command[1:5], []string{"-O2", "-pipe", "-static", "-s"}) {
		t.Fatalf("CompileCommand() = %v, want compiler flags shared with PCH build", command)
	}
	if image := (Cpp{}).CompileImage(); image != CppCompileImage {
		t.Fatalf("CompileImage() = %q, want %q", image, CppCompileImage)
	}
}
