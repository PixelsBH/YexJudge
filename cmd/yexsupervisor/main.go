package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	contestantUID = 10001
	contestantGID = 10001
	workspacePath = "/workspace"
	maxCases      = 500
)

type supervisorRequest struct {
	Command     []string `json:"command"`
	Inputs      []string `json:"inputs"`
	TimeLimitMs int      `json:"timeLimitMs"`
	OutputLimit int      `json:"outputLimit"`
}

type caseResult struct {
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	ExitCode    int    `json:"exitCode"`
	CPUMs       int64  `json:"cpuMs"`
	MaxRssKb    int64  `json:"maxRssKb"`
	TimedOut    bool   `json:"timedOut"`
	OutputLimit bool   `json:"outputLimit"`
}

type boundedBuffer struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int
	exceeded bool
	onLimit  func()
	once     sync.Once
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	remaining := b.limit - b.buffer.Len()
	if remaining < 0 {
		remaining = 0
	}
	if len(p) > remaining {
		_, _ = b.buffer.Write(p[:remaining])
		b.exceeded = true
		b.mu.Unlock()
		b.once.Do(func() {
			if b.onLimit != nil {
				b.onLimit()
			}
		})
		return len(p), nil
	}
	_, _ = b.buffer.Write(p)
	b.mu.Unlock()
	return len(p), nil
}

func (b *boundedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buffer.String() }
func (b *boundedBuffer) Exceeded() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.exceeded }

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--child" {
		if err := execChild(os.Args[2:]); err != nil {
			fatal(err.Error())
		}
		return
	}
	if len(os.Args) != 2 || os.Args[1] != "run" {
		fatal("expected supervisor command")
	}
	if err := runSupervisor(); err != nil {
		fatal(err.Error())
	}
}

func fatal(message string) { _, _ = fmt.Fprintln(os.Stderr, message); os.Exit(125) }

func runSupervisor() error {
	if os.Geteuid() != 0 {
		return errors.New("supervisor must run as root")
	}
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 16<<20))
	var request supervisorRequest
	if err := decoder.Decode(&request); err != nil {
		return fmt.Errorf("decode supervisor request: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return err
	}
	if len(request.Command) == 0 || request.Command[0] == "" {
		return errors.New("supervisor command is empty")
	}
	if len(request.Inputs) == 0 || len(request.Inputs) > maxCases {
		return fmt.Errorf("supervisor case count must be between 1 and %d", maxCases)
	}
	if request.TimeLimitMs <= 0 || request.TimeLimitMs > 30_000 {
		return errors.New("invalid per-case time limit")
	}
	if request.OutputLimit <= 0 || request.OutputLimit > 1<<20 {
		return errors.New("invalid per-case output limit")
	}
	baseline, err := snapshotWorkspace()
	if err != nil {
		return fmt.Errorf("validate staged workspace: %w", err)
	}
	for _, input := range request.Inputs {
		if err := resetCaseFilesystem(baseline); err != nil {
			return fmt.Errorf("reset before case: %w", err)
		}
		result := runCase(request, input)
		if err := cleanupContestantProcesses(); err != nil {
			return fmt.Errorf("cleanup contestant processes: %w", err)
		}
		if err := resetCaseFilesystem(baseline); err != nil {
			return fmt.Errorf("cleanup case filesystem: %w", err)
		}
		if err := validateWorkspaceBaseline(baseline); err != nil {
			return err
		}
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			return fmt.Errorf("write case result: %w", err)
		}
		if result.ExitCode != 0 || result.TimedOut || result.OutputLimit {
			break
		}
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("supervisor request contains trailing JSON data")
	}
	return nil
}

func runCase(request supervisorRequest, input string) caseResult {
	limit := time.Duration(request.TimeLimitMs) * time.Millisecond
	wallLimit := 2*limit + 500*time.Millisecond
	caseCtx, cancel := context.WithTimeout(context.Background(), wallLimit)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		return caseResult{ExitCode: 125, Stderr: err.Error()}
	}
	args := append([]string{"--child", strconv.Itoa(request.TimeLimitMs)}, request.Command...)
	cmd := exec.CommandContext(caseCtx, executable, args...)
	cmd.Dir = workspacePath
	cmd.Stdin = strings.NewReader(input)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: &syscall.Credential{Uid: contestantUID, Gid: contestantGID, Groups: []uint32{contestantGID}}}
	cmd.WaitDelay = 100 * time.Millisecond
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return killProcessGroup(cmd.Process.Pid)
	}
	stdout := &boundedBuffer{limit: request.OutputLimit}
	stderr := &boundedBuffer{limit: request.OutputLimit}
	stdout.onLimit = func() { _ = killProcessGroup(processID(cmd)) }
	stderr.onLimit = func() { _ = killProcessGroup(processID(cmd)) }
	cmd.Stdout, cmd.Stderr = stdout, stderr
	runErr := cmd.Run()
	_ = killProcessGroup(processID(cmd))
	result := caseResult{Stdout: stdout.String(), Stderr: stderr.String(), OutputLimit: stdout.Exceeded() || stderr.Exceeded()}
	var cpuTime time.Duration
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
		if usage, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
			cpuTime = timevalDuration(usage.Utime) + timevalDuration(usage.Stime)
			result.CPUMs = cpuTime.Milliseconds()
			if usage.Maxrss > 0 {
				result.MaxRssKb = usage.Maxrss
			}
		}
	} else {
		result.ExitCode = 125
		if runErr != nil {
			result.Stderr += runErr.Error()
		}
	}
	result.TimedOut = caseCtx.Err() == context.DeadlineExceeded || (cpuTime > 0 && cpuTime >= limit)
	if result.TimedOut {
		result.ExitCode = 124
	}
	return result
}

