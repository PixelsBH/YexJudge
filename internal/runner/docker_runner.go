package runner

import (
	"bytes"
	"context"
	"fmt"
	"io"
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
	return d.run(ctx, input, nil, DefaultOutputLimitBytes, cmd, args...)
}

func (d *DockerRunner) RunWithOutput(ctx context.Context, input string, output io.Writer, limit int64, cmd string, args ...string) (*RunResult, error) {
	if output == nil || limit <= 0 {
		return nil, fmt.Errorf("invalid bounded output destination")
	}
	return d.run(ctx, input, output, limit, cmd, args...)
}

type boundedWriter struct {
	output    io.Writer
	remaining int64
	exceeded  bool
	failed    bool
	cancel    context.CancelFunc
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	original := len(p)
	if int64(len(p)) > w.remaining {
		p = p[:w.remaining]
		w.exceeded = true
		w.cancel()
	}
	if len(p) > 0 {
		n, err := w.output.Write(p)
		w.remaining -= int64(n)
		if err != nil || n != len(p) {
			w.failed = true
			w.cancel()
			return n, fmt.Errorf("write bounded command output failed")
		}
	}
	return original, nil
}

func (d *DockerRunner) run(ctx context.Context, input string, output io.Writer, limit int64, cmd string, args ...string) (*RunResult, error) {
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
	stream := &boundedWriter{output: output, remaining: limit, cancel: cancel}
	if output != nil {
		command.Stdout = stream
	}
	command.Stderr = &stderrBuf

	if err := command.Start(); err != nil {
		if ctx.Err() != nil {
			return &RunResult{ExitCode: -1, TimedOut: ctx.Err() == context.DeadlineExceeded, TimeUsed: time.Since(start)}, nil
		}
		return nil, fmt.Errorf("start command failed")
	}
	// Also reap local CLI descendants on normal exit or WaitDelay failure.
	defer syscall.Kill(-command.Process.Pid, syscall.SIGKILL)

	err := command.Wait()
	if stream.failed {
		return nil, fmt.Errorf("write command output failed")
	}
	if err != nil {
		_, isExitError := err.(*exec.ExitError)
		if !isExitError && !stdoutBuf.exceeded && !stderrBuf.exceeded && !stream.exceeded && ctx.Err() == nil {
			return nil, fmt.Errorf("wait for command failed")
		}
	}

	result := &RunResult{
		Stdout:              stdoutBuf.String(),
		Stderr:              stderrBuf.String(),
		OutputLimitExceeded: stdoutBuf.exceeded || stderrBuf.exceeded || stream.exceeded,
		ExitCode:            -1,
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
