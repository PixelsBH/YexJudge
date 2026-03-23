package languages

type Go struct{}

func (Go) Name() string {
	return "go"
}

func (Go) SourceFileName() string {
	return "main.go"
}

func (Go) NeedsCompile() bool {
	return true
}

func (Go) CompileImage() string {
	return "golang:1.24-alpine"
}

func (Go) CompileCommand() []string {
	return []string{
		"go", "build",
		"-o", "/workspace/main",
		"/workspace/main.go",
	}
}

func (Go) RuntimeImage() string {
	return "alpine"
}

func (Go) RunCommand() []string {
	return []string{"/workspace/main"}
}