func timevalDuration(value syscall.Timeval) time.Duration {
	return time.Duration(value.Sec)*time.Second + time.Duration(value.Usec)*time.Microsecond
}
func processID(cmd *exec.Cmd) int {
	if cmd.Process == nil {
		return 0
	}
	return cmd.Process.Pid
}
func killProcessGroup(pid int) error {
	if pid <= 0 {
		return nil
	}
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func execChild(args []string) error {
	if len(args) < 2 || os.Geteuid() != contestantUID || os.Getegid() != contestantGID {
		return errors.New("invalid child execution context")
	}
	limitMs, err := strconv.Atoi(args[0])
	if err != nil || limitMs <= 0 {
		return errors.New("invalid child time limit")
	}
	if err := applyChildLimits(limitMs); err != nil {
		return err
	}
	if err := installContestantSeccomp(); err != nil {
		return fmt.Errorf("install syscall restrictions: %w", err)
	}
	command := args[1:]
	resolvedCommand, err := exec.LookPath(command[0])
	if err != nil {
		return fmt.Errorf("resolve contestant command: %w", err)
	}
	command[0] = resolvedCommand
	return syscall.Exec(command[0], command, []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/tmp", "TMPDIR=/tmp"})
}

func applyChildLimits(timeLimitMs int) error {
	cpuSeconds := uint64((timeLimitMs + 999) / 1000)
	limits := []struct {
		resource int
		soft     uint64
		hard     uint64
	}{
		{syscall.RLIMIT_CORE, 0, 0}, {syscall.RLIMIT_FSIZE, 16 << 20, 16 << 20}, {6, 64, 64}, {syscall.RLIMIT_NOFILE, 256, 256}, {syscall.RLIMIT_CPU, cpuSeconds, cpuSeconds + 1},
	}
	for _, limit := range limits {
		r := &syscall.Rlimit{Cur: limit.soft, Max: limit.hard}
		if err := syscall.Setrlimit(limit.resource, r); err != nil {
			return fmt.Errorf("set rlimit %d: %w", limit.resource, err)
		}
	}
	return nil
}

type workspaceEntry struct {
	mode      os.FileMode
	directory bool
}

func snapshotWorkspace() (map[string]workspaceEntry, error) {
	baseline := make(map[string]workspaceEntry)
	err := filepath.WalkDir(workspacePath, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == workspacePath {
			return nil
		}
		rel, err := filepath.Rel(workspacePath, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported staged workspace entry %q", rel)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return fmt.Errorf("staged workspace entry %q is not root-owned", rel)
		}
		if info.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("staged workspace entry %q is writable by contestant", rel)
		}
		baseline[rel] = workspaceEntry{mode: info.Mode(), directory: info.IsDir()}
		return nil
	})
	return baseline, err
}

func validateWorkspaceBaseline(baseline map[string]workspaceEntry) error {
	for rel, expected := range baseline {
		path := filepath.Join(workspacePath, rel)
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("protected staged path %q disappeared: %w", rel, err)
		}
		if expected.directory != info.IsDir() || (!expected.directory && !info.Mode().IsRegular()) {
			return fmt.Errorf("protected staged path %q changed type", rel)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return fmt.Errorf("protected staged path %q changed owner", rel)
		}
		if info.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("protected staged path %q became writable", rel)
		}
	}
	return nil
}

func resetCaseFilesystem(baseline map[string]workspaceEntry) error {
	if err := clearWorkspace(workspacePath, "", baseline); err != nil {
		return fmt.Errorf("clear workspace: %w", err)
	}
	for _, path := range []string{"/tmp", "/dev/shm"} {
		if err := clearDirectory(path); err != nil {
			return fmt.Errorf("clear %s: %w", path, err)
		}
	}
	return nil
}

func clearWorkspace(path, rel string, baseline map[string]workspaceEntry) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		childRel := entry.Name()
		if rel != "" {
			childRel = filepath.Join(rel, entry.Name())
		}
		if _, protected := baseline[childRel]; !protected {
			if err := os.RemoveAll(filepath.Join(path, entry.Name())); err != nil {
				return err
			}
			continue
		}
		if entry.IsDir() {
			if err := clearWorkspace(filepath.Join(path, entry.Name()), childRel, baseline); err != nil {
				return err
			}
		} else if !entry.Type().IsRegular() {
			return fmt.Errorf("protected path %q changed type", childRel)
		}
	}
	return nil
}

