package runner

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type DockerRunner struct{}

func (d *DockerRunner) Run(ctx context.Context, input string, cmd string, args ...string) (*RunResult, error) {
	start := time.Now()

	command := exec.CommandContext(ctx, cmd, args...)

	command.Stdin = strings.NewReader(input)

	command.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	stdoutPipe, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}

	stderrPipe, err := command.StderrPipe()
	if err != nil {
		return nil, err
	}

	if err := command.Start(); err != nil {
		return nil, err
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	var wg sync.WaitGroup

	wg.Add(2)

	go func() {
		defer wg.Done()
		io.Copy(&stdoutBuf, stdoutPipe)
	}()

	go func() {
		defer wg.Done()
		io.Copy(&stderrBuf, stderrPipe)
	}()

	err = command.Wait()

	wg.Wait()

	result := &RunResult{
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
		TimeUsed: time.Since(start),
	}

	if ctx.Err() == context.DeadlineExceeded {
		result.TimedOut = true

		if command.Process != nil {
			syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		}
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
