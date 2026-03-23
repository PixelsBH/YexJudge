package languages

type Spec interface {
	Name() string
	SourceFileName() string
	NeedsCompile() bool
	CompileImage() string
	CompileCommand() []string
	RuntimeImage() string
	RunCommand() []string
}
