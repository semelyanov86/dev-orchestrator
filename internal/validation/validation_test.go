package validation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"dev-orchestrator/internal/runner"
)

type fakeExec struct {
	req    runner.Request
	stdout string
	err    error
}

func (f *fakeExec) Run(_ context.Context, r runner.Request) (runner.Result, error) {
	f.req = r
	return runner.Result{Stdout: f.stdout}, f.err
}
func TestPreflightOnlyListsTargets(t *testing.T) {
	f := &fakeExec{stdout: `{"tasks":[{"name":"all"}]}`}
	v := Validator{Exec: f, Command: []string{"task", "all"}, Timeout: time.Second}
	if err := v.Preflight(t.Context(), "/synthetic"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.req.Args, " ") != "--list-all --json --offline" {
		t.Fatal("doctor executed validation")
	}
	f.stdout = `{"tasks":[{"name":"test"}]}`
	if err := v.Preflight(t.Context(), "/synthetic"); err == nil {
		t.Fatal("missing all target accepted")
	}
	f.err = errors.New("process failed")
	if r, err := v.Validate(t.Context(), "/synthetic"); err == nil || r.Status != "failed" {
		t.Fatal("validation failure lost")
	}
}
