// Package remote owns fixed SSH diagnostics; model text never becomes a command.
package remote

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dev-orchestrator/internal/config"
	"dev-orchestrator/internal/runner"
	"dev-orchestrator/internal/safety"
)

type Executor interface {
	Run(context.Context, runner.Request) (runner.Result, error)
}
type RemoteExecutor struct {
	Exec    Executor
	Cleaner safety.Cleaner
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func Validate(s config.Server) error {
	host := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]*$`)
	user := regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]*$`)
	if !host.MatchString(s.Host) || (s.User != "" && !user.MatchString(s.User)) || s.Port < 1 || s.Port > 65535 || !strings.HasPrefix(s.ProjectPath, "/") || strings.ContainsAny(s.ProjectPath, "\x00\r\n") {
		return errors.New("invalid ssh profile")
	}
	return nil
}
func (r RemoteExecutor) Investigate(ctx context.Context, s config.Server) (string, error) {
	if err := Validate(s); err != nil {
		return "", err
	}
	host := s.Host
	if s.User != "" {
		host = s.User + "@" + host
	}
	// Deliberately omit environment values, customer logs, and all operational writes.
	command := "uptime; ps -eo pid,ppid,comm,pcpu,pmem --sort=-pcpu | head -25; ss -lnt; systemctl --failed --no-pager; git -C " + quote(s.ProjectPath) + " rev-parse HEAD; git -C " + quote(s.ProjectPath) + " status --short"
	res, err := r.Exec.Run(ctx, runner.Request{Command: "ssh", Args: []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-p", strconv.Itoa(s.Port), host, command}, Timeout: 30 * time.Second})
	output := r.Cleaner.Clean(res.Stdout + "\n" + res.Stderr)
	if err != nil {
		return output, fmt.Errorf("remote read-only diagnostics: %w", err)
	}
	return output, nil
}
