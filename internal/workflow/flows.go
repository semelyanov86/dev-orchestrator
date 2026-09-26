package workflow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"dev-orchestrator/internal/agent"
	"dev-orchestrator/internal/decision"
	"dev-orchestrator/internal/report"
	"dev-orchestrator/internal/ui"
)

func (s *session) execute(ctx context.Context) error {
	switch s.input.Workflow {
	case "ping":
		return s.ping(ctx)
	case "feature", "plan":
		plan, err := s.stage(ctx, "claude", "plan", "plan", agent.ReadOnly, "")
		if err != nil {
			return err
		}
		s.plan = plan.Markdown
		if err := s.planGate(ctx, "codex"); err != nil {
			return err
		}
		if s.input.Workflow == "plan" {
			s.run.Final = s.plan
			return nil
		}
		return s.writing(ctx)
	case "implement":
		s.plan = s.input.Imported
		if err := s.planGate(ctx, "codex"); err != nil {
			return err
		}
		return s.writing(ctx)
	case "fix_review":
		_, err := s.stage(ctx, "codex", "imported-review-verify", "code-review", agent.ReadOnly, s.input.Imported)
		if err != nil {
			return err
		}
		s.plan = s.handoff
		s.planGeneration = s.generation
		safe := s.safe("review_verification")
		if safe.State.Findings.Disputed > 0 || safe.State.Findings.Proposed > 0 {
			return stop("needs_input", "imported findings need current evidence before writer")
		}
		if safe.State.Findings.UnverifiedFixes > 0 || !s.resultCoverage() {
			return stop("unresolved", "imported report applicability is incomplete")
		}
		if safe.State.Findings.Confirmed == 0 {
			s.run.Final = s.handoff
			return nil
		}
		return s.writing(ctx)
	case "bug", "server_bug", "chore", "refactor", "tests":
		role := "plan"
		stage := "analysis-plan"
		if s.input.Workflow == "bug" || s.input.Workflow == "server_bug" {
			role = "investigate"
			stage = "diagnosis"
		}
		r, err := s.initial(ctx, stage, role)
		if err != nil {
			return err
		}
		s.plan = r.Markdown
		if role == "investigate" {
			if err := s.diagnosisGate(ctx, true); err != nil {
				return err
			}
			if s.latest.Diagnosis != "local_code" {
				s.run.Validation.Status = "not_required"
				s.run.Validation.Snapshot = s.run.Current.ID
				s.run.Validation.Reason = "operational diagnosis without local writes"
				return s.final(ctx)
			}
		} else {
			reviewer := "codex"
			if s.input.Workflow == "refactor" || s.input.Risk == "high" || s.input.Risk == "critical" {
				reviewer = "claude"
			}
			if err := s.planGate(ctx, reviewer); err != nil {
				return err
			}
		}
		s.planGeneration = s.generation
		return s.writing(ctx)
	case "docs":
		r, err := s.stage(ctx, "claude", "documentation-plan", "plan", agent.ReadOnly, "")
		if err != nil {
			return err
		}
		s.plan = r.Markdown
		if err := s.planGate(ctx, "codex"); err != nil {
			return err
		}
		return s.writing(ctx)
	case "architecture", "research":
		return s.answer(ctx)
	case "investigate":
		_, err := s.initial(ctx, "diagnosis", "investigate")
		if err != nil {
			return err
		}
		if err := s.diagnosisGate(ctx, false); err != nil {
			return err
		}
		return s.final(ctx)
	case "review":
		return s.review(ctx)
	case "incident":
		return s.incident(ctx)
	default:
		return errors.New("workflow not implemented")
	}
}

func (s *session) readiness(ctx context.Context) error {
	if s.latest.Outcome != "incomplete" && len(s.latest.UnresolvedQuestions) == 0 && len(s.latest.ScopeChanges) == 0 {
		return nil
	}
	actions := []decision.Action{decision.Clarify}
	if len(s.latest.UnresolvedQuestions) == 0 && len(s.latest.ScopeChanges) == 0 {
		actions = append(actions, decision.Proceed)
	}
	action, err := s.gate(ctx, "task_readiness", actions)
	if err != nil {
		return err
	}
	if action != decision.Proceed {
		return stop("needs_input", "task readiness requires a revised plan")
	}
	return nil
}

