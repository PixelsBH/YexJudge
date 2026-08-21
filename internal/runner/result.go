package runner

import "time"

const DefaultOutputLimitBytes = 64 * 1024

type RunResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	TimedOut bool

	OutputLimitExceeded bool
	TimeUsed            time.Duration
	MemoryUsed          int64
}
