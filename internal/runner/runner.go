package runner

import (
	"context"
	"io"
)

type Runner interface {
	Run(ctx context.Context, input string, cmd string, args ...string) (*RunResult, error)
}

// OutputRunner streams binary artifacts without expanding the diagnostic limit.
// Implementations must cancel the command on overflow or writer failure.
type OutputRunner interface {
	RunWithOutput(ctx context.Context, input string, output io.Writer, limit int64, cmd string, args ...string) (*RunResult, error)
}
