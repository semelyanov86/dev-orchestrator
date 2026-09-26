package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dev-orchestrator/internal/agent"
	"dev-orchestrator/internal/artifacts"
	"dev-orchestrator/internal/config"
	"dev-orchestrator/internal/decision"
	"dev-orchestrator/internal/project"
	"dev-orchestrator/internal/report"
	"dev-orchestrator/internal/safety"
	"dev-orchestrator/internal/ui"
	"dev-orchestrator/internal/validation"
)

type fakeAgent struct {
	mu       sync.Mutex
	requests []agent.Request
	run      func(context.Context, agent.Request) (agent.Result, error)
}

func (f *fakeAgent) Run(ctx context.Context, r agent.Request) (agent.Result, error) {
	f.mu.Lock()
	f.requests = append(f.requests, r)
	f.mu.Unlock()
	if f.run != nil {
		return f.run(ctx, r)
	}
	return agent.Result{Report: goodReport(r), ExitCode: 0}, nil
}
func (f *fakeAgent) writes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if r.Mode != agent.ReadOnly {
			n++
		}
	}
	return n
}

type fakeInspector struct {
	mu       sync.Mutex
	snapshot project.Snapshot
}

func (f *fakeInspector) Snapshot(context.Context, string) (project.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.snapshot
	s.Files = map[string]string{}
	for k, v := range f.snapshot.Files {
		s.Files[k] = v
	}
	return s, nil
}
func (f *fakeInspector) Diff(context.Context, string, string) (string, error) {
	return "synthetic diff", nil
}
func (f *fakeInspector) change() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshot.ID += "x"
	f.snapshot.Files["main.go"] = f.snapshot.ID
}

type fakeValidator struct {
	calls     int
	failures  int
	preflight error
}

func (f *fakeValidator) Preflight(context.Context, string) error { return f.preflight }
func (f *fakeValidator) Validate(context.Context, string) (validation.Result, error) {
	f.calls++
	if f.calls <= f.failures {
		return validation.Result{Status: "failed", ExitCode: 1, Output: "test failure"}, errors.New("validation failed")
	}
	return validation.Result{Status: "passed", ExitCode: 0, Output: "all passed"}, nil
}

type fakeRemote struct{ calls int }

func (f *fakeRemote) Investigate(context.Context, config.Server) (string, error) {
	f.calls++
	return "revision synthetic; service healthy", nil
}

type fakeEvents struct {
	mu     sync.Mutex
	events []ui.Event
}