func (s *session) planGate(ctx context.Context, reviewer string) error {
	if err := s.readiness(ctx); err != nil {
		return err
	}
	for {
		_, err := s.stage(ctx, reviewer, "plan-review", "plan-review", agent.ReadOnly, s.plan+"\n"+s.handoffJSON())
		if err != nil {
			return err
		}
		state := s.safe("plan_review").State
		actions := []decision.Action{decision.Clarify}
		clean := state.Findings.Blocking == 0 && state.Findings.Disputed == 0 && state.Findings.Proposed == 0 && state.Findings.UnverifiedFixes == 0 && !state.MissingEvidence && !state.Disagreements && state.Requirements.Satisfied > 0 && state.Requirements.Unknown == 0 && state.Requirements.Unsatisfied == 0 && s.latest.Outcome == "completed"
		if clean {
			actions = []decision.Action{decision.Proceed}
		}
		if s.remaining().PlanRevisions > 0 && state.Findings.Blocking+state.Requirements.Unsatisfied > 0 {
			actions = append(actions, decision.RevisePlan)
		}
		if s.remaining().InvestigationRounds > 0 && (state.MissingEvidence || state.Findings.Disputed+state.Findings.Proposed+state.Findings.UnverifiedFixes+state.Requirements.Unknown > 0) {
			actions = append(actions, decision.Verify)
		}
		action, err := s.gate(ctx, "plan_review", actions)
		if err != nil {
			return err
		}
		if action == decision.Proceed {
			s.planGeneration = s.generation
			return nil
		}
		before := s.progressSignature()
		switch action {
		case decision.RevisePlan:
			s.run.Budgets.PlanRevisions++
			writer := "claude"
			if slices.Contains([]string{"bug", "refactor", "tests", "chore"}, s.input.Workflow) {
				writer = "codex"
			}
			r, err := s.stage(ctx, writer, "plan-revision", "plan", agent.ReadOnly, s.plan+"\n"+s.handoffJSON())
			if err != nil {
				return err
			}
			s.plan = r.Markdown
		case decision.Verify, decision.Investigate:
			s.run.Budgets.Investigations++
			if _, err := s.stage(ctx, "codex", "plan-verify", "verify", agent.ReadOnly, s.plan+"\n"+s.handoffJSON()); err != nil {
				return err
			}
		default:
			return stop("unresolved", "plan has unresolved blockers or exhausted revision budget")
		}
		if err := s.progress(before); err != nil {
			return err
		}
	}
}

func (s *session) diagnosisGate(ctx context.Context, writing bool) error {
	for {
		_, err := s.stage(ctx, "claude", "diagnosis-verify", "verify", agent.ReadOnly, s.handoffJSON())
		if err != nil {
			return err
		}
		state := s.safe("diagnosis").State
		actions := []decision.Action{decision.Clarify}
		certain := !state.MissingEvidence && !state.Disagreements && state.Findings.Disputed == 0 && state.Findings.Proposed == 0 && state.Requirements.Unknown == 0 && slices.Contains([]string{"local_code", "operational"}, state.Diagnosis) && s.latest.Outcome == "completed"
		if certain {
			if writing && state.Diagnosis == "local_code" {
				actions = append(actions, decision.Proceed)
			} else {
				actions = append(actions, decision.Finalize)
			}
		}
		if !writing && s.latest.Outcome == "completed" {
			actions = append(actions, decision.Finalize)
		}
		if s.remaining().InvestigationRounds > 0 && !certain {
			actions = append(actions, decision.Investigate)
		}
		action, err := s.gate(ctx, "diagnosis", slices.Compact(actions))
		if err != nil {
			return err
		}
		if action == decision.Proceed || action == decision.Finalize {
			s.plan = s.handoff
			s.planGeneration = s.generation
			return nil
		}
		if s.remaining().InvestigationRounds == 0 {
			return stop("unresolved", "diagnosis evidence budget exhausted")
		}
		before := s.progressSignature()
		s.run.Budgets.Investigations++
		if _, err := s.stage(ctx, "codex", "diagnosis-investigation", "investigate", agent.ReadOnly, s.handoffJSON()); err != nil {
			return err
		}
		if err := s.progress(before); err != nil {
			return err
		}
	}
}

