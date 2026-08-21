package runner

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type limitedBuffer struct {
	buffer     bytes.Buffer
	limit      int
	exceeded   bool
	onExceeded func()
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit <= b.buffer.Len() {
		if !b.exceeded {
			b.exceeded = true
			if b.onExceeded != nil {
				b.onExceeded()
			}
		}
		return len(p), nil
	}

	remaining := b.limit - b.buffer.Len()
	if len(p) > remaining {
		_, _ = b.buffer.Write(p[:remaining])
		if !b.exceeded {
			b.exceeded = true
			if b.onExceeded != nil {
				b.onExceeded()
			}
		}
		return len(p), nil
	}

	return b.buffer.Write(p)
}

func (b *limitedBuffer) String() string {
	return b.buffer.String()
}

type DockerRunner struct{}

func (d *DockerRunner) Run(ctx context.Context, input string, cmd string, args ...string) (*RunResult, error) {
	start := time.Now()
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()

	command := exec.CommandContext(runContext, cmd, args...)

	command.Stdin = strings.NewReader(input)

	command.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	// Docker commands can create a child process group. Killing only the
	// client on cancellation can leave that group alive, especially when a
	// docker exec process is running in a reusable container.
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	command.WaitDelay = 2 * time.Second

	var stdoutBuf, stderrBuf limitedBuffer
	stdoutBuf.limit = DefaultOutputLimitBytes
	stderrBuf.limit = DefaultOutputLimitBytes
	stdoutBuf.onExceeded = cancel
	stderrBuf.onExceeded = cancel
	command.Stdout = &stdoutBuf
	command.Stderr = &stderrBuf

	if err := command.Start(); err != nil {
		return nil, err
	}

	err := command.Wait()

	result := &RunResult{
		Stdout:              stdoutBuf.String(),
		Stderr:              stderrBuf.String(),
		OutputLimitExceeded: stdoutBuf.exceeded || stderrBuf.exceeded,
		TimeUsed:            time.Since(start),
	}

	if ctx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		}
	} else {
		result.ExitCode = 0
	}
	return result, nil
}
