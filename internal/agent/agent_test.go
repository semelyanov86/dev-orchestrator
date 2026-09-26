package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dev-orchestrator/internal/report"
	"dev-orchestrator/internal/runner"
	"dev-orchestrator/internal/safety"
)

type fakeExecutor struct {
	req    runner.Request
	output runner.Result
}

func (f *fakeExecutor) Run(_ context.Context, req runner.Request) (runner.Result, error) {
	f.req = req
	return f.output, nil
}
func fixtureReport() report.StepReport {
	return report.StepReport{SchemaVersion: 1, StepID: "01", Stage: "review", Outcome: "completed", Summary: "done", Markdown: "done", Requirements: []report.Requirement{{ID: "task", Description: "task", Status: "satisfied", Evidence: []report.Evidence{{Kind: "observation", Reference: "stage", Detail: "checked"}}}}, Findings: []report.Finding{}, MissingEvidence: []string{}, Disagreements: []string{}, UnresolvedQuestions: []string{}, ScopeChanges: []string{}, ArtifactReferences: []string{}, Diagnosis: "not_applicable", FailureClass: "not_applicable", Verdict: "pass"}
}

func TestDocumentationScopeCanCreateOnlyAuthorizedFile(t *testing.T) {
	dir := t.TempDir()
	c := CLI{Provider: "claude", Home: t.TempDir()}
	if _, err := c.sandbox(Request{ProjectDir: dir, Mode: DocsOnly, DocsPaths: []string{"README.md"}}, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "README.md")); err != nil || len(b) != 0 {
		t.Fatalf("new documentation scope: %v", err)
	}
	if _, err := c.sandbox(Request{ProjectDir: dir, Mode: DocsOnly, DocsPaths: []string{"docs/.."}}, t.TempDir()); err == nil {
		t.Fatal("root writable via normalized scope")
	}
	args, err := c.arguments(Request{ProjectDir: dir, Mode: DocsOnly, DocsPaths: []string{"documentation"}})
	if err != nil || !strings.Contains(strings.Join(args, " "), "Edit(/documentation/**)") {
		t.Fatalf("new directory permissions: %v %v", args, err)
	}
}
func TestCLIArgsSandboxAndInput(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			dir := t.TempDir()
			home := t.TempDir()
			for _, path := range []string{filepath.Join(dir, ".git"), filepath.Join(dir, ".codex"), filepath.Join(home, ".claude"), filepath.Join(home, ".codex"), filepath.Join(home, ".ssh")} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, ".codex", "config.toml"), []byte("[mcp_servers.untrusted]\ncommand='bad'\n"), 0600); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(fixtureReport())
			if err != nil {
				t.Fatal(err)
			}
			output := `{"is_error":false,"structured_output":` + string(data) + `}`
			if provider == "codex" {
				event := map[string]any{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": string(data)}}
				b, err := json.Marshal(event)
				if err != nil {
					t.Fatal(err)
				}
				output = string(b) + "\n"
			}
			executor := &fakeExecutor{output: runner.Result{Stdout: output, ExitCode: 0}}
			c := CLI{Provider: provider, Command: provider, Exec: executor, Home: home, StateRoot: t.TempDir(), Cleaner: safety.New()}
			r, err := c.Run(t.Context(), Request{ProjectDir: dir, StepID: "01", Stage: "review", Prompt: "task with $(do-not-execute)", Mode: ReadOnly, Timeout: time.Second})
			if err != nil || r.ReportError != "" {
				t.Fatalf("CLI report err %v %s", err, r.ReportError)
			}
			argv := strings.Join(executor.req.Args, " ")
			if executor.req.Command != "bwrap" || strings.Contains(argv, "do-not-execute") || executor.req.Input != "task with $(do-not-execute)" {
				t.Fatal("prompt escaped stdin boundary")
			}
			if !strings.Contains(argv, "--tmpfs "+filepath.Join(home, ".ssh")) || !strings.Contains(argv, "--ro-bind /dev/null "+filepath.Join(dir, ".codex", "config.toml")) {
				t.Fatal("credentials/project mcp config exposed")
			}
			if provider == "claude" && !strings.Contains(argv, "--tools Read,Glob,Grep --permission-mode plan") {
				t.Fatal("read-only tool limit missing")
			}
			if provider == "codex" && !strings.Contains(argv, "--sandbox read-only") {
				t.Fatal("native sandbox missing")
			}
		})
	}
}
func TestProviderEnvelopeFailures(t *testing.T) {
	for _, tt := range []struct{ name, provider, data string }{{name: "claude prose", provider: "claude", data: `{"result":"LGTM"}`}, {name: "claude error", provider: "claude", data: `{"is_error":true,"structured_output":{}}`}, {name: "codex failed", provider: "codex", data: `{"type":"turn.failed"}`}, {name: "codex empty", provider: "codex", data: `{"type":"turn.completed"}`}} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := decodeOutput(tt.provider, tt.data); err == nil {
				t.Fatal("invalid provider response accepted")
			}
		})
	}
}
func TestDocsPermissionScopes(t *testing.T) {
	c := CLI{Provider: "claude"}
	dir := t.TempDir()
	path := filepath.Join(dir, "docs")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	args, err := c.arguments(Request{ProjectDir: dir, Mode: DocsOnly, DocsPaths: []string{"docs"}})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "acceptEdits") || !strings.Contains(joined, "Edit(/docs/**)") {
		t.Fatal("docs permissions not confined")
	}
}
