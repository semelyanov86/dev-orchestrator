// Package decision recommends typed transitions; the engine applies policy separately.
package decision

import (
	"context"
	"errors"
	"slices"

	"dev-orchestrator/internal/config"
	"dev-orchestrator/internal/router/jev"
)

type Action string

const (
	Proceed                Action = "proceed"
	Clarify                Action = "clarify"
	Investigate            Action = "investigate"
	Finalize               Action = "finalize"
	RevisePlan             Action = "revise_plan"
	Verify                 Action = "verify"
	Validate               Action = "validate"
	ContinueImplementation Action = "continue_implementation"
	Fix                    Action = "fix"
	Complete               Action = "complete"
	Stop                   Action = "stop"
	ReviseAnswer           Action = "revise_answer"
)

type Counts struct {
	Satisfied   int `json:"satisfied"`
	Unsatisfied int `json:"unsatisfied"`
	Unknown     int `json:"unknown"`
}
type FindingCounts struct {
	Confirmed       int `json:"confirmed"`
	Blocking        int `json:"blocking"`
	Disputed        int `json:"disputed"`
	Proposed        int `json:"proposed"`
	UnverifiedFixes int `json:"unverified_fixes"`
}
type Remaining struct {
	FixIterations        int `json:"fix_iterations"`
	ReviewPasses         int `json:"review_passes"`
	ValidationRepairs    int `json:"validation_repairs"`
	PlanRevisions        int `json:"plan_revisions"`
	InvestigationRounds  int `json:"investigation_rounds"`
	AnswerRevisions      int `json:"answer_revisions"`
	ImplementationRounds int `json:"implementation_rounds"`
	Steps                int `json:"steps"`
	Requests             int `json:"requests"`
}

// SafeState intentionally has no free-text summaries, paths, commands, logs or evidence content.
type SafeState struct {
	Workflow        string        `json:"workflow"`
	Stage           string        `json:"stage"`
	Scope           string        `json:"scope"`
	Outcome         string        `json:"outcome"`
	Requirements    Counts        `json:"requirements"`
	Findings        FindingCounts `json:"findings"`
	Categories      []string      `json:"issue_categories"`
	MissingEvidence bool          `json:"missing_evidence"`
	Disagreements   bool          `json:"disagreements"`
	Questions       bool          `json:"questions"`
	ScopeConflict   bool          `json:"scope_conflict"`
	Validation      string        `json:"validation"`
	SnapshotMatches bool          `json:"review_matches_current_snapshot"`
	Progress        string        `json:"progress"`
	Diagnosis       string        `json:"diagnosis"`
	FailureClass    string        `json:"failure_class"`
	Remaining       Remaining     `json:"remaining"`
}
type DecisionInput struct {
	Point          string    `json:"decision_point"`
	State          SafeState `json:"state"`
	AllowedActions []Action  `json:"allowed_actions"`
}
type StepDecision struct {
	Action                 Action        `json:"recommended_action"`
	Applied                Action        `json:"applied_action"`
	Source                 string        `json:"source"`
	Reason                 string        `json:"reason"`
	FallbackReason         string        `json:"fallback_reason,omitempty"`
	Telemetry              *jev.Response `json:"typed_response,omitempty"`
	Threshold              float64       `json:"threshold,omitempty"`
	RejectedRecommendation Action        `json:"rejected_recommendation,omitempty"`
	RequestedModel         string        `json:"requested_model,omitempty"`
}
type DecisionService interface {
	Decide(context.Context, DecisionInput) (StepDecision, error)
}

// LocalDecisionService never calls another model to replace JEV.
type LocalDecisionService struct{}

