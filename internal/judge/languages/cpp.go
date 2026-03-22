package languages

type Cpp struct{}

func (Cpp) Name() string {
	return "cpp"
}

func (Cpp) SourceFileName() string {
	return "main.cpp"
}

func (Cpp) CompileImage() string {
	return "gcc:13"
}

func (Cpp) CompileCommand() []string {
	return []string{
		"g++",
		"-O2",
		"-pipe",
		"-static",
		"-s",
		"/workspace/main.cpp",
		"-o",
		"/workspace/main",
	}
}

func (Cpp) RunCommand() []string {
	return []string{"/workspace/main"}
}
