package languages

import (
	_ "embed"
	"strings"
)

//go:embed cpp_compile_flags.txt
var cppCompileFlags string

const CppCompileImage = "yexjudge-cpp-compile:latest"

type Cpp struct{}

func (Cpp) Name() string {
	return "cpp"
}

func (Cpp) SourceFileName() string {
	return "main.cpp"
}

func (Cpp) NeedsCompile() bool {
	return true
}

func (Cpp) CompileImage() string {
	return CppCompileImage
}

func (Cpp) CompileCommand() []string {
	command := []string{"g++"}
	command = append(command, strings.Fields(cppCompileFlags)...)
	return append(command, "/workspace/main.cpp", "-o", "/workspace/main")
}

func (Cpp) RunCommand() []string {
	return []string{"/workspace/main"}
}