func (LocalDecisionService) Decide(ctx context.Context, input DecisionInput) (StepDecision, error) {
	if err := ctx.Err(); err != nil {
		return StepDecision{}, err
	}
	choose := func(actions ...Action) StepDecision {
		for _, a := range actions {
			if slices.Contains(input.AllowedActions, a) {
				return StepDecision{Action: a, Source: "local", Reason: "verified report and engine prerequisites"}
			}
		}
		return StepDecision{Action: Stop, Source: "local", Reason: "no safe transition"}
	}
	s := input.State
	if s.Questions || s.ScopeConflict || s.Outcome == "needs_input" {
		return choose(Clarify, Stop), nil
	}
	switch input.Point {
	case "task_readiness":
		return choose(Proceed, Clarify), nil
	case "diagnosis":
		if s.MissingEvidence || s.Disagreements || s.Diagnosis == "unknown" {
			return choose(Investigate, Finalize, Clarify, Stop), nil
		}
		return choose(Proceed, Finalize, Clarify), nil
	case "plan_review":
		if s.Findings.Disputed > 0 || s.Findings.Proposed > 0 || s.MissingEvidence {
			return choose(Verify, Clarify, Stop), nil
		}
		if s.Findings.Blocking > 0 || s.Requirements.Unsatisfied > 0 {
			return choose(RevisePlan, Clarify, Stop), nil
		}
		return choose(Proceed, Verify, Clarify), nil
	case "implementation_result":
		if s.Outcome == "incomplete" || s.Requirements.Unsatisfied > 0 {
			return choose(ContinueImplementation, Investigate, Clarify, Stop), nil
		}
		return choose(Validate, Investigate, Clarify), nil
	case "validation_failure":
		if s.FailureClass == "current_change" {
			return choose(Fix, Stop), nil
		}
		return choose(Investigate, Stop, Clarify), nil
	case "code_review":
		if slices.Contains(input.AllowedActions, Complete) {
			return choose(Complete), nil
		}
		if s.Findings.Disputed > 0 || s.Findings.Proposed > 0 || s.Findings.UnverifiedFixes > 0 {
			return choose(Verify, Investigate, Clarify, Stop), nil
		}
		if s.Findings.Blocking > 0 {
			return choose(Fix, Stop), nil
		}
		return choose(Verify, Investigate, Clarify, Stop), nil
	case "answer_review":
		if s.Findings.Disputed+s.Findings.Proposed+s.Findings.UnverifiedFixes > 0 || s.Requirements.Unknown > 0 {
			return choose(Investigate, ReviseAnswer, Clarify, Stop), nil
		}
		if s.Requirements.Satisfied > 0 && s.Requirements.Unsatisfied == 0 && s.Findings.Blocking == 0 && !s.Disagreements && s.Outcome == "completed" {
			return choose(Finalize, Investigate, Clarify), nil
		}
		if s.Findings.Blocking > 0 || s.Requirements.Unsatisfied > 0 {
			return choose(ReviseAnswer, Investigate, Clarify, Stop), nil
		}
		if s.MissingEvidence || s.Disagreements {
			return choose(Investigate, Finalize, Clarify, Stop), nil
		}
		return choose(Finalize, ReviseAnswer, Clarify), nil
	case "review_verification":
		if s.Findings.Disputed+s.Findings.Proposed+s.Findings.UnverifiedFixes > 0 || s.MissingEvidence {
			return choose(Verify, Finalize, Clarify), nil
		}
		return choose(Finalize, Clarify), nil
	default:
		return StepDecision{}, errors.New("unknown decision point")
	}
}

type JevDecisionService struct {
	Client   *jev.Client
	Config   config.Decisions
	Local    LocalDecisionService
	Disabled bool
}

func (j JevDecisionService) Decide(ctx context.Context, input DecisionInput) (StepDecision, error) {
	if err := ctx.Err(); err != nil {
		return StepDecision{}, err
	}
	if len(input.AllowedActions) == 0 {
		return StepDecision{}, errors.New("empty action policy")
	}
	if len(input.AllowedActions) == 1 {
		return StepDecision{Action: input.AllowedActions[0], Source: "policy", Reason: "one mandatory transition"}, nil
	}
	if j.Disabled || j.Config.Type == "local" {
		return j.fallback(ctx, input, "disabled")
	}
	criteria := map[string]string{}
	choices := make([]string, len(input.AllowedActions))
	for i, a := range input.AllowedActions {
		criteria[string(a)] = string(a)
		choices[i] = string(a)
	}
	response, err := j.Client.Ask(ctx, map[string]jev.Question{
		"next_action":         {Type: "choice", Instructions: "Choose the next useful transition from the allowed actions. Never assume completion from agent claims alone.", Criteria: criteria},
		"evidence_sufficient": {Type: "noul", Instructions: "Are the normalized evidence and prerequisites sufficient for completion or a writing transition?"},
	}, input)
	if err != nil {
		return j.rejected(ctx, input, err.Error(), response)
	}
	threshold, ok := j.Config.Confidence[input.Point]
	if !ok {
		return j.rejected(ctx, input, "missing_threshold", response)
	}
	action, err := jev.Choice(response.Answers["next_action"], choices, threshold)
	if err != nil {
		return j.rejected(ctx, input, err.Error(), response)
	}
	evidence, err := jev.Noul(response.Answers["evidence_sufficient"], j.Config.NoulNo, j.Config.NoulYes)
	if err != nil {
		return j.rejected(ctx, input, err.Error(), response)
	}
	needsEvidence := slices.Contains([]Action{Complete, Proceed, Finalize, Fix, Validate}, Action(action))
	if needsEvidence && !evidence {
		return j.rejected(ctx, input, "contradictory_evidence_answer", response)
	}
	return StepDecision{Action: Action(action), Source: "jev", RequestedModel: j.Client.Model, Reason: "typed recommendation passed thresholds; engine policy still required", Telemetry: &response, Threshold: threshold}, nil
}

func (j JevDecisionService) rejected(ctx context.Context, input DecisionInput, reason string, response jev.Response) (StepDecision, error) {
	d, err := j.fallback(ctx, input, reason)
	d.Telemetry = &response
	d.RequestedModel = j.Client.Model
	d.Threshold = j.Config.Confidence[input.Point]
	d.RejectedRecommendation = Action(response.Answers["next_action"].Choice)
	return d, err
}
func (j JevDecisionService) fallback(ctx context.Context, input DecisionInput, reason string) (StepDecision, error) {
	if ctx.Err() != nil {
		return StepDecision{}, ctx.Err()
	}
	d, err := j.Local.Decide(ctx, input)
	d.FallbackReason = reason
	return d, err
}

// Allowed checks membership without permitting arbitrary model-generated commands.
func Allowed(input DecisionInput, d StepDecision) bool {
	return slices.Contains(input.AllowedActions, d.Action)
}
