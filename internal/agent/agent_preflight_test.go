package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"dev-orchestrator/internal/runner"
	"dev-orchestrator/internal/safety"
)

type preflightExecutor func(context.Context, runner.Request) (runner.Result, error)

func (f preflightExecutor) Run(ctx context.Context, req runner.Request) (runner.Result, error) {
	return f(ctx, req)
}

func TestCheckSandbox(t *testing.T) {
	denied := errors.New("process bwrap exit 1")
	for _, tt := range []struct {
		name   string
		stderr string
		err    error
		want   string
	}{
		{name: "available"},
		{name: "uid map denied", stderr: "bwrap: setting up uid map: Permission denied\n", err: denied, want: "setting up uid map: Permission denied"},
		{name: "missing executable", err: denied, want: "check bubblewrap installation"},
		{name: "redacted output", stderr: "\x1b[31mbwrap: denied local-test-credential\x1b[0m", err: denied, want: "bwrap: denied [REDACTED]"},
		{name: "bounded output", stderr: strings.Repeat("x", 3000), err: denied, want: "[truncated]"},
		{name: "cancelled", err: context.Canceled, want: "context canceled"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			executor := preflightExecutor(func(_ context.Context, req runner.Request) (runner.Result, error) {
				if req.Command != "bwrap" || req.Dir != dir || req.Timeout <= 0 || req.Input != "" {
					t.Fatalf("unexpected sandbox probe: %+v", req)
				}
				args := strings.Join(req.Args, " ")
				if !strings.Contains(args, "--ro-bind / /") || strings.Contains(args, "--bind ") || req.Args[len(req.Args)-1] != "/bin/true" {
					t.Fatalf("probe permits writes or launches provider: %v", req.Args)
				}
				return runner.Result{Stderr: tt.stderr}, tt.err
			})
			err := CheckSandbox(t.Context(), executor, dir, safety.New("local-test-credential"))
			if tt.err == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, tt.err) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("sandbox error %v, want cause %v and detail %q", err, tt.err, tt.want)
			}
			if strings.ContainsAny(err.Error(), "\x1b\r") || strings.Contains(err.Error(), "local-test-credential") || len(err.Error()) > 2400 {
				t.Fatalf("unsafe or unbounded diagnostic: %q", err)
			}
		})
	}
}

func TestCLIPreflightReportsSandboxFailure(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			denied := errors.New("sandbox denied")
			executor := preflightExecutor(func(_ context.Context, req runner.Request) (runner.Result, error) {
				if req.Command == "bwrap" {
					return runner.Result{Stderr: "bwrap: setting up uid map: Permission denied"}, denied
				}
				if req.Command != provider || req.Args[len(req.Args)-1] != "--help" {
					t.Fatalf("preflight launched a provider request: %+v", req)
				}
				return runner.Result{Stdout: "--restricted --json-schema --strict-mcp-config --output-schema --ignore-user-config --sandbox"}, nil
			})
			cli := CLI{Provider: provider, Command: provider, Exec: executor, Cleaner: safety.New()}
			err := cli.Preflight(t.Context(), t.TempDir())
			if !errors.Is(err, denied) || !strings.Contains(err.Error(), "setting up uid map: Permission denied") {
				t.Fatalf("preflight dropped sandbox diagnostic: %v", err)
			}
		})
	}
}
