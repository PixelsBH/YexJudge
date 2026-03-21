package languages

type Spec interface {
	Name() string
	SourceFileName() string
	NeedsCompile() bool
	CompileCommand() []string
	RunCommand() []string
}
