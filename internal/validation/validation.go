// Package validation knows only the working project's configured argv contract.
package validation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"dev-orchestrator/internal/runner"
)

type Executor interface {
	Run(context.Context, runner.Request) (runner.Result, error)
}
type Result struct {
	Status   string `json:"status"`
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
	Snapshot string `json:"snapshot"`
	Reason   string `json:"reason"`
}
type Validator struct {
	Exec    Executor
	Command []string
	Timeout time.Duration
}

func (v Validator) Validate(ctx context.Context, dir string) (Result, error) {
	r, err := v.Exec.Run(ctx, runner.Request{Command: v.Command[0], Args: v.Command[1:], Dir: dir, Timeout: v.Timeout})
	status := "passed"
	if err != nil {
		status = "failed"
	}
	return Result{Status: status, ExitCode: r.ExitCode, Output: r.Stdout + "\n" + r.Stderr}, err
}

// Preflight lists targets offline; it never evaluates task all to discover it.
func (v Validator) Preflight(ctx context.Context, dir string) error {
	if len(v.Command) == 0 {
		return errors.New("validation command is empty")
	}
	if len(v.Command) != 2 || v.Command[0] != "task" || v.Command[1] != "all" {
		return nil
	}
	r, err := v.Exec.Run(ctx, runner.Request{Command: "task", Args: []string{"--list-all", "--json", "--offline"}, Dir: dir, Timeout: 10 * time.Second})
	if err != nil {
		return fmt.Errorf("list task targets: %w", err)
	}
	var listing struct {
		Tasks []struct {
			Name string `json:"name"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(r.Stdout), &listing); err != nil {
		return fmt.Errorf("decode task listing: %w", err)
	}
	for _, task := range listing.Tasks {
		if strings.TrimSpace(task.Name) == "all" {
			return nil
		}
	}
	return errors.New("working project has no task all target")
}
