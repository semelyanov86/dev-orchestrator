package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dev-orchestrator/internal/runner"
	"dev-orchestrator/internal/safety"
)

func TestProbeMinimalSchemaAndProviderArgs(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			reply := "ping"
			if provider == "claude" {
				reply = "pong"
			}
			raw := `{"reply":"` + reply + `"}`
			envelope, err := json.Marshal(map[string]any{"is_error": false, "result": raw})
			if err != nil {
				t.Fatal(err)
			}
			output := string(envelope)
			if provider == "codex" {
				data, err := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]string{"type": "agent_message", "text": raw}})
				if err != nil {
					t.Fatal(err)
				}
				output = string(data)
			}
			executor := &fakeExecutor{output: runner.Result{Stdout: output, ExitCode: 0}}
			c := CLI{Provider: provider, Command: provider, Exec: executor, Home: t.TempDir(), Cleaner: safety.New()}
			for _, path := range []string{filepath.Join(c.Home, ".codex", "skills"), filepath.Join(c.Home, ".agents", "skills")} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			req := Request{ProjectDir: t.TempDir(), StepID: "01-connection", Stage: "connection", Mode: ReadOnly, ProbeReply: reply, Prompt: "Reply only.", Timeout: time.Second}
			result, err := c.Run(t.Context(), req)
			if err != nil || result.Report.Markdown != reply || result.ReportError != "" {
				t.Fatalf("probe report=%+v error=%v", result.Report, err)
			}
			if err := result.Report.Validate(req.StepID, req.Stage); err != nil {
				t.Fatal(err)
			}
			args := strings.Join(executor.req.Args, " ")
			if executor.req.Command != "bwrap" || !strings.Contains(args, "--chdir /tmp/probe") || strings.Contains(args, "schema_version") || !strings.Contains(args, "--ro-bind "+req.ProjectDir) {
				t.Fatalf("probe lost minimal/sandbox contract: %s", args)
			}
			if !strings.Contains(args, "--tmpfs "+filepath.Join(c.Home, ".codex", "skills")) || !strings.Contains(args, "--tmpfs "+filepath.Join(c.Home, ".agents", "skills")) {
				t.Fatal("probe exposes installed skills")
			}
			if provider == "codex" && (!strings.Contains(args, "project_doc_max_bytes=0") || !strings.Contains(args, `model_instructions_file="/tmp/instructions.md"`) || !strings.Contains(args, "--disable code_mode_host")) {
				t.Fatal("Codex probe loads normal instructions/tools")
			}
			if provider == "claude" && (!strings.Contains(args, "--tools  --permission-mode dontAsk") || !strings.Contains(args, "--safe-mode") || !strings.Contains(args, "--system-prompt")) {
				t.Fatal("Claude probe loads normal instructions/tools")
			}
			if provider == "claude" && strings.Contains(args, "--json-schema") {
				t.Fatal("Claude probe adds a structured-output tool round")
			}
		})
	}
}

func TestProbeClaudeFailures(t *testing.T) {
	for _, raw := range []string{`{"is_error":true,"result":"{}"}`, `{"is_error":false}`, `not json`} {
		if _, err := decodeProbeClaude(raw); err == nil {
			t.Fatalf("Claude error accepted: %s", raw)
		}
	}
}

func TestProbeRejectsUnexpectedResponses(t *testing.T) {
	for _, raw := range []string{`{"reply":"pong"}`, `{"reply":"ping","extra":"ignored"}`, `{"reply":"ping"} {}`, `ping`, `null`, `{}`} {
		t.Run(raw, func(t *testing.T) {
			c := CLI{Provider: "codex"}
			if _, err := c.probeReport(Request{ProbeReply: "ping"}, raw); err == nil {
				t.Fatal("unexpected reply accepted")
			}
		})
	}
}
