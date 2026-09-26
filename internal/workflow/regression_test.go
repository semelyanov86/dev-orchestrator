package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"dev-orchestrator/internal/agent"
	"dev-orchestrator/internal/decision"
	"dev-orchestrator/internal/report"
)

func TestPlanRevisionRequiresExplicitReviewerClosure(t *testing.T) {
	for _, verified := range []bool{false, true} {
		name := "omitted closure"
		if verified {
			name = "verified closure"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "feature")
			passes := 0
			f.codex.run = func(_ context.Context, req agent.Request) (agent.Result, error) {
				r := goodReport(req)
				if req.Stage == "plan-review" {
					passes++
					if passes == 1 {
						r.Findings = []report.Finding{finding("confirmed")}
					}
					if passes > 1 && verified {
						r.Findings = []report.Finding{finding("fixed")}
					}
				}
				if req.Stage == "code-review" && verified {
					r.Findings = []report.Finding{finding("fixed")}
				}
				return agent.Result{Report: r}, nil
			}
			f.claude.run = func(_ context.Context, req agent.Request) (agent.Result, error) {
				r := goodReport(req)
				if req.Stage == "plan-revision" {
					r.Findings = []report.Finding{finding("fixed")}
				}
				return agent.Result{Report: r}, nil
			}
			r, _ := f.engine.Run(t.Context(), f.input)
			if verified && r.Outcome != "success" {
				t.Fatalf("reviewed closure rejected: %s %s", r.Outcome, r.Reason)
			}
			if !verified && (r.Outcome == "success" || f.claude.writes() > 0) {
				t.Fatal("unreviewed plan blocker reached writer")
			}
		})
	}
}

func TestIncompleteImportedReviewCannotSucceed(t *testing.T) {
	for _, status := range []string{"incomplete", "unknown", "unsatisfied"} {
		t.Run(status, func(t *testing.T) {
			f := newFixture(t, "fix_review")
			f.input.Imported = "existing review"
			f.codex.run = func(_ context.Context, req agent.Request) (agent.Result, error) {
				r := goodReport(req)
				if status == "incomplete" {
					r.Outcome = status
				} else {
					r.Requirements[0].Status = status
				}
				return agent.Result{Report: r}, nil
			}
			r, _ := f.engine.Run(t.Context(), f.input)
			if r.Outcome == "success" || f.claude.writes() > 0 {
				t.Fatal("incomplete imported applicability accepted")
			}
		})
	}
}

func TestIncompleteValidationAttributionCannotWrite(t *testing.T) {
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
			r.Outcome = "incomplete"
			r.FailureClass = "current_change"
			r.MissingEvidence = []string{"cause is unverified"}
			r.Requirements[0].Evidence = []report.Evidence{{Kind: "file", Reference: "main.go:1", Detail: "suspected change"}}
		}
		return agent.Result{Report: r}, nil
	}
	r, _ := f.engine.Run(t.Context(), f.input)
	if r.Outcome == "success" || f.claude.writes() != 1 || r.Budgets.ValidationRepairs != 0 {
		t.Fatal("speculative repair performed")
	}
}

func TestUsefulImplementationContinuationsDoNotConsumeNoProgress(t *testing.T) {
	f := newFixture(t, "feature")
	f.engine.Config.Workflow.MaxImplementationRounds = 4
	writes := 0
	f.claude.run = func(_ context.Context, req agent.Request) (agent.Result, error) {
		r := goodReport(req)
		if req.Mode != agent.ReadOnly {
			writes++
			f.inspector.change()
			if writes < 3 {
				r.Outcome = "incomplete"
			}
		}
		return agent.Result{Report: r}, nil
	}
	r, _ := f.engine.Run(t.Context(), f.input)
	if r.Outcome != "success" || writes != 3 || r.Budgets.NoProgress != 0 {
		t.Fatalf("useful progress stopped: %s %s %+v", r.Outcome, r.Reason, r.Budgets)
	}
}

func TestCancelDuringClarificationCancelsRunContext(t *testing.T) {
	f := newFixture(t, "feature")
	updates := make(chan Update)
	f.engine.Updates = updates
	started := make(chan struct{})
	f.engine.Ask = func(ctx context.Context, _ string) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}
	f.claude.run = func(_ context.Context, req agent.Request) (agent.Result, error) {
		r := goodReport(req)
		r.Outcome = "needs_input"
		r.UnresolvedQuestions = []string{"clarify scope"}
		return agent.Result{Report: r}, nil
	}
	done := make(chan Run, 1)
	go func() { r, _ := f.engine.Run(t.Context(), f.input); done <- r }()
	<-started
	updates <- Update{Kind: "cancel"}
	r := <-done
	if r.Outcome != "cancelled" || r.ExitCode != 130 || f.claude.writes() > 0 {
		t.Fatalf("cancelled question outcome: %s", r.Outcome)
	}
}

