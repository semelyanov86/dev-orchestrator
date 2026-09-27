//go:build integration

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"dev-orchestrator/internal/project"
	"dev-orchestrator/internal/runner"
	"dev-orchestrator/internal/safety"
	"dev-orchestrator/prompts"
)

func TestReadOnlyProviders(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "codex"} {
		t.Run(name, func(t *testing.T) {
			if _, err := exec.LookPath(name); err != nil {
				t.Skip("provider not installed")
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Synthetic smoke\nThis repository contains no customer data or secrets.\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(dir, ".codex"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".codex", "config.toml"), []byte("invalid project config must be masked\n"), 0600); err != nil {
				t.Fatal(err)
			}
			git := exec.Command("git", "init", "-q", dir)
			if out, err := git.CombinedOutput(); err != nil {
				t.Fatalf("synthetic Git init %v %s", err, out)
			}
			runner := runner.New()
			c := CLI{Provider: name, Command: name, Exec: runner, Home: home, StateRoot: t.TempDir(), Cleaner: safety.New(os.Getenv("OPENROUTER_API_KEY"))}
			if err := c.Preflight(t.Context(), dir); err != nil {
				t.Fatal(err)
			}
			inspector := project.Inspector{Exec: runner}
			before, err := inspector.Snapshot(t.Context(), dir)
			if err != nil {
				t.Fatal(err)
			}
			prompt, err := prompts.Build("final", "Read README.md and summarize its purpose in one sentence. Do not modify files.", "", "StepID: smoke; stage: smoke; schema_version: 1; access: read_only. One requirement id=task, satisfied only after reading README.md. Use file evidence README.md. All unused collections must be []; diagnosis/failure_class/verdict=not_applicable.")
			if err != nil {
				t.Fatal(err)
			}
			result, err := c.Run(t.Context(), Request{ProjectDir: dir, StepID: "smoke", Stage: "smoke", Prompt: prompt, Mode: ReadOnly, Timeout: 90 * time.Second})
			if err != nil {
				t.Fatalf("provider process: %v\n%s", err, result.ErrorOutput)
			}
			if result.ReportError != "" {
				t.Fatalf("report: %s\n%s", result.ReportError, result.Output)
			}
			if result.Report.Outcome != "completed" || len(result.Report.Requirements) != 1 || result.Report.Requirements[0].Status != "satisfied" {
				t.Fatalf("smoke task incomplete: %s", result.Report.Markdown)
			}
			after, err := inspector.Snapshot(t.Context(), dir)
			if err != nil {
				t.Fatal(err)
			}
			if before.ID != after.ID {
				t.Fatal("provider modified synthetic project")
			}
			t.Logf("%s: %s; unchanged snapshot", name, result.Report.Markdown)
		})
	}
}

func TestSandboxConfigMasksReadable(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap not installed")
	}
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			dir := t.TempDir()
			home := t.TempDir()
			paths := []string{
				filepath.Join(dir, ".codex", "config.toml"),
				filepath.Join(home, ".codex", "config.toml"),
				filepath.Join(home, ".config", "dev-agent", "config.yaml"),
				filepath.Join(dir, ".dev-agent.yaml"),
				filepath.Join(dir, ".env"),
			}
			for _, path := range paths {
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("must remain hidden\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			c := CLI{Provider: provider, Home: home}
			args, err := c.sandbox(Request{ProjectDir: dir, Mode: ReadOnly}, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			args = append(args, "/usr/bin/cat")
			args = append(args, paths...)
			result, err := runner.New().Run(t.Context(), runner.Request{Command: "bwrap", Args: args, Dir: dir, Timeout: 10 * time.Second})
			if err != nil || result.Stdout != "" {
				t.Fatalf("masked configs unreadable or exposed: %v stdout=%q stderr=%s", err, result.Stdout, result.Stderr)
			}
			for _, path := range paths {
				content, err := os.ReadFile(path)
				if err != nil || string(content) != "must remain hidden\n" {
					t.Fatalf("host config modified: %s %v", path, err)
				}
			}
		})
	}
}
