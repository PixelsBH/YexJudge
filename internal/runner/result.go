package runner

import "time"

type RunResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	TimedOut bool

	TimeUsed   time.Duration
	MemoryUsed int64
}