func TestSIGTERMPersistedStatus(t *testing.T) {
	f := newFixture(t, "feature")
	ctx, cancel := context.WithCancelCause(t.Context())
	f.claude.run = func(_ context.Context, req agent.Request) (agent.Result, error) {
		cancel(Interruption{Status: 143})
		return agent.Result{}, context.Canceled
	}
	r, _ := f.engine.Run(ctx, f.input)
	if r.Outcome != "cancelled" || r.ExitCode != 143 {
		t.Fatalf("signal status differs: %s %d", r.Outcome, r.ExitCode)
	}
}

func TestIncidentFormatRepairStaysIndependent(t *testing.T) {
	f := newFixture(t, "incident")
	f.claude.run = func(_ context.Context, req agent.Request) (agent.Result, error) {
		if req.Stage == "incident-claude" {
			return agent.Result{ReportError: "invalid json", Output: "OWN_RESULT"}, nil
		}
		if strings.Contains(req.Stage, "format-repair") && strings.Contains(req.Prompt, "OTHER_RESULT") {
			t.Error("repair saw other initial report")
		}
		return agent.Result{Report: goodReport(req)}, nil
	}
	f.codex.run = func(_ context.Context, req agent.Request) (agent.Result, error) {
		r := goodReport(req)
		if req.Stage == "incident-codex" {
			r.Markdown = "OTHER_RESULT"
		}
		return agent.Result{Report: r}, nil
	}
	r, _ := f.engine.Run(t.Context(), f.input)
	if r.Outcome != "success" || len(r.Steps) != 4 {
		t.Fatalf("incident repair: %s %s", r.Outcome, r.Reason)
	}
}

type correctingDecision struct {
	update chan<- Update
	once   bool
}

func (c *correctingDecision) Decide(ctx context.Context, input decision.DecisionInput) (decision.StepDecision, error) {
	if !c.once && input.Point == "code_review" {
		c.once = true
		accepted := make(chan struct{})
		c.update <- Update{Kind: "correction", Text: "also preserve compatibility", Accepted: accepted}
		<-accepted
	}
	return (decision.LocalDecisionService{}).Decide(ctx, input)
}
func TestCorrectionDuringGateCannotApplyOldComplete(t *testing.T) {
	f := newFixture(t, "feature")
	updates := make(chan Update)
	f.engine.Updates = updates
	f.engine.Decisions = &correctingDecision{update: updates}
	r, _ := f.engine.Run(t.Context(), f.input)
	if r.Outcome != "success" || r.Budgets.PlanRevisions != 1 || r.Budgets.Implementations != 2 || len(r.Updates) != 1 {
		t.Fatalf("correction skipped fresh plan/review: %s %+v", r.Outcome, r.Budgets)
	}
}

func TestAnswerProposedMinorFindingUsesVerification(t *testing.T) {
	f := newFixture(t, "research")
	reviews := 0
	f.claude.run = func(_ context.Context, req agent.Request) (agent.Result, error) {
		r := goodReport(req)
		if req.Stage == "answer-review" {
			reviews++
			item := finding("proposed")
			item.Severity = "low"
			if reviews > 1 {
				item.Status = "dismissed"
			}
			r.Findings = []report.Finding{item}
		}
		return agent.Result{Report: r}, nil
	}
	f.codex.run = func(_ context.Context, req agent.Request) (agent.Result, error) {
		r := goodReport(req)
		if req.Stage == "final" {
			item := finding("dismissed")
			item.Severity = "low"
			r.Findings = []report.Finding{item}
		}
		return agent.Result{Report: r}, nil
	}
	r, _ := f.engine.Run(t.Context(), f.input)
	if r.Outcome != "success" || r.Budgets.Investigations != 1 || len(r.Questions) > 0 {
		t.Fatalf("minor finding asked user: %s %s", r.Outcome, r.Reason)
	}
}