func clearDirectory(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(path, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func cleanupContestantProcesses() error {
	deadline := time.Now().Add(750 * time.Millisecond)
	for {
		pids, err := contestantPIDs()
		if err != nil {
			return err
		}
		if len(pids) == 0 {
			return nil
		}
		for _, pid := range pids {
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return fmt.Errorf("kill contestant process %d: %w", pid, err)
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("contestant processes remain after cleanup: %v", pids)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func contestantPIDs() ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		status, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "status"))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
				continue
			}
			return nil, err
		}
		for _, line := range strings.Split(string(status), "\n") {
			if !strings.HasPrefix(line, "Uid:") {
				continue
			}
			fields := strings.Fields(strings.TrimPrefix(line, "Uid:"))
			if len(fields) < 2 {
				return nil, fmt.Errorf("invalid UID for pid %d", pid)
			}
			uid, err := strconv.Atoi(fields[1])
			if err != nil {
				return nil, err
			}
			if uid == contestantUID {
				pids = append(pids, pid)
			}
			break
		}
	}
	return pids, nil
}

func installContestantSeccomp() error {
	setnsNumber, clone3Number, err := seccompSyscallNumbers()
	if err != nil {
		return err
	}
	const (
		bpfLDWABS    = 0x20
		bpfJMPEQK    = 0x15
		bpfJMPJSETK  = 0x45
		bpfRETK      = 0x06
		seccompErrno = 0x00050000
		seccompAllow = 0x7fff0000
		prSetNoNew   = 38
		prSetSeccomp = 22
		seccompMode  = 2
		namespaceOps = 0x7e020080
		x32Syscall   = 0x40000000
	)
	architecture, err := seccompAuditArchitecture()
	if err != nil {
		return err
	}
	filter := make([]syscall.SockFilter, 0, 44)
	filter = append(filter, syscall.SockFilter{Code: bpfLDWABS, K: 4}, syscall.SockFilter{Code: bpfJMPEQK, Jt: 1, K: architecture}, syscall.SockFilter{Code: bpfRETK, K: seccompErrno | uint32(syscall.EPERM)}, syscall.SockFilter{Code: bpfLDWABS, K: 0})
	if runtime.GOARCH == "amd64" {
		filter = append(filter, syscall.SockFilter{Code: bpfJMPJSETK, Jf: 1, K: x32Syscall}, syscall.SockFilter{Code: bpfRETK, K: seccompErrno | uint32(syscall.EPERM)})
	}
	deny := func(number uintptr) {
		filter = append(filter, syscall.SockFilter{Code: bpfJMPEQK, Jf: 1, K: uint32(number)}, syscall.SockFilter{Code: bpfRETK, K: seccompErrno | uint32(syscall.EPERM)})
	}
	for _, number := range []uintptr{syscall.SYS_SETPGID, syscall.SYS_SETSID, syscall.SYS_UNSHARE, setnsNumber, syscall.SYS_SHMGET, syscall.SYS_SHMAT, syscall.SYS_SHMCTL, syscall.SYS_SHMDT, syscall.SYS_SEMGET, syscall.SYS_SEMOP, syscall.SYS_SEMCTL, syscall.SYS_MSGGET, syscall.SYS_MSGSND, syscall.SYS_MSGRCV, syscall.SYS_MSGCTL} {
		deny(number)
	}
	filter = append(filter, syscall.SockFilter{Code: bpfJMPEQK, Jf: 1, K: uint32(clone3Number)}, syscall.SockFilter{Code: bpfRETK, K: seccompErrno | uint32(syscall.ENOSYS)}, syscall.SockFilter{Code: bpfJMPEQK, Jf: 3, K: uint32(syscall.SYS_CLONE)}, syscall.SockFilter{Code: bpfLDWABS, K: 16}, syscall.SockFilter{Code: bpfJMPJSETK, Jf: 1, K: namespaceOps}, syscall.SockFilter{Code: bpfRETK, K: seccompErrno | uint32(syscall.EPERM)}, syscall.SockFilter{Code: bpfRETK, K: seccompAllow})
	program := syscall.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetNoNew, 1, 0, 0, 0, 0); errno != 0 {
		return errno
	}
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetSeccomp, seccompMode, uintptr(unsafe.Pointer(&program)), 0, 0, 0); errno != 0 {
		return errno
	}
	runtime.KeepAlive(filter)
	runtime.KeepAlive(&program)
	return nil
}

func seccompSyscallNumbers() (uintptr, uintptr, error) {
	switch runtime.GOARCH {
	case "amd64":
		return 308, 435, nil
	case "arm64":
		return 268, 435, nil
	default:
		return 0, 0, fmt.Errorf("unsupported architecture %q", runtime.GOARCH)
	}
}
func seccompAuditArchitecture() (uint32, error) {
	switch runtime.GOARCH {
	case "amd64":
		return 0xc000003e, nil
	case "arm64":
		return 0xc00000b7, nil
	default:
		return 0, fmt.Errorf("unsupported architecture %q", runtime.GOARCH)
	}
}
