package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dev-orchestrator/internal/agent"
	"dev-orchestrator/internal/decision"
	"dev-orchestrator/internal/project"
	"dev-orchestrator/internal/ui"
	"dev-orchestrator/internal/validation"
)

func (s *session) validate(ctx context.Context) error {
	for {
		if err := s.updates(); err != nil {
			return err
		}
		before, err := s.engine.Inspector.Snapshot(ctx, s.input.Project.Root)
		if err != nil {
			return err
		}
		required := s.validationRequired(before)
		if !required {
			s.run.Validation = validation.Result{Status: "not_required", Snapshot: before.ID, Reason: "docs-only paths without executable examples; project instructions do not require validation"}
			s.engine.Events.Emit(ui.Event{Kind: "message", Name: "Validation", Actor: "task all", Status: "not_required", Message: s.run.Validation.Reason})
			return s.persist()
		}
		if err := s.engine.Validator.Preflight(ctx, s.input.Project.Root); err != nil {
			return stop("needs_input", err.Error())
		}
		if len(s.run.Steps) >= s.engine.Config.Workflow.MaxSteps {
			return stop("unresolved", "step budget exhausted before validation")
		}
		id := fmt.Sprintf("%02d-validation", len(s.run.Steps)+1)
		started := time.Now()
		s.engine.Events.Emit(ui.Event{Kind: "start", ID: id, Name: "Validation", Actor: strings.Join(s.engine.Config.Validation.Command, " "), Access: "local validation"})
		result, runErr := s.engine.Validator.Validate(ctx, s.input.Project.Root)
		after, snapErr := s.engine.Inspector.Snapshot(context.WithoutCancel(ctx), s.input.Project.Root)
		result.Snapshot = after.ID
		result.Output = s.engine.Store.Cleaner.Clean(result.Output)
		if runErr != nil || result.ExitCode != 0 {
			result.Status = "failed"
		} else {
			result.Status = "passed"
		}
		if ctx.Err() != nil {
			result.Status = "cancelled"
		} else if snapErr != nil || before.ID != after.ID {
			result.Status = "stale"
		}
		s.run.Validation = result
		s.run.Current = after
		record := Step{ID: id, Stage: "validation", Agent: "validator", Access: agent.Write, Started: started, Finished: time.Now(), ExitCode: result.ExitCode, Outcome: result.Status, Before: before.ID, After: after.ID, Changed: []string{}}
		record.Duration = record.Finished.Sub(started)
		s.run.Steps = append(s.run.Steps, record)
		if err := errors.Join(s.engine.Store.Write("steps/"+id+"/validation.log", result.Output), s.engine.Store.Write("validation.log", result.Output), s.engine.Store.WriteJSON("steps/"+id+"/record.json", record), s.persist()); err != nil {
			return err
		}
		excerpt := result.Output
		if len(excerpt) > 800 {
			excerpt = excerpt[len(excerpt)-800:]
		}
		s.engine.Events.Emit(ui.Event{Kind: "finish", ID: id, Name: "Validation", Actor: strings.Join(s.engine.Config.Validation.Command, " "), Status: result.Status, Message: record.Duration.Round(time.Second).String()})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if snapErr != nil {
			return snapErr
		}
		if result.Status == "stale" {
			return stop("unresolved", "project changed during validation; result is stale")
		}
		if result.Status == "passed" {
			return nil
		}
		s.engine.Events.Emit(ui.Event{Kind: "error", Name: "Validation failed", Message: excerpt + "\nOutput: " + filepath.Join(s.engine.Store.Dir, "steps", id, "validation.log")})
		// First failure is unknown. A read-only investigator must show attribution evidence.
		if s.remaining().InvestigationRounds == 0 {
			return stop("unresolved", "validation failed; no safe attribution budget")
		}
		s.run.Budgets.Investigations++
		if _, err := s.stage(ctx, "codex", "validation-failure-investigation", "investigate", agent.ReadOnly, "Actual validation output:\n"+result.Output+"\n"+s.handoffJSON()); err != nil {
			return err
		}
		actions := []decision.Action{decision.Stop, decision.Clarify}
		facts := s.safe("validation_failure").State
		attributed := s.latest.Outcome == "completed" && s.latest.FailureClass == "current_change" && s.attributedFailure(after) && !facts.MissingEvidence && !facts.Disagreements && !facts.Questions && !facts.ScopeConflict && facts.Findings.Disputed+facts.Findings.Proposed == 0 && facts.Requirements.Unknown == 0
		if attributed && s.remaining().ValidationRepairs > 0 && s.remaining().ReviewPasses > 0 && s.remaining().Steps >= 3 {
			actions = append(actions, decision.Fix)
		}
		action, err := s.gate(ctx, "validation_failure", actions)
		if err != nil {
			return err
		}
		if action != decision.Fix {
			return stop("unresolved", "validation failure is not proven to be caused by this run")
		}
		s.run.Budgets.ValidationRepairs++
		s.invalidate()
		s.engine.Events.Emit(ui.Event{Kind: "message", Name: fmt.Sprintf("Validation repair %d/%d", s.run.Budgets.ValidationRepairs, s.engine.Config.ValidationRepair.MaxIterations)})
		if _, err := s.stage(ctx, "claude", "validation-repair", "implement", s.writeMode(), s.plan+"\n"+s.handoffJSON()); err != nil {
			return err
		}
	}
}

func (s *session) attributedFailure(current project.Snapshot) bool {
	changed := project.Changed(s.run.Initial, current)
	for _, q := range s.latest.Requirements {
		for _, e := range q.Evidence {
			if e.Kind == "file" {
				path := strings.Split(e.Reference, ":")[0]
				for _, name := range changed {
					if name == path {
						return true
					}
				}
			}
		}
	}
	for _, f := range s.latest.Findings {
		for _, e := range f.Evidence {
			if e.Kind == "file" {
				path := strings.Split(e.Reference, ":")[0]
				for _, name := range changed {
					if name == path {
						return true
					}
				}
			}
		}
	}
	return false
}

func (s *session) validationRequired(current project.Snapshot) bool {
	if s.input.Workflow != "docs" {
		return true
	}
	instructions := strings.ToLower(s.input.Project.Instructions)
	if strings.Contains(instructions, "task all") {
		return true
	}
	for _, path := range project.Changed(s.run.Initial, current) {
		if !inScope(path, s.input.DocsPaths) {
			return true
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".md", ".rst", ".txt", ".adoc":
		default:
			return true
		}
		b, err := os.ReadFile(filepath.Join(s.input.Project.Root, path))
		if err == nil && (strings.Contains(string(b), "```") || strings.Contains(string(b), ".. code-block::")) {
			return true
		}
	}
	return false
}