func TestResearchAcknowledgedOutOfScopeUnknownDoesNotRepeat(t *testing.T) {
	f := newFixture(t, "research")
	f.claude.run = func(_ context.Context, req agent.Request) (agent.Result, error) {
		r := goodReport(req)
		r.MissingEvidence = []string{"runtime validation deliberately not executed in read-only explanation scope"}
		return agent.Result{Report: r}, nil
	}
	r, _ := f.engine.Run(t.Context(), f.input)
	if r.Outcome != "success" || r.Budgets.Investigations != 0 || len(r.Steps) != 3 {
		t.Fatalf("completed research repeated: %s %+v", r.Outcome, r.Budgets)
	}
}

func TestExactReviewFixAndPassBudgets(t *testing.T) {
	f := newFixture(t, "feature")
	f.claude.run = func(_ context.Context, req agent.Request) (agent.Result, error) {
		if req.Mode != agent.ReadOnly {
			f.inspector.change()
		}
		return agent.Result{Report: goodReport(req)}, nil
	}
	f.codex.run = func(_ context.Context, req agent.Request) (agent.Result, error) {
		r := goodReport(req)
		if strings.Contains(req.Stage, "code-review") || req.Stage == "code-re-review" {
			r.Findings = []report.Finding{finding("confirmed")}
		}
		return agent.Result{Report: r}, nil
	}
	r, _ := f.engine.Run(t.Context(), f.input)
	if r.Outcome == "success" || r.Budgets.ReviewFixes != 2 || r.Budgets.ReviewPasses != 3 || f.claude.writes() != 3 || f.validator.calls != 3 {
		t.Fatalf("review limit exceeded/hidden: %s %+v", r.Outcome, r.Budgets)
	}
}

func TestNoProgressAndStepLimitsStopIncompleteWork(t *testing.T) {
	for _, steps := range []int{3, 40} {
		name := "no progress"
		if steps == 3 {
			name = "step budget"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "feature")
			f.engine.Config.Workflow.MaxSteps = steps
			f.engine.Config.Workflow.MaxImplementationRounds = 4
			f.claude.run = func(_ context.Context, req agent.Request) (agent.Result, error) {
				r := goodReport(req)
				if req.Mode != agent.ReadOnly && steps == 40 {
					r.Outcome = "incomplete"
				}
				return agent.Result{Report: r}, nil
			}
			r, _ := f.engine.Run(t.Context(), f.input)
			if r.Outcome != "unresolved" || len(r.Steps) > steps {
				t.Fatalf("limit outcome: %s %s", r.Outcome, r.Reason)
			}
			if steps == 40 && (r.Budgets.NoProgress != 2 || f.claude.writes() != 2) {
				t.Fatalf("no-progress boundary: %+v", r.Budgets)
			}
		})
	}
}

func TestOptionalParallelResearchInvestigations(t *testing.T) {
	f := newFixture(t, "research")
	f.input.ParallelRequired = true
	started := make(chan string, 2)
	release := make(chan struct{})
	callback := func(who string) func(context.Context, agent.Request) (agent.Result, error) {
		return func(ctx context.Context, req agent.Request) (agent.Result, error) {
			r := goodReport(req)
			if req.Stage == "parallel-investigation" || req.Stage == "draft" {
				if strings.Contains(req.Prompt, "OTHER_INITIAL_RESULT") {
					t.Error("parallel report leaked into initial request")
				}
				started <- who
				select {
				case <-release:
				case <-ctx.Done():
					return agent.Result{}, ctx.Err()
				}
				r.Markdown = "OTHER_INITIAL_RESULT " + who
			}
			return agent.Result{Report: r}, nil
		}
	}
	f.claude.run = callback("claude")
	f.codex.run = callback("codex")
	done := make(chan Run, 1)
	go func() { r, _ := f.engine.Run(t.Context(), f.input); done <- r }()
	<-started
	<-started
	close(release)
	r := <-done
	if r.Outcome != "success" || len(r.Steps) != 4 || f.claude.writes() != 0 {
		t.Fatalf("optional parallel branch: %s", r.Outcome)
	}
}

func TestOperationalDiagnosisWithoutValidationTargetDoesNotWrite(t *testing.T) {
	f := newFixture(t, "bug")
	f.validator.preflight = errors.New("missing task all")
	callback := func(_ context.Context, req agent.Request) (agent.Result, error) {
		r := goodReport(req)
		r.Diagnosis = "operational"
		return agent.Result{Report: r}, nil
	}
	f.claude.run = callback
	f.codex.run = callback
	r, _ := f.engine.Run(t.Context(), f.input)
	if r.Outcome != "success" || f.claude.writes() != 0 || f.validator.calls != 0 || r.Validation.Status != "not_required" {
		t.Fatalf("operational diagnosis required writer validation: %s %s", r.Outcome, r.Reason)
	}
}