func (f *fakeEvents) Emit(e ui.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

func goodReport(r agent.Request) report.StepReport {
	markdown := "Synthetic result for " + r.Stage
	id := "task"
	if r.ProbeReply != "" {
		markdown = r.ProbeReply
		id = r.StepID
	}
	return report.StepReport{SchemaVersion: 1, StepID: r.StepID, Stage: r.Stage, Outcome: "completed", Summary: "stage completed", Markdown: markdown, Requirements: []report.Requirement{{ID: id, Description: "authorized task result", Status: "satisfied", Evidence: []report.Evidence{{Kind: "observation", Reference: "stage_contract", Detail: "checked current task"}}}}, Findings: []report.Finding{}, MissingEvidence: []string{}, Disagreements: []string{}, UnresolvedQuestions: []string{}, ScopeChanges: []string{}, ArtifactReferences: []string{}, Diagnosis: "local_code", FailureClass: "not_applicable", Verdict: "pass"}
}
func finding(status string) report.Finding {
	return report.Finding{ID: "f1", Severity: "high", Category: "correctness", Status: status, Problem: "wrong behavior", Impact: "incorrect result", SuggestedDirection: "fix the behavior", File: "main.go", Line: 1, Evidence: []report.Evidence{{Kind: "file", Reference: "main.go:1", Detail: "current implementation evidence"}}, Rationale: "verified locally"}
}

type fixture struct {
	engine    *Engine
	input     Input
	claude    *fakeAgent
	codex     *fakeAgent
	validator *fakeValidator
	inspector *fakeInspector
	remote    *fakeRemote
}

func newFixture(t *testing.T, name string) *fixture {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := artifacts.New(t.TempDir(), dir, safety.New("secret-value"))
	if err != nil {
		t.Fatal(err)
	}
	inspector := &fakeInspector{snapshot: project.Snapshot{ID: "initial", Head: "head", Branch: "main", Files: map[string]string{"main.go": "original", "user.txt": "user state"}, Status: " M user.txt\x00?? note.txt\x00", Unstaged: 1, Untracked: 1}}
	initial, err := inspector.Snapshot(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	claude := &fakeAgent{}
	codex := &fakeAgent{}
	validator := &fakeValidator{}
	remote := &fakeRemote{}
	e := &Engine{Config: config.Defaults(), Claude: claude, Codex: codex, Decisions: decision.LocalDecisionService{}, Validator: validator, Inspector: inspector, Remote: remote, Events: &fakeEvents{}, Store: store}
	return &fixture{engine: e, input: Input{Project: project.Project{Root: dir, Initial: initial}, Workflow: name, Task: "Complete the authorized synthetic task", Router: "explicit"}, claude: claude, codex: codex, validator: validator, inspector: inspector, remote: remote}
}

func TestRunWorkflowCatalog(t *testing.T) {
	for _, d := range Catalog() {
		t.Run(d.Name, func(t *testing.T) {
			f := newFixture(t, d.Name)
			if d.Input == "plan" || d.Input == "report" {
				f.input.Imported = "synthetic imported document"
			}
			if d.Name == "server_bug" {
				f.input.Server = "production"
				f.engine.Config.Servers["production"] = config.Server{Host: "example.com", Port: 22, ProjectPath: "/srv/app"}
			}
			if d.Name == "docs" {
				f.input.DocsPaths = []string{"main.go"}
				f.input.Project.Instructions = "task all"
			}
			result, err := f.engine.Run(t.Context(), f.input)
			if err != nil || result.Outcome != "success" {
				t.Fatalf("Run = %s, %v, reason %s", result.Outcome, err, result.Reason)
			}
			if !Writing(d.Name) && f.claude.writes() != 0 {
				t.Fatal("read-only workflow invoked writer")
			}
			if result.Initial.Files["user.txt"] != "user state" {
				t.Fatal("initial dirty tree lost")
			}
			data, err := os.ReadFile(filepath.Join(result.Artifacts, "run.json"))
			if err != nil {
				t.Fatal(err)
			}
			var saved Run
			if err := json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			if saved.Outcome != result.Outcome || saved.ExitCode != result.ExitCode {
				t.Fatal("artifact outcome mismatch")
			}
		})
	}
}

func TestRunReviewFixesRequireValidationAndVerification(t *testing.T) {
	f := newFixture(t, "feature")
	f.claude.run = func(_ context.Context, req agent.Request) (agent.Result, error) {
		r := goodReport(req)
		if req.Mode != agent.ReadOnly {
			f.inspector.change()
		}
		if req.Stage == "review-fixes" {
			r.Findings = []report.Finding{finding("fixed")}
		}
		return agent.Result{Report: r}, nil
	}
	f.codex.run = func(_ context.Context, req agent.Request) (agent.Result, error) {
		r := goodReport(req)
		if req.Stage == "code-review" {
			r.Findings = []report.Finding{finding("confirmed")}
		}
		if req.Stage == "code-re-review" {
			r.Findings = []report.Finding{finding("fixed")}
		}
		return agent.Result{Report: r}, nil
	}
	result, err := f.engine.Run(t.Context(), f.input)
	if err != nil || result.Outcome != "success" {
		t.Fatalf("outcome %s error %v reason %s", result.Outcome, err, result.Reason)
	}
	if f.validator.calls != 2 || result.Budgets.ReviewFixes != 1 || result.Budgets.ReviewPasses != 2 {
		t.Fatalf("wrong loop budgets %+v validations=%d", result.Budgets, f.validator.calls)
	}
	var stages []string
	for _, s := range result.Steps {
		stages = append(stages, s.Stage)
	}
	joined := strings.Join(stages, ",")
	if !strings.Contains(joined, "review-fixes,validation,code-re-review") {
		t.Fatalf("wrong sequence %s", joined)
	}
}

func TestRunCompletionBlockers(t *testing.T) {
	for _, tt := range []struct {
		name      string
		status    string
		unknown   bool
		maxFixes  int
		maxPasses int
	}{{name: "confirmed blocker zero fixes", status: "confirmed", maxFixes: 0, maxPasses: 3}, {name: "disputed evidence", status: "disputed", maxFixes: 2, maxPasses: 1}, {name: "proposed hypothesis", status: "proposed", maxFixes: 2, maxPasses: 1}, {name: "unknown criterion", unknown: true, maxFixes: 2, maxPasses: 1}, {name: "writer fixed without review confirmation", status: "fixed", maxFixes: 2, maxPasses: 1}} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, "feature")
			f.engine.Config.Review.MaxIterations = tt.maxFixes
			f.engine.Config.Review.MaxPasses = tt.maxPasses
			f.codex.run = func(_ context.Context, req agent.Request) (agent.Result, error) {
				r := goodReport(req)
				if req.Stage == "code-review" {
					if tt.status != "" {
						r.Findings = []report.Finding{finding(tt.status)}
					}
					if tt.unknown {
						r.Requirements[0].Status = "unknown"
					}
				}
				return agent.Result{Report: r}, nil
			}
			if tt.status == "fixed" {
				f.claude.run = func(_ context.Context, req agent.Request) (agent.Result, error) {
					r := goodReport(req)
					if req.Mode != agent.ReadOnly {
						r.Findings = []report.Finding{finding("fixed")}
					}
					return agent.Result{Report: r}, nil
				}
				f.codex.run = func(_ context.Context, req agent.Request) (agent.Result, error) {
					return agent.Result{Report: goodReport(req)}, nil
				}
			}
			result, _ := f.engine.Run(t.Context(), f.input)
			if result.Outcome == "success" {
				t.Fatal("unmet completion predicate accepted")
			}
		})
	}
}