func (s *session) answer(ctx context.Context) error {
	_, err := s.initial(ctx, "draft", "draft")
	if err != nil {
		return err
	}
	for {
		_, err := s.stage(ctx, "claude", "answer-review", "answer-review", agent.ReadOnly, s.handoffJSON())
		if err != nil {
			return err
		}
		state := s.safe("answer_review").State
		actions := []decision.Action{decision.Clarify}
		if state.Findings.Blocking == 0 && state.Findings.Disputed+state.Findings.Proposed+state.Findings.UnverifiedFixes == 0 && state.Requirements.Unsatisfied == 0 && !state.Disagreements && s.latest.Outcome == "completed" {
			actions = append(actions, decision.Finalize)
		}
		if s.remaining().AnswerRevisions > 0 && (state.Findings.Blocking > 0 || state.Requirements.Unsatisfied > 0) {
			actions = append(actions, decision.ReviseAnswer)
		}
		if s.remaining().InvestigationRounds > 0 && (state.MissingEvidence || state.Disagreements || state.Findings.Disputed+state.Findings.Proposed+state.Findings.UnverifiedFixes+state.Requirements.Unknown > 0) {
			actions = append(actions, decision.Investigate)
		}
		action, err := s.gate(ctx, "answer_review", actions)
		if err != nil {
			return err
		}
		if action == decision.Finalize {
			return s.final(ctx)
		}
		before := s.progressSignature()
		if action == decision.ReviseAnswer {
			s.run.Budgets.AnswerRevisions++
			if _, err := s.stage(ctx, "codex", "answer-revision", "draft", agent.ReadOnly, s.handoffJSON()); err != nil {
				return err
			}
		} else {
			if s.remaining().InvestigationRounds == 0 {
				return stop("unresolved", "answer investigation budget exhausted")
			}
			s.run.Budgets.Investigations++
			if _, err := s.stage(ctx, "codex", "answer-investigation", "investigate", agent.ReadOnly, s.handoffJSON()); err != nil {
				return err
			}
		}
		if err := s.progress(before); err != nil {
			return err
		}
	}
}

func (s *session) final(ctx context.Context) error {
	r, err := s.stage(ctx, "codex", "final", "final", agent.ReadOnly, s.handoffJSON())
	if err != nil {
		return err
	}
	s.run.Final = r.Markdown
	if r.Outcome != "completed" {
		return stop("unresolved", "final report incomplete")
	}
	return nil
}

func (s *session) review(ctx context.Context) error {
	if _, err := s.reviewStage(ctx, "codex", "code-review", "code-review"); err != nil {
		return err
	}
	if _, err := s.reviewStage(ctx, "claude", "review-verification", "code-review"); err != nil {
		return err
	}
	for {
		state := s.safe("review_verification").State
		actions := []decision.Action{decision.Finalize}
		if (state.Findings.Disputed+state.Findings.Proposed+state.Findings.UnverifiedFixes > 0 || state.MissingEvidence) && s.remaining().ReviewPasses > 0 {
			actions = append(actions, decision.Verify)
		}
		action, err := s.gate(ctx, "review_verification", actions)
		if err != nil {
			return err
		}
		if action == decision.Finalize {
			break
		}
		before := s.progressSignature()
		if _, err := s.reviewStage(ctx, "codex", "targeted-review-verify", "verify"); err != nil {
			return err
		}
		if err := s.progress(before); err != nil {
			return err
		}
	}
	if err := s.final(ctx); err != nil {
		return err
	}
	state := s.safe("review_verification").State
	if state.Findings.Disputed+state.Findings.Proposed+state.Findings.UnverifiedFixes > 0 || state.Requirements.Unknown+state.Requirements.Unsatisfied > 0 || state.MissingEvidence || state.Disagreements || state.Questions || s.latest.Verdict == "inconclusive" {
		return stop("unresolved", "review is inconclusive")
	}
	if state.Findings.Confirmed > 0 || s.latest.Verdict == "findings" {
		return stop("findings", "confirmed review findings")
	}
	if s.latest.Verdict != "pass" {
		return stop("unresolved", "final review has no verified pass verdict")
	}
	return nil
}

