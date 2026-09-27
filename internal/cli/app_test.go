package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dev-orchestrator/internal/config"
	"dev-orchestrator/internal/project"
	"dev-orchestrator/internal/runner"
	"dev-orchestrator/internal/safety"
	"dev-orchestrator/internal/ui"
)

type doctorExecutor func(context.Context, runner.Request) (runner.Result, error)

func (f doctorExecutor) Run(ctx context.Context, req runner.Request) (runner.Result, error) {
	return f(ctx, req)
}

func TestDoctorChecksSandboxWithoutProviders(t *testing.T) {
	for _, tt := range []struct {
		name     string
		err      error
		want     string
		wantCode int
	}{
		{name: "available", want: "[ok] filesystem sandbox"},
		{name: "denied", err: errors.New("sandbox denied"), want: "bwrap: setting up uid map: Permission denied", wantCode: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			toolsDir := t.TempDir()
			for _, command := range []string{"git", "task", "bwrap", "ssh"} {
				if err := os.WriteFile(filepath.Join(toolsDir, command), []byte("fake executable"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", toolsDir)
			dir := t.TempDir()
			var out bytes.Buffer
			probed := false
			executor := doctorExecutor(func(_ context.Context, req runner.Request) (runner.Result, error) {
				switch req.Command {
				case "bwrap":
					probed = true
					return runner.Result{Stderr: "bwrap: setting up uid map: Permission denied"}, tt.err
				case "task":
					if strings.Join(req.Args, " ") != "--list-all --json --offline" {
						t.Fatalf("doctor ran validation: %+v", req)
					}
					return runner.Result{Stdout: `{"tasks":[{"name":"all"}]}`}, nil
				default:
					t.Fatalf("doctor launched a provider: %+v", req)
					return runner.Result{}, nil
				}
			})
			cfg := config.Defaults()
			cfg.Decisions.MaxRequests = 0
			cfg.Agents.Claude.Command = "git"
			cfg.Agents.Codex.Command = "git"
			u := ui.New(&out, io.Discard, safety.New(), ui.Options{})
			code := (App{Home: t.TempDir(), Cwd: dir}).doctor(t.Context(), cfg, project.Project{Root: dir}, nil, executor, u)
			if !probed || !strings.Contains(out.String(), tt.want) {
				t.Fatalf("doctor skipped sandbox: code=%d output=%s", code, out.String())
			}
			if code != tt.wantCode {
				t.Fatalf("doctor code=%d, want %d; output=%s", code, tt.wantCode, out.String())
			}
		})
	}
}