func TestRunValidationFailureAttribution(t *testing.T) {
	for _, class := range []string{"current_change", "pre_existing", "infrastructure", "unknown"} {
		t.Run(class, func(t *testing.T) {
			f := newFixture(t, "feature")
			f.validator.failures = 1
			f.claude.run = func(_ context.Context, req agent.Request) (agent.Result, error) {
				if req.Mode != agent.ReadOnly {
					f.inspector.change()
				}
				return agent.Result{Report: goodReport(req)}, nil
			}
			f.codex.run = func(_ context.Context, req agent.Request) (agent.Result, error) {
				r := goodReport(req)
				if req.Stage == "validation-failure-investigation" {
					r.FailureClass = class
					r.Requirements[0].Evidence = []report.Evidence{{Kind: "file", Reference: "main.go:1", Detail: "change causes validation failure"}}
				}
				return agent.Result{Report: r}, nil
			}
			result, _ := f.engine.Run(t.Context(), f.input)
			if class == "current_change" {
				if result.Outcome != "success" || result.Budgets.ValidationRepairs != 1 || f.validator.calls != 2 {
					t.Fatalf("failed repair %+v", result)
				}
			} else {
				if result.Outcome == "success" || f.claude.writes() != 1 {
					t.Fatalf("unrelated failure auto-fixed outcome=%s writes=%d", result.Outcome, f.claude.writes())
				}
			}
		})
	}
}

func TestRunFormatRepairIsReadOnlyAndBounded(t *testing.T) {
	for _, tt := range []struct {
		name        string
		repairValid bool
	}{{name: "valid repair", repairValid: true}, {name: "invalid repair"}} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, "feature")
			writes := 0
			f.claude.run = func(_ context.Context, req agent.Request) (agent.Result, error) {
				if req.Mode != agent.ReadOnly {
					writes++
					return agent.Result{ReportError: "invalid json", Output: "partial implementation"}, nil
				}
				if strings.HasSuffix(req.Stage, "format-repair") && !tt.repairValid {
					return agent.Result{ReportError: "still invalid"}, nil
				}
				return agent.Result{Report: goodReport(req)}, nil
			}
			result, _ := f.engine.Run(t.Context(), f.input)
			if writes != 1 {
				t.Fatalf("writer retried %d times", writes)
			}
			if tt.repairValid && result.Outcome != "success" {
				t.Fatalf("valid repair outcome %s: %s", result.Outcome, result.Reason)
			}
			if !tt.repairValid && result.Outcome != "failed" {
				t.Fatalf("invalid repair outcome %s", result.Outcome)
			}
		})
	}
}