// independent freezes both requests before either result exists. Format repair sees only its own transcript.
func (s *session) independent(ctx context.Context, claudeStage, codexStage, primaryRole string) ([]report.StepReport, error) {
	if err := s.updates(); err != nil {
		return nil, err
	}
	a, err := s.prepare(ctx, "claude", claudeStage, "investigate", agent.ReadOnly, "")
	if err != nil {
		return nil, err
	}
	b, err := s.prepare(ctx, "codex", codexStage, primaryRole, agent.ReadOnly, "")
	if err != nil {
		return nil, err
	}
	type answer struct {
		result agent.Result
		step   Step
		err    error
	}
	answers := make([]answer, 2)
	initialCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	for i, p := range []prepared{a, b} {
		wg.Go(func() {
			r, record, err := s.perform(initialCtx, p)
			answers[i] = answer{result: r, step: record, err: err}
			if err != nil {
				cancel()
			}
		})
	}
	wg.Wait()
	for i, p := range []prepared{a, b} {
		s.run.Steps[p.index] = answers[i].step
	}
	if err := errors.Join(answers[0].err, answers[1].err); err != nil {
		return nil, err
	}
	results := make([]report.StepReport, 2)
	for i, p := range []prepared{a, b} {
		r := answers[i].result
		if r.ReportError != "" {
			repair, err := s.prepare(ctx, p.who, p.req.Stage+"-format-repair", "repair", agent.ReadOnly, r.Output+"\nFormat error: "+r.ReportError)
			if err != nil {
				return nil, err
			}
			fixed, record, err := s.perform(ctx, repair)
			s.run.Steps[repair.index] = record
			if err != nil {
				return nil, err
			}
			if fixed.ReportError != "" {
				return nil, errors.New("bounded independent report repair failed")
			}
			r = fixed
		}
		if err := s.checkEvidence(r.Report); err != nil {
			return nil, err
		}
		results[i] = r.Report
	}
	s.run.Current = recordSnapshot(answers[1].step, s.run.Current)
	return results, nil
}

func (s *session) initial(ctx context.Context, stage, role string) (report.StepReport, error) {
	if !s.input.ParallelRequired || (role != "investigate" && role != "draft") {
		return s.stage(ctx, "codex", stage, role, agent.ReadOnly, "")
	}
	results, err := s.independent(ctx, "parallel-investigation", stage, role)
	if err != nil {
		return report.StepReport{}, err
	}
	primary := results[1]
	primary.Markdown += "\nIndependent Claude investigation:\n" + results[0].Markdown
	primary.MissingEvidence = append(primary.MissingEvidence, results[0].MissingEvidence...)
	primary.Disagreements = append(primary.Disagreements, results[0].Disagreements...)
	primary.UnresolvedQuestions = append(primary.UnresolvedQuestions, results[0].UnresolvedQuestions...)
	primary.ScopeChanges = append(primary.ScopeChanges, results[0].ScopeChanges...)
	s.merge(results[0], false)
	s.merge(primary, false)
	s.latest = primary
	s.lastGeneration = s.generation
	s.handoff = primary.Markdown
	if err := s.persist(); err != nil {
		return primary, err
	}
	return primary, nil
}

