package runner

import "time"

type RunResult struct {
    Stdout   string
    Stderr   string
    ExitCode int
    TimedOut bool
    Err      error
	TimeUsed time.Duration 
	MemoryUsed int64
}