func TestRunMissingInputsAndValidationTarget(t *testing.T) {
	for _, name := range []string{"implement", "fix_review", "server_bug", "docs", "feature"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, name)
			if name == "feature" {
				f.validator.preflight = errors.New("missing task all")
			}
			result, _ := f.engine.Run(t.Context(), f.input)
			if result.Outcome != "needs_input" || f.claude.writes() != 0 {
				t.Fatalf("missing input outcome %s writes %d", result.Outcome, f.claude.writes())
			}
		})
	}
}

func TestRunProcessFailureAndCancellation(t *testing.T) {
	for _, name := range []string{"partial writer failure", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "feature")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			f.claude.run = func(_ context.Context, req agent.Request) (agent.Result, error) {
				if req.Mode != agent.ReadOnly {
					f.inspector.change()
					if name == "cancelled" {
						cancel()
						return agent.Result{ExitCode: -1}, context.Canceled
					}
					return agent.Result{ExitCode: 1, Output: "partial writes"}, errors.New("process failed")
				}
				return agent.Result{Report: goodReport(req)}, nil
			}
			result, _ := f.engine.Run(ctx, f.input)
			want := "failed"
			if name == "cancelled" {
				want = "cancelled"
			}
			if result.Outcome != want || f.claude.writes() != 1 || result.Current.ID == result.Initial.ID {
				t.Fatalf("wrong terminal state %s writes %d", result.Outcome, f.claude.writes())
			}
		})
	}
}

func TestRunIncidentIndependentParallelInvestigations(t *testing.T) {
	f := newFixture(t, "incident")
	started := make(chan string, 2)
	release := make(chan struct{})
	investigation := func(who string) func(context.Context, agent.Request) (agent.Result, error) {
		return func(ctx context.Context, req agent.Request) (agent.Result, error) {
			r := goodReport(req)
			if strings.HasPrefix(req.Stage, "incident-") && !strings.Contains(req.Stage, "synthesis") {
				if strings.Contains(req.Prompt, "PRIVATE_OTHER_RESULT") {
					return agent.Result{}, errors.New("independence violated")
				}
				started <- who
				select {
				case <-release:
				case <-ctx.Done():
					return agent.Result{}, ctx.Err()
				}
				r.Markdown = "PRIVATE_OTHER_RESULT " + who
			}
			return agent.Result{Report: r}, nil
		}
	}
	f.claude.run = investigation("claude")
	f.codex.run = investigation("codex")
	done := make(chan Run, 1)
	go func() { r, _ := f.engine.Run(t.Context(), f.input); done <- r }()
	<-started
	<-started
	close(release)
	result := <-done
	if result.Outcome != "success" {
		t.Fatalf("incident outcome %s: %s", result.Outcome, result.Reason)
	}
}

type badDecision struct{}

func (badDecision) Decide(context.Context, decision.DecisionInput) (decision.StepDecision, error) {
	return decision.StepDecision{Action: "deploy", Source: "jev"}, nil
}
func TestRunRejectsUnknownAction(t *testing.T) {
	f := newFixture(t, "feature")
	f.engine.Decisions = badDecision{}
	r, err := f.engine.Run(t.Context(), f.input)
	if err != nil || r.Outcome != "success" {
		t.Fatalf("policy fallback %s %v", r.Outcome, err)
	}
	for _, step := range r.Steps {
		if strings.Contains(step.Stage, "deploy") {
			t.Fatal("unknown action executed")
		}
	}
}

func TestRunReadOnlySnapshotDrift(t *testing.T) {
	f := newFixture(t, "plan")
	f.claude.run = func(_ context.Context, req agent.Request) (agent.Result, error) {
		f.inspector.change()
		return agent.Result{Report: goodReport(req)}, nil
	}
	r, _ := f.engine.Run(t.Context(), f.input)
	if r.Outcome != "failed" || f.claude.writes() != 0 {
		t.Fatalf("drift accepted %s", r.Outcome)
	}
}