func (s *session) incident(ctx context.Context) error {
	results, err := s.independent(ctx, "incident-claude", "incident-codex", "investigate")
	if err != nil {
		return err
	}
	synthesis := results[0].Markdown + "\nIndependent Codex report:\n" + results[1].Markdown
	if _, err := s.stage(ctx, "codex", "incident-synthesis", "final", agent.ReadOnly, synthesis); err != nil {
		return err
	}
	for {
		actions := []decision.Action{decision.Finalize}
		if len(s.latest.MissingEvidence) > 0 && s.remaining().InvestigationRounds > 0 {
			actions = append(actions, decision.Investigate)
		}
		action, err := s.gate(ctx, "diagnosis", actions)
		if err != nil {
			return err
		}
		if action == decision.Finalize {
			if s.latest.Outcome != "completed" || s.safe("diagnosis").State.Requirements.Unsatisfied > 0 {
				return stop("unresolved", "incident synthesis incomplete")
			}
			s.run.Final = s.latest.Markdown
			return nil
		}
		before := s.progressSignature()
		s.run.Budgets.Investigations++
		if _, err := s.stage(ctx, "codex", "incident-diagnostics", "investigate", agent.ReadOnly, s.handoffJSON()); err != nil {
			return err
		}
		if _, err := s.stage(ctx, "codex", "incident-synthesis", "final", agent.ReadOnly, s.handoffJSON()); err != nil {
			return err
		}
		if err := s.progress(before); err != nil {
			return err
		}
	}
}

func (s *session) reviewStage(ctx context.Context, who, stage, role string) (report.StepReport, error) {
	if s.remaining().ReviewPasses == 0 {
		return report.StepReport{}, stop("unresolved", "review pass budget exhausted")
	}
	s.run.Budgets.ReviewPasses++
	s.engine.Events.Emit(ui.Event{Kind: "message", Name: fmt.Sprintf("Code review pass %d/%d", s.run.Budgets.ReviewPasses, s.engine.Config.Review.MaxPasses)})
	r, err := s.stage(ctx, who, stage, role, agent.ReadOnly, s.handoffJSON())
	if err != nil {
		return r, err
	}
	s.run.ReviewedSnapshot = s.run.Current.ID
	s.reviewGeneration = s.generation
	return r, nil
}

// completion is based on current engine facts and acceptance coverage, not prose.
func (s *session) completion() bool {
	state := s.safe("code_review").State
	if s.input.Workflow == "fix_review" && state.Findings.Confirmed > 0 {
		return false
	}
	valid := s.run.Validation.Status == "passed" || s.run.Validation.Status == "not_required"
	return valid && s.run.Validation.Snapshot == s.run.Current.ID && state.SnapshotMatches && state.Requirements.Satisfied > 0 && state.Requirements.Unknown == 0 && state.Requirements.Unsatisfied == 0 && state.Findings.Blocking == 0 && state.Findings.Disputed == 0 && state.Findings.Proposed == 0 && state.Findings.UnverifiedFixes == 0 && !state.MissingEvidence && !state.Disagreements && !state.Questions && s.latest.Outcome == "completed"
}

// resultCoverage allows honest research unknowns while requiring the requested result.
func (s *session) resultCoverage() bool {
	state := s.safe("task_readiness").State
	switch s.input.Workflow {
	case "plan", "architecture", "research":
		point := "answer_review"
		if s.input.Workflow == "plan" {
			point = "plan_review"
		}
		facts := s.safe(point).State
		if facts.Findings.Blocking+facts.Findings.Disputed+facts.Findings.Proposed+facts.Findings.UnverifiedFixes > 0 || facts.Disagreements {
			return false
		}
	case "fix_review":
		if s.run.Budgets.Implementations == 0 {
			facts := s.safe("review_verification").State
			if facts.Findings.UnverifiedFixes > 0 || facts.MissingEvidence || facts.Disagreements {
				return false
			}
		}
	}
	return s.latest.Outcome == "completed" && state.Requirements.Satisfied > 0 && state.Requirements.Unknown == 0 && state.Requirements.Unsatisfied == 0 && !state.Questions && !state.ScopeConflict
}

