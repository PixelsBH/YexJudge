package runner

import "context"

type Runner interface {
	Run(ctx context.Context, input string, cmd string, args ...string) (*RunResult, error)
}
