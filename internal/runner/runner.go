package runner

import "context"

type Runner interface {
    Run(ctx context.Context, cmd string, args ...string) (*RunResult, error)
}