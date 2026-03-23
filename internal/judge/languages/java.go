package languages

type Java struct{}

func (Java) Name() string {
	return "java"
}

func (Java) SourceFileName() string {
	return "Main.java"
}

func (Java) NeedsCompile() bool {
	return true
}

func (Java) CompileImage() string {
	return "eclipse-temurin:21-alpine"
}

func (Java) CompileCommand() []string {
	return []string{
		"javac",
		"/workspace/Main.java",
	}
}

func (Java) RuntimeImage() string {
	return "eclipse-temurin:21-alpine"
}

func (Java) RunCommand() []string {
	return []string{
		"java",
		"-cp",
		"/workspace",
		"Main",
	}
}
