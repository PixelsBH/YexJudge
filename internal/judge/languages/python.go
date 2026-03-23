package languages

type Python struct{}

func (Python) Name() string {
	return "python"
}

func (Python) SourceFileName() string {
	return "main.py"
}

func (Python) NeedsCompile() bool {
	return false
}

func (Python) CompileImage() string {
	return ""
}

func (Python) CompileCommand() []string {
	return nil
}

func (Python) RuntimeImage() string {
	return "python:3.12-alpine"
}

func (Python) RunCommand() []string {
	return []string{"python3", "/workspace/main.py"}
}