func (s *session) writing(ctx context.Context) error {
	if s.input.Workflow != "docs" {
		if err := s.engine.Validator.Preflight(ctx, s.input.Project.Root); err != nil {
			return stop("needs_input", err.Error())
		}
	}
	if err := s.implementation(ctx); err != nil {
		return err
	}
	if err := s.validate(ctx); err != nil {
		return err
	}
	if _, err := s.reviewStage(ctx, "codex", "code-review", "code-review"); err != nil {
		return err
	}
	for {
		state := s.safe("code_review").State
		actions := []decision.Action{decision.Stop, decision.Clarify}
		if s.completion() {
			actions = []decision.Action{decision.Complete}
		}
		canFix := s.remaining().FixIterations > 0 && s.remaining().ReviewPasses > 0 && s.remaining().Steps >= 3
		if !s.completion() && (state.Findings.Blocking > 0 || (s.input.Workflow == "fix_review" && state.Findings.Confirmed > 0)) && canFix {
			actions = append(actions, decision.Fix)
		}
		needsVerification := state.Findings.Disputed+state.Findings.Proposed+state.Findings.UnverifiedFixes+state.Requirements.Unknown > 0 || state.MissingEvidence || state.Disagreements
		if needsVerification && s.remaining().ReviewPasses > 0 {
			actions = append(actions, decision.Verify)
		}
		action, err := s.gate(ctx, "code_review", actions)
		if err != nil {
			return err
		}
		if action == decision.Complete {
			s.run.Final = s.latest.Markdown
			return nil
		}
		before := s.progressSignature()
		switch action {
		case decision.Fix:
			if !canFix {
				return stop("unresolved", "review fix budget exhausted")
			}
			s.run.Budgets.ReviewFixes++
			s.engine.Events.Emit(ui.Event{Kind: "message", Name: fmt.Sprintf("Review fixes %d/%d → validation → re-review", s.run.Budgets.ReviewFixes, s.engine.Config.Review.MaxIterations)})
			s.invalidate()
			if _, err := s.stage(ctx, "claude", "review-fixes", "implement", s.writeMode(), s.plan+"\n"+s.handoffJSON()); err != nil {
				return err
			}
			if err := s.validate(ctx); err != nil {
				return err
			}
			if _, err := s.reviewStage(ctx, "codex", "code-re-review", "code-review"); err != nil {
				return err
			}
		case decision.Verify, decision.Investigate:
			if _, err := s.reviewStage(ctx, "codex", "targeted-code-verify", "verify"); err != nil {
				return err
			}
		default:
			return stop("unresolved", "unresolved code review")
		}
		if err := s.progress(before); err != nil {
			return err
		}
	}
}
func (s *session) writeMode() agent.Mode {
	if s.input.Workflow == "docs" {
		return agent.DocsOnly
	}
	return agent.Write
}
func (s *session) invalidate() {
	s.run.Validation.Status = "stale"
	s.run.ReviewedSnapshot = ""
	s.reviewGeneration = -1
}

func (s *session) implementation(ctx context.Context) error {
	for {
		if s.remaining().ImplementationRounds == 0 {
			return stop("unresolved", "implementation round budget exhausted")
		}
		before := s.progressSignature()
		s.run.Budgets.Implementations++
		s.invalidate()
		r, err := s.stage(ctx, "claude", "implementation", "implement", s.writeMode(), s.plan+"\n"+s.handoffJSON())
		if err != nil {
			return err
		}
		state := s.safe("implementation_result").State
		actions := []decision.Action{decision.Clarify}
		if r.Outcome == "completed" {
			actions = append(actions, decision.Validate)
		}
		if r.Outcome == "incomplete" && s.remaining().ImplementationRounds > 0 && !state.ScopeConflict {
			actions = append(actions, decision.ContinueImplementation)
		}
		if r.Diagnosis == "unknown" && s.remaining().InvestigationRounds > 0 {
			actions = append(actions, decision.Investigate)
		}
		action, err := s.gate(ctx, "implementation_result", actions)
		if err != nil {
			return err
		}
		if action == decision.Validate {
			return nil
		}
		if action == decision.Investigate {
			s.run.Budgets.Investigations++
			if _, err := s.stage(ctx, "codex", "implementation-investigation", "investigate", agent.ReadOnly, s.handoffJSON()); err != nil {
				return err
			}
			if err := s.diagnosisGate(ctx, true); err != nil {
				return err
			}
		}
		if err := s.progress(before); err != nil {
			return err
		}
	}
}
