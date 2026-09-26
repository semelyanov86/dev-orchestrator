package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dev-orchestrator/internal/agent"
)

func TestPingMinimalCallsAndArtifacts(t *testing.T) {
	f := newFixture(t, "ping")
	f.input.Task = "Do not send this user text or source to providers"
	f.input.Project.Instructions = strings.Repeat("large project instructions", 1000)
	f.engine.Decisions = nil
	f.engine.Config.Decisions.MaxRequests = 0
	run, err := f.engine.Run(t.Context(), f.input)
	if err != nil || run.Outcome != "success" || run.Final != "codex: ping\nclaude: pong" {
		t.Fatalf("ping outcome=%s final=%q err=%v", run.Outcome, run.Final, err)
	}
	if len(run.Steps) != 2 || len(f.codex.requests) != 1 || len(f.claude.requests) != 1 || f.validator.calls != 0 || f.remote.calls != 0 || len(run.Decisions) != 0 {
		t.Fatalf("unexpected extra work: steps=%d decisions=%v", len(run.Steps), run.Decisions)
	}
	for _, req := range []agent.Request{f.codex.requests[0], f.claude.requests[0]} {
		if len(req.Prompt) > 100 || strings.Contains(req.Prompt, "user text") || strings.Contains(req.Prompt, "project instructions") || req.Mode != agent.ReadOnly || req.Timeout > 30*time.Second {
			t.Fatalf("probe is not minimal/read-only: %+v", req)
		}
		if _, err := os.Stat(filepath.Join(run.Artifacts, "steps", req.StepID, "record.json")); err != nil {
			t.Fatal(err)
		}
	}
	if run.Current.ID != run.Initial.ID || run.Validation.Status != "not_required" || len(run.Criteria) != 2 {
		t.Fatalf("incorrect completion facts: snapshot=%s validation=%s criteria=%d", run.Current.ID, run.Validation.Status, len(run.Criteria))
	}
}

func TestPingFailurePaths(t *testing.T) {
	for _, name := range []string{"process failure", "unexpected reply", "read-only drift", "step budget", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "ping")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if name == "step budget" {
				f.engine.Config.Workflow.MaxSteps = 1
			}
			f.codex.run = func(ctx context.Context, req agent.Request) (agent.Result, error) {
				result := agent.Result{Report: goodReport(req)}
				switch name {
				case "process failure":
					result.ExitCode = 1
					return result, errors.New("provider unavailable")
				case "unexpected reply":
					result.Report.Markdown = "not ping"
				case "read-only drift":
					f.inspector.change()
				case "cancelled":
					cancel()
					return result, ctx.Err()
				}
				return result, nil
			}
			run, err := f.engine.Run(ctx, f.input)
			if err == nil || run.Outcome == "success" || run.ExitCode == 0 || len(f.codex.requests) != 1 {
				t.Fatalf("failure swallowed/retried: outcome=%s code=%d err=%v", run.Outcome, run.ExitCode, err)
			}
			if name == "cancelled" && (run.Outcome != "cancelled" || len(f.claude.requests) != 0) {
				t.Fatalf("cancellation did not stop: outcome=%s Claude calls=%d", run.Outcome, len(f.claude.requests))
			}
			if name == "process failure" && len(f.claude.requests) != 1 {
				t.Fatal("second provider connection was not checked")
			}
		})
	}
}
