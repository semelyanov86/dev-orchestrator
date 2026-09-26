// Package runner executes argv without a shell and owns the whole process group.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Request holds executable, separate arguments, stdin and an explicit deadline.
type Request struct {
	Command string
	Args    []string
	Dir     string
	Input   string
	Timeout time.Duration
	Env     []string
}
type Result struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Duration  time.Duration
	Truncated bool
}

// Executor is implemented by Runner and test fakes at its consumers.
type Runner struct {
	MaxOutput int
	Grace     time.Duration
}

func New() *Runner { return &Runner{MaxOutput: 4 << 20, Grace: 500 * time.Millisecond} }

type limitedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	room := b.limit - b.buf.Len()
	if room < len(p) {
		b.truncated = true
		p = p[:max(0, room)]
	}
	_, err := b.buf.Write(p)
	return n, err
}
func (b *limitedBuffer) text() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String(), b.truncated
}

// Environment excludes the classifier credential even when supplied via DEV overrides.
func Environment(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if k != "OPENROUTER_API_KEY" && k != "DEV_OPENROUTER_API_KEY" {
			out = append(out, e)
		}
	}
	return out
}

// Run drains bounded output and cancels descendants, including those inheriting pipes.
func (r *Runner) Run(ctx context.Context, req Request) (Result, error) {
	var result Result
	result.ExitCode = -1
	if req.Command == "" || req.Timeout <= 0 {
		return result, errors.New("runner requires command and positive timeout")
	}
	ctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, req.Command, req.Args...)
	cmd.Dir = req.Dir
	cmd.Stdin = strings.NewReader(req.Input)
	env := req.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = Environment(env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// CommandContext's default Cancel only kills the direct child; the owner below replaces it.
	cmd.Cancel = func() error { return nil }
	cmd.WaitDelay = r.Grace + time.Second
	out := &limitedBuffer{limit: r.MaxOutput}
	stderr := &limitedBuffer{limit: r.MaxOutput}
	cmd.Stdout = out
	cmd.Stderr = stderr
	started := time.Now()
	if err := cmd.Start(); err != nil {
		return result, fmt.Errorf("start %s: %w", req.Command, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		signalGroup(cmd.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(r.Grace)
		select {
		case err = <-done:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			signalGroup(cmd.Process.Pid, syscall.SIGKILL)
			err = <-done
		}
	}
	// Cleanup detached grandchildren in the same group even after a successful parent exit.
	signalGroup(cmd.Process.Pid, syscall.SIGKILL)
	result.Duration = time.Since(started)
	result.ExitCode = cmd.ProcessState.ExitCode()
	var a, b bool
	result.Stdout, a = out.text()
	result.Stderr, b = stderr.text()
	result.Truncated = a || b
	if ctx.Err() != nil {
		return result, fmt.Errorf("process %s interrupted: %w", req.Command, ctx.Err())
	}
	if err != nil {
		return result, fmt.Errorf("process %s exit %d: %w", req.Command, result.ExitCode, err)
	}
	return result, nil
}

func signalGroup(pid int, signal syscall.Signal) {
	// ESRCH means the group has already completed; other errors cannot be recovered here.
	if err := syscall.Kill(-pid, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		if p, e := os.FindProcess(pid); e == nil {
			_ = p.Signal(signal)
		}
	}
}

// ReadBounded reads one local input document without unbounded allocation.
func ReadBounded(r io.Reader, limit int) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return "", err
	}
	if len(b) > limit {
		return "", errors.New("input exceeds size limit")
	}
	return string(b), nil
}
