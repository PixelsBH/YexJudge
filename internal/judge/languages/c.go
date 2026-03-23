package languages

type C struct{}

func (C) Name() string {
	return "c"
}

func (C) SourceFileName() string {
	return "main.c"
}

func (C) NeedsCompile() bool {
	return true
}

func (C) CompileImage() string {
	return "gcc:13"
}

func (C) CompileCommand() []string {
	return []string{
		"gcc",
		"-O2",
		"-pipe",
		"-static",
		"-s",
		"/workspace/main.c",
		"-o",
		"/workspace/main",
	}
}

func (C) RuntimeImage() string {
	return "alpine"
}

func (C) RunCommand() []string {
	return []string{"/workspace/main"}
}
