package languages

type Spec interface {
	Name() string
	SourceFileName() string
	CompileImage() string
	CompileCommand() []string
	RunCommand() []string
}
