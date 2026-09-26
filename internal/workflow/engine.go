// Package workflow runs deterministic role sequences and bounded decision gates.
package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"dev-orchestrator/internal/agent"
	"dev-orchestrator/internal/artifacts"
	"dev-orchestrator/internal/config"
	"dev-orchestrator/internal/decision"
	"dev-orchestrator/internal/project"
	"dev-orchestrator/internal/report"
	"dev-orchestrator/internal/ui"
	"dev-orchestrator/internal/validation"
	"dev-orchestrator/prompts"
)

type Validator interface {
	Preflight(context.Context, string) error
	Validate(context.Context, string) (validation.Result, error)
}
type Snapshotter interface {
	Snapshot(context.Context, string) (project.Snapshot, error)
	Diff(context.Context, string, string) (string, error)
}
type RemoteExecutor interface {
	Investigate(context.Context, config.Server) (string, error)
}
type RequestCounter interface{ Used() int }
type Input struct {
	Project            project.Project
	Workflow           string
	Task               string
	Imported           string
	ImportedPath       string
	DocsPaths          []string
	Server             string
	Router             string
	Risk               string
	PlanReviewRequired bool
	ParallelRequired   bool
	RouteConfidence    *float64
	Complexity         string
	RoutingProvider    string
	RoutingModel       string
}

// Update does not implicitly increase scope. Restrictions stop the active subprocess immediately.
type Update struct {
	Text     string        `json:"text"`
	Kind     string        `json:"kind"`
	At       time.Time     `json:"at"`
	Accepted chan struct{} `json:"-"`
}
type Step struct {
	ID       string             `json:"step_id"`
	Stage    string             `json:"stage"`
	Agent    string             `json:"agent"`
	Access   agent.Mode         `json:"access"`
	Started  time.Time          `json:"started_at"`
	Finished time.Time          `json:"finished_at"`
	Duration time.Duration      `json:"duration"`
	ExitCode int                `json:"exit_code"`
	Outcome  string             `json:"outcome"`
	Before   string             `json:"before_snapshot"`
	After    string             `json:"after_snapshot"`
	Changed  []string           `json:"changed_paths"`
	Report   *report.StepReport `json:"report,omitempty"`
	Reason   string             `json:"reason,omitempty"`
	snapshot project.Snapshot
}
type Budgets struct {
	ReviewFixes       int `json:"review_fixes"`
	ReviewPasses      int `json:"review_passes"`
	ValidationRepairs int `json:"validation_repairs"`
	PlanRevisions     int `json:"plan_revisions"`
	Investigations    int `json:"investigations"`
	AnswerRevisions   int `json:"answer_revisions"`
	Implementations   int `json:"implementations"`
	NoProgress        int `json:"no_progress_rounds"`
	Requests          int `json:"requests"`
}
type Run struct {
	ID               string               `json:"run_id"`
	Project          string               `json:"project"`
	Workflow         string               `json:"workflow"`
	Router           string               `json:"router"`
	RouteConfidence  *float64             `json:"routing_confidence,omitempty"`
	Complexity       string               `json:"complexity,omitempty"`
	RoutingProvider  string               `json:"routing_provider,omitempty"`
	RoutingModel     string               `json:"routing_model,omitempty"`
	Scope            string               `json:"effective_scope"`
	Started          time.Time            `json:"started_at"`
	Finished         time.Time            `json:"finished_at"`
	Outcome          string               `json:"outcome"`
	Reason           string               `json:"termination_reason"`
	ExitCode         int                  `json:"exit_code"`
	Steps            []Step               `json:"steps"`
	Agents           []string             `json:"agents"`
	Decisions        []string             `json:"decision_ids"`
	Updates          []Update             `json:"user_updates"`
	Criteria         []report.Requirement `json:"acceptance_criteria"`
	Findings         []report.Finding     `json:"findings"`
	Questions        []string             `json:"pending_questions"`
	Initial          project.Snapshot     `json:"initial_snapshot"`
	Current          project.Snapshot     `json:"current_snapshot"`
	ReviewedSnapshot string               `json:"reviewed_snapshot"`
	Validation       validation.Result    `json:"validation"`
	Budgets          Budgets              `json:"consumed_budgets"`
	Remaining        decision.Remaining   `json:"remaining_budgets"`
	Final            string               `json:"final"`
	Artifacts        string               `json:"artifacts"`
}
type Engine struct {
	Config    config.Config
	Claude    agent.Agent
	Codex     agent.Agent
	Decisions decision.DecisionService
	Validator Validator
	Inspector Snapshotter
	Remote    RemoteExecutor
	Events    ui.Sink
	Store     *artifacts.Store
	Requests  RequestCounter
	Updates   <-chan Update
	Ask       func(context.Context, string) (string, error)
}
type session struct {
	engine           *Engine
	input            Input
	run              Run
	mu               sync.Mutex
	criteria         map[string]report.Requirement
	findings         map[string]report.Finding
	latest           report.StepReport
	plan             string
	handoff          string
	remote           string
	generation       int
	planGeneration   int
	reviewGeneration int
	lastGeneration   int
	pending          []Update
	cancelStage      context.CancelFunc
	stageWriting     bool
	runCancel        context.CancelFunc
	stopMonitor      chan struct{}
	monitorDone      chan struct{}
}
type halt struct{ outcome, reason string }

var errReplan = errors.New("user correction requires a fresh plan")

func (h halt) Error() string            { return h.reason }
func stop(outcome, reason string) error { return halt{outcome: outcome, reason: reason} }

func ExitCode(outcome string) int {
	switch outcome {
	case "success":
		return 0
	case "needs_input":
		return 2
	case "cancelled":
		return 130
	default:
		return 1
	}
}

// Interruption records the signal-specific exit status in persisted run metadata.
type Interruption struct{ Status int }

func (i Interruption) Error() string { return "interrupted" }

func ExitCodeContext(ctx context.Context, outcome string) int {
	var interrupted Interruption
	if outcome == "cancelled" && errors.As(context.Cause(ctx), &interrupted) && (interrupted.Status == 130 || interrupted.Status == 143) {
		return interrupted.Status
	}
	return ExitCode(outcome)
}

// Run saves completed and partial work on every terminal path.
func (e *Engine) Run(ctx context.Context, input Input) (Run, error) {
	if _, ok := Lookup(input.Workflow); !ok {
		return Run{}, errors.New("invalid workflow")
	}
	ctx, cancel := context.WithTimeout(ctx, e.Config.Workflow.Timeout)
	defer cancel()
	d, _ := Lookup(input.Workflow)
	s := &session{engine: e, input: input, criteria: map[string]report.Requirement{}, findings: map[string]report.Finding{}, stopMonitor: make(chan struct{}), monitorDone: make(chan struct{})}
	s.runCancel = cancel
	s.run = Run{ID: e.Store.ID, Project: input.Project.Root, Workflow: input.Workflow, Router: input.Router, Scope: d.Access, Started: time.Now(), Outcome: "running", Initial: input.Project.Initial, Current: input.Project.Initial, Artifacts: e.Store.Dir, Steps: []Step{}, Decisions: []string{}, Updates: []Update{}, Criteria: []report.Requirement{}, Findings: []report.Finding{}, Questions: []string{}, Agents: []string{}}
	s.run.RouteConfidence = input.RouteConfidence
	s.run.Complexity = input.Complexity
	s.run.RoutingProvider = input.RoutingProvider
	s.run.RoutingModel = input.RoutingModel
	if !Writing(input.Workflow) {
		s.run.Validation = validation.Result{Status: "not_required", Snapshot: input.Project.Initial.ID, Reason: "read-only workflow"}
	}
	go s.monitor(ctx)
	err := e.Store.Write("task.md", input.Task)
	if err == nil && input.Imported != "" {
		err = e.Store.Write("imported.md", input.Imported)
	}
	if err == nil {
		err = s.persist()
	}
	if err == nil {
		err = s.preflight(ctx)
	}
	if err == nil {
		for {
			err = s.execute(ctx)
			if !errors.Is(err, errReplan) {
				break
			}
			if s.remaining().PlanRevisions == 0 {
				err = stop("unresolved", "correction replan budget exhausted")
				break
			}
			s.run.Budgets.PlanRevisions++
		}
	}
	close(s.stopMonitor)
	<-s.monitorDone
	if updateErr := s.updates(); updateErr != nil {
		err = updateErr
	}
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if snapshot, snapErr := e.Inspector.Snapshot(context.WithoutCancel(ctx), input.Project.Root); snapErr == nil {
		if err == nil && snapshot.ID != s.run.Current.ID {
			err = stop("unresolved", "project changed before final completion")
		}
		s.run.Current = snapshot
	} else {
		err = errors.Join(err, fmt.Errorf("final snapshot unavailable: %w", snapErr))
	}
	if err == nil && (s.lastGeneration != s.generation || !s.resultCoverage()) {
		err = stop("unresolved", "final acceptance coverage became stale or incomplete")
	}
	if err == nil && Writing(input.Workflow) && s.run.Budgets.Implementations > 0 && !s.completion() {
		err = stop("unresolved", "final completion prerequisites became stale")
	}
	s.run.Outcome = "success"
	s.run.Reason = "completion predicates satisfied"
	if err != nil {
		var h halt
		switch {
		case errors.As(err, &h):
			s.run.Outcome = h.outcome
			s.run.Reason = h.reason
		case errors.Is(err, context.Canceled):
			s.run.Outcome = "cancelled"
			s.run.Reason = "cancelled"
		case errors.Is(err, context.DeadlineExceeded):
			s.run.Outcome = "failed"
			s.run.Reason = "deadline exceeded"
		default:
			s.run.Outcome = "failed"
			s.run.Reason = err.Error()
		}
	}
	s.run.Finished = time.Now()
	s.run.ExitCode = ExitCodeContext(ctx, s.run.Outcome)
	diff, diffErr := e.Inspector.Diff(context.WithoutCancel(ctx), input.Project.Root, input.Project.MergeBase)
	if diffErr == nil {
		if relative, ok := e.Inspector.(interface {
			RunDiff(context.Context, project.Project, project.Snapshot) (string, error)
		}); ok {
			diff, diffErr = relative.RunDiff(context.WithoutCancel(ctx), input.Project, s.run.Current)
		}
		if diffErr == nil {
			diffErr = e.Store.Write("diff.patch", diff)
		}
	}
	s.run.Questions = append(s.run.Questions, s.latest.UnresolvedQuestions...)
	s.run.Final = s.run.FinalOrFallback()
	s.refresh()
	writeErr := errors.Join(e.Store.Write("final.md", s.run.Final), s.persist(), diffErr)
	if writeErr != nil {
		s.run.Outcome = "failed"
		s.run.ExitCode = 1
		s.run.Reason = "artifacts incomplete: " + writeErr.Error()
		err = errors.Join(err, writeErr)
		_ = s.persist()
	}
	return s.run, err
}

func (r Run) FinalOrFallback() string {
	if r.Final != "" {
		return r.Final
	}
	return fmt.Sprintf("Outcome: %s\nПричина: %s\nВыполнено этапов: %d\nArtifacts: %s", r.Outcome, r.Reason, len(r.Steps), r.Artifacts)
}

func (s *session) monitor(ctx context.Context) {
	defer close(s.monitorDone)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopMonitor:
			return
		case u, ok := <-s.engine.Updates:
			if !ok {
				return
			}
			s.mu.Lock()
			s.pending = append(s.pending, u)
			if u.Accepted != nil {
				close(u.Accepted)
			}
			if u.Kind == "cancel" {
				s.runCancel()
			}
			if u.Kind == "restrict" && s.stageWriting && s.cancelStage != nil {
				s.cancelStage()
			}
			s.mu.Unlock()
		}
	}
}

func (s *session) updates() error {
	s.mu.Lock()
	updates := s.pending
	s.pending = nil
	s.mu.Unlock()
	for _, u := range updates {
		if u.At.IsZero() {
			u.At = time.Now()
		}
		s.run.Updates = append(s.run.Updates, u)
		if u.Kind == "cancel" {
			return stop("cancelled", "user cancelled run")
		}
		if u.Kind == "question" {
			s.engine.Events.Emit(ui.Event{Kind: "message", Name: "Вопрос сохранён; текущая задача продолжается", Message: u.Text})
			continue
		}
		if u.Kind != "correction" && u.Kind != "restrict" {
			return stop("needs_input", "scope expansion is not supported within this run")
		}
		s.input.Task += "\nUSER UPDATE:\n" + u.Text
		s.generation++
		s.planGeneration = -1
		s.reviewGeneration = -1
		s.run.Validation.Status = "stale"
		for id, q := range s.criteria {
			q.Status = "unknown"
			s.criteria[id] = q
		}
		s.engine.Events.Emit(ui.Event{Kind: "message", Name: "Уточнение принято на границе этапа", Message: "Plan/review/validation prerequisites требуют повторной проверки"})
		if u.Kind == "restrict" {
			return stop("needs_input", "restriction accepted; inspect completed work before choosing a new restricted run")
		}
	}
	return nil
}

func (s *session) preflight(ctx context.Context) error {
	d, _ := Lookup(s.input.Workflow)
	if strings.TrimSpace(s.input.Task) == "" {
		return stop("needs_input", "provide task scope and acceptance criteria")
	}
	if (d.Input == "plan" || d.Input == "report") && s.input.Imported == "" {
		return stop("needs_input", "provide --"+d.Input+" local artifact")
	}
	if d.Input == "server" && s.input.Server == "" {
		return stop("needs_input", "provide --server profile")
	}
	if s.input.Workflow == "docs" && len(s.input.DocsPaths) == 0 {
		return stop("needs_input", "provide explicit documentation paths with --docs-path")
	}
	if s.input.Server != "" {
		server, ok := s.engine.Config.Servers[s.input.Server]
		if !ok {
			return stop("needs_input", "unknown server profile")
		}
		s.engine.Events.Emit(ui.Event{Kind: "start", ID: "remote", Name: "Диагностика сервера", Actor: "SSH", Access: "remote_read_only"})
		output, err := s.engine.Remote.Investigate(ctx, server)
		s.remote = s.engine.Store.Cleaner.Clean(output)
		writeErr := s.engine.Store.Write("remote-investigation.md", s.remote)
		status := "completed"
		if err != nil {
			status = "failed"
		}
		s.engine.Events.Emit(ui.Event{Kind: "finish", ID: "remote", Name: "Диагностика сервера", Status: status})
		if err != nil || writeErr != nil {
			return errors.Join(err, writeErr)
		}
	}
	if Writing(s.input.Workflow) && !slices.Contains([]string{"docs", "bug", "server_bug"}, s.input.Workflow) {
		if err := s.engine.Validator.Preflight(ctx, s.input.Project.Root); err != nil {
			return stop("needs_input", err.Error())
		}
	}
	return nil
}

func (s *session) refresh() {
	s.run.Criteria = s.run.Criteria[:0]
	for _, q := range s.criteria {
		s.run.Criteria = append(s.run.Criteria, q)
	}
	slices.SortFunc(s.run.Criteria, func(a, b report.Requirement) int { return strings.Compare(a.ID, b.ID) })
	s.run.Findings = s.run.Findings[:0]
	for _, f := range s.findings {
		s.run.Findings = append(s.run.Findings, f)
	}
	slices.SortFunc(s.run.Findings, func(a, b report.Finding) int { return strings.Compare(a.ID, b.ID) })
	if s.engine.Requests != nil {
		s.run.Budgets.Requests = s.engine.Requests.Used()
	}
	s.run.Remaining = s.remaining()
}
func (s *session) persist() error { s.refresh(); return s.engine.Store.WriteJSON("run.json", s.run) }
func (s *session) remaining() decision.Remaining {
	c := s.engine.Config
	b := s.run.Budgets
	return decision.Remaining{FixIterations: max(0, c.Review.MaxIterations-b.ReviewFixes), ReviewPasses: max(0, c.Review.MaxPasses-b.ReviewPasses), ValidationRepairs: max(0, c.ValidationRepair.MaxIterations-b.ValidationRepairs), PlanRevisions: max(0, c.Workflow.MaxPlanRevisions-b.PlanRevisions), InvestigationRounds: max(0, c.Workflow.MaxInvestigationRounds-b.Investigations), AnswerRevisions: max(0, c.Workflow.MaxAnswerRevisions-b.AnswerRevisions), ImplementationRounds: max(0, c.Workflow.MaxImplementationRounds-b.Implementations), Steps: max(0, c.Workflow.MaxSteps-len(s.run.Steps)), Requests: max(0, c.Decisions.MaxRequests-b.Requests)}
}

func (s *session) stage(ctx context.Context, who, stage, role string, mode agent.Mode, extra string) (report.StepReport, error) {
	if err := s.updates(); err != nil {
		return report.StepReport{}, err
	}
	if mode != agent.ReadOnly && s.planGeneration != s.generation {
		return report.StepReport{}, errReplan
	}
	step, err := s.prepare(ctx, who, stage, role, mode, extra)
	if err != nil {
		return report.StepReport{}, err
	}
	result, record, err := s.perform(ctx, step)
	s.run.Steps[step.index] = record
	if err == nil && result.ReportError != "" {
		if step.mode != agent.ReadOnly {
			s.run.Validation.Status = "stale"
			s.run.ReviewedSnapshot = ""
		}
		repair, repairErr := s.prepare(ctx, who, stage+"-format-repair", "repair", agent.ReadOnly, result.Output+"\nFormat error: "+result.ReportError)
		if repairErr != nil {
			return report.StepReport{}, repairErr
		}
		fixed, repairRecord, repairErr := s.perform(ctx, repair)
		s.run.Steps[repair.index] = repairRecord
		if repairErr != nil {
			return report.StepReport{}, repairErr
		}
		if fixed.ReportError != "" {
			return report.StepReport{}, errors.New("bounded report format repair failed: " + fixed.ReportError)
		}
		result = fixed
	}
	if err != nil {
		return result.Report, err
	}
	s.run.Current = recordSnapshot(record, s.run.Current)
	if err := s.checkEvidence(result.Report); err != nil {
		return result.Report, err
	}
	reviewer := mode == agent.ReadOnly && !strings.Contains(stage, "format-repair") && (strings.Contains(stage, "review") || strings.Contains(stage, "verify") || strings.Contains(stage, "final") || strings.Contains(stage, "synthesis"))
	s.merge(result.Report, reviewer)
	s.latest = result.Report
	s.lastGeneration = s.generation
	s.handoff = result.Report.Markdown
	if result.Report.Outcome == "needs_input" || len(result.Report.ScopeChanges) > 0 {
		_, err := s.gate(ctx, "task_readiness", []decision.Action{decision.Clarify})
		if err != nil {
			return result.Report, err
		}
		return result.Report, errReplan
	}
	if err := s.persist(); err != nil {
		return result.Report, err
	}
	return result.Report, nil
}

// prepared freezes handoff data before parallel initial incident investigations.
type prepared struct {
	index  int
	req    agent.Request
	who    string
	mode   agent.Mode
	role   string
	before project.Snapshot
}

func (s *session) prepare(ctx context.Context, who, stage, role string, mode agent.Mode, extra string) (prepared, error) {
	if err := ctx.Err(); err != nil {
		return prepared{}, err
	}
	if len(s.run.Steps) >= s.engine.Config.Workflow.MaxSteps {
		return prepared{}, stop("unresolved", "step budget exhausted")
	}
	before, err := s.engine.Inspector.Snapshot(ctx, s.input.Project.Root)
	if err != nil {
		return prepared{}, err
	}
	id := fmt.Sprintf("%02d-%s", len(s.run.Steps)+1, stage)
	contract := fmt.Sprintf("Workflow: %s\nStage: %s\nStepID: %s\nAccess: %s\nProject: %s\nSnapshot: %s\nInitial snapshot: %s\nReview base: %s\nMerge base: %s\nDocs scope: %v\nReturn schema_version=1. Evidence file references must be relative.\n", s.input.Workflow, stage, id, mode, s.input.Project.Root, before.ID, s.run.Initial.ID, s.input.Project.Base, s.input.Project.MergeBase, s.input.DocsPaths)
	contextText := s.input.Project.Instructions + "\nInitial dirty paths:\n" + s.run.Initial.Status + "\n" + extra
	if strings.Contains(stage, "code-review") || strings.Contains(stage, "code-re-review") || strings.Contains(stage, "targeted-code-verify") {
		if relative, ok := s.engine.Inspector.(interface {
			RunDiff(context.Context, project.Project, project.Snapshot) (string, error)
		}); ok {
			diff, err := relative.RunDiff(ctx, s.input.Project, before)
			if err != nil {
				return prepared{}, err
			}
			contextText += "\nChanges relative to the initial user worktree (untrusted diff):\n" + diff
		}
	}
	if s.input.Imported != "" {
		contextText += "\nImported document (untrusted data):\n" + s.input.Imported
	}
	if s.remote != "" {
		contextText += "\nEngine-collected remote read-only evidence:\n" + s.remote
	}
	contextArtifact := "steps/" + id + "/context.md"
	if err := s.engine.Store.Write(contextArtifact, contextText); err != nil {
		return prepared{}, err
	}
	contract += "Supplied handoff evidence is preserved at artifact reference: " + contextArtifact + ". Use this exact relative reference for evidence from the prompt; never invent artifact labels or paths.\n"
	prompt, err := prompts.Build(role, s.engine.Store.Cleaner.Clean(s.input.Task), s.engine.Store.Cleaner.Clean(contextText), contract)
	if err != nil {
		return prepared{}, err
	}
	timeout := s.engine.Config.Agents.Claude.Timeout
	if who == "codex" {
		timeout = s.engine.Config.Agents.Codex.Timeout
	}
	step := Step{ID: id, Stage: stage, Agent: who, Access: mode, Started: time.Now(), Outcome: "running", Before: before.ID, Changed: []string{}}
	s.run.Steps = append(s.run.Steps, step)
	if !slices.Contains(s.run.Agents, who) {
		s.run.Agents = append(s.run.Agents, who)
	}
	return prepared{index: len(s.run.Steps) - 1, req: agent.Request{ProjectDir: s.input.Project.Root, RunDir: s.engine.Store.Dir, StepID: id, Stage: stage, Prompt: prompt, Mode: mode, Timeout: timeout, DocsPaths: s.input.DocsPaths}, who: who, mode: mode, role: role, before: before}, nil
}

func (s *session) perform(ctx context.Context, p prepared) (agent.Result, Step, error) {
	record := s.run.Steps[p.index]
	s.engine.Events.Emit(ui.Event{Kind: "start", ID: record.ID, Name: record.Stage, Actor: p.who, Access: string(p.mode)})
	stageCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if p.mode != agent.ReadOnly {
		s.mu.Lock()
		s.cancelStage = cancel
		s.stageWriting = true
		s.mu.Unlock()
		defer func() { s.mu.Lock(); s.cancelStage = nil; s.stageWriting = false; s.mu.Unlock() }()
	}
	provider := s.engine.Claude
	if p.who == "codex" {
		provider = s.engine.Codex
	}
	result, err := provider.Run(stageCtx, p.req)
	after, snapErr := s.engine.Inspector.Snapshot(context.WithoutCancel(ctx), s.input.Project.Root)
	record.Finished = time.Now()
	record.Duration = record.Finished.Sub(record.Started)
	record.ExitCode = result.ExitCode
	record.After = after.ID
	record.snapshot = after
	record.Changed = project.Changed(p.before, after)
	if err == nil && snapErr != nil {
		err = snapErr
	}
	if err == nil && result.ExitCode != 0 {
		err = errors.New("agent process returned nonzero exit status")
	}
	if err == nil && p.mode == agent.ReadOnly && after.ID != p.before.ID {
		err = errors.New("project snapshot drifted during read-only stage")
	}
	if err == nil && p.mode == agent.DocsOnly {
		for _, path := range record.Changed {
			if !inScope(path, s.input.DocsPaths) {
				err = errors.New("docs writer changed a path outside documentation scope")
				break
			}
		}
	}
	if err == nil && result.ReportError == "" {
		if reportErr := result.Report.Validate(p.req.StepID, p.req.Stage); reportErr != nil {
			result.ReportError = reportErr.Error()
		}
	}
	record.Outcome = result.Report.Outcome
	if result.ReportError != "" {
		record.Outcome = "incomplete"
		record.Reason = result.ReportError
	}
	if err != nil {
		record.Outcome = "failed"
		record.Reason = err.Error()
	}
	if err == nil && result.ReportError == "" {
		record.Report = &result.Report
	}
	outputErr := errors.Join(s.engine.Store.Write("steps/"+record.ID+"/output.md", result.Output+"\n"+result.ErrorOutput), s.engine.Store.WriteJSON("steps/"+record.ID+"/record.json", record))
	if record.Report != nil {
		outputErr = errors.Join(outputErr, s.engine.Store.WriteJSON("steps/"+record.ID+"/report.json", result.Report), s.engine.Store.Write(artifactName(record.Stage), result.Report.Markdown))
	}
	s.engine.Events.Emit(ui.Event{Kind: "finish", ID: record.ID, Name: record.Stage, Actor: p.who, Status: record.Outcome, Message: result.Report.Summary + " · " + record.Duration.Round(time.Second).String(), Details: fmt.Sprintf("Exit: %d · snapshot: %s → %s · changed: %v\nReport: %s", record.ExitCode, record.Before, record.After, record.Changed, filepath.Join(s.engine.Store.Dir, "steps", record.ID, "record.json"))})
	return result, record, errors.Join(err, outputErr)
}

func recordSnapshot(record Step, current project.Snapshot) project.Snapshot {
	if record.snapshot.ID == "" {
		return current
	}
	return record.snapshot
}
func inScope(path string, paths []string) bool {
	for _, p := range paths {
		if path == p || strings.HasPrefix(path, strings.TrimSuffix(p, "/")+"/") {
			return true
		}
	}
	return false
}
func artifactName(stage string) string {
	switch {
	case strings.Contains(stage, "plan-review"):
		return "plan-review.md"
	case strings.Contains(stage, "plan"):
		return "plan.md"
	case strings.Contains(stage, "review") || strings.Contains(stage, "verify"):
		return "review.md"
	case strings.Contains(stage, "draft"):
		return "draft.md"
	case strings.Contains(stage, "final") || strings.Contains(stage, "synthesis"):
		return "final.md"
	case strings.Contains(stage, "investigat") || strings.Contains(stage, "diagnos"):
		return "investigation.md"
	default:
		return "implementation.md"
	}
}

func (s *session) checkEvidence(r report.StepReport) error {
	check := func(e report.Evidence) error {
		if e.Kind == "file" {
			path := strings.Split(e.Reference, ":")[0]
			resolved, err := project.ResolveScope(s.input.Project.Root, path)
			if err != nil {
				return err
			}
			if _, err := os.Stat(filepath.Join(s.input.Project.Root, resolved)); err != nil {
				return fmt.Errorf("evidence file unavailable: %w", err)
			}
		}
		if e.Kind == "artifact" {
			if e.Reference == "imported.md" && s.input.Imported != "" {
				return nil
			}
			if e.Reference == "remote-investigation.md" && s.remote != "" {
				return nil
			}
			clean := filepath.Clean(e.Reference)
			if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
				return errors.New("artifact evidence escapes run")
			}
			if _, err := os.Stat(filepath.Join(s.engine.Store.Dir, clean)); err != nil {
				return fmt.Errorf("artifact evidence unavailable: %w", err)
			}
		}
		return nil
	}
	for _, q := range r.Requirements {
		for _, e := range q.Evidence {
			if err := check(e); err != nil {
				return err
			}
		}
	}
	for _, f := range r.Findings {
		for _, e := range f.Evidence {
			if err := check(e); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *session) merge(r report.StepReport, reviewer bool) {
	seen := map[string]bool{}
	for _, q := range r.Requirements {
		s.criteria[q.ID] = q
		seen[q.ID] = true
	}
	for id, q := range s.criteria {
		if !seen[id] {
			q.Status = "unknown"
			s.criteria[id] = q
		}
	}
	for _, f := range r.Findings {
		previous, exists := s.findings[f.ID]
		if !exists {
			for id, old := range s.findings {
				if old.File == f.File && old.Line == f.Line && old.Category == f.Category && strings.EqualFold(old.Problem, f.Problem) {
					f.ID = id
					previous = old
					exists = true
					break
				}
			}
		}
		if !reviewer && (f.Status == "dismissed" || f.Status == "fixed") {
			f.Status = "fixed"
			if r.Outcome == "incomplete" {
				f.Status = "disputed"
			}
		}
		if reviewer && exists && f.Status == "fixed" && len(f.Evidence) == 0 {
			f = previous
		}
		if exists && (previous.Severity == "critical" || previous.Severity == "high") && (f.Status != "fixed" && f.Status != "dismissed") {
			f.Severity = previous.Severity
		}
		s.findings[f.ID] = f
	}
}

func (s *session) safe(point string) decision.DecisionInput {
	r := s.latest
	state := decision.SafeState{Workflow: s.input.Workflow, Stage: r.Stage, Scope: s.run.Scope, Outcome: r.Outcome, Categories: []string{}, MissingEvidence: len(r.MissingEvidence) > 0, Disagreements: len(r.Disagreements) > 0, Questions: len(r.UnresolvedQuestions) > 0, ScopeConflict: len(r.ScopeChanges) > 0, Validation: s.run.Validation.Status, SnapshotMatches: s.run.ReviewedSnapshot == s.run.Current.ID && s.reviewGeneration == s.generation, Diagnosis: r.Diagnosis, FailureClass: r.FailureClass, Remaining: s.remaining(), Progress: "none"}
	for _, q := range s.criteria {
		switch q.Status {
		case "satisfied":
			state.Requirements.Satisfied++
		case "unsatisfied":
			state.Requirements.Unsatisfied++
		default:
			state.Requirements.Unknown++
		}
	}
	for _, f := range s.findings {
		if !slices.Contains(state.Categories, f.Category) {
			state.Categories = append(state.Categories, f.Category)
		}
		switch f.Status {
		case "confirmed":
			state.Findings.Confirmed++
			if f.Severity != "low" {
				state.Findings.Blocking++
			}
		case "disputed":
			state.Findings.Disputed++
		case "proposed":
			state.Findings.Proposed++
		case "fixed", "dismissed":
			if !s.verifiedFor(f, point) {
				state.Findings.UnverifiedFixes++
			}
		}
	}
	slices.Sort(state.Categories)
	return decision.DecisionInput{Point: point, State: state}
}
func (s *session) verified(f report.Finding) bool {
	return s.verifiedFor(f, "code_review")
}

func (s *session) verifiedFor(f report.Finding, point string) bool {
	for i := len(s.run.Steps) - 1; i >= 0; i-- {
		step := s.run.Steps[i]
		isReviewer := step.Agent == "codex" && !strings.Contains(step.Stage, "format-repair") && (strings.Contains(step.Stage, "code-review") || strings.Contains(step.Stage, "code-re-review") || strings.Contains(step.Stage, "targeted-code-verify") || strings.Contains(step.Stage, "targeted-review-verify"))
		switch point {
		case "plan_review":
			isReviewer = step.Stage == "plan-review"
		case "answer_review":
			isReviewer = step.Agent == "claude" && step.Stage == "answer-review"
		case "diagnosis":
			isReviewer = step.Agent == "claude" && step.Stage == "diagnosis-verify"
		case "review_verification":
			isReviewer = isReviewer || step.Stage == "review-verification" || step.Stage == "imported-review-verify"
		}
		if step.Report == nil || step.Report.Outcome != "completed" || !isReviewer || step.Access != agent.ReadOnly || step.After != s.run.Current.ID {
			continue
		}
		for _, item := range step.Report.Findings {
			if item.ID == f.ID && (item.Status == "fixed" || item.Status == "dismissed") {
				return true
			}
		}
	}
	return false
}

func (s *session) gate(ctx context.Context, point string, actions []decision.Action) (decision.Action, error) {
	generation := s.generation
	if err := s.updates(); err != nil {
		return "", err
	}
	if generation != s.generation {
		return "", errReplan
	}
	if len(actions) == 0 {
		return "", stop("unresolved", "mandatory transition budget exhausted")
	}
	input := s.safe(point)
	input.AllowedActions = actions
	s.engine.Events.Emit(ui.Event{Kind: "start", ID: "decision", Name: point, Actor: "JEV / local policy"})
	d, err := s.engine.Decisions.Decide(ctx, input)
	if err != nil {
		s.engine.Events.Emit(ui.Event{Kind: "finish", ID: "decision", Name: point, Status: "failed"})
		return "", err
	}
	if !decision.Allowed(input, d) {
		recommended := d.Action
		d, err = (decision.LocalDecisionService{}).Decide(ctx, input)
		if err != nil {
			return "", err
		}
		d.FallbackReason = "rejected_policy_action: " + string(recommended)
	}
	if err := s.updates(); err != nil {
		return "", err
	}
	if generation != s.generation {
		return "", errReplan
	}
	if d.Action == decision.Complete && !s.completion() {
		return "", stop("unresolved", "completion prerequisites changed")
	}
	if !decision.Allowed(input, d) {
		return "", stop("unresolved", "no permitted local transition")
	}
	current, err := s.engine.Inspector.Snapshot(ctx, s.input.Project.Root)
	if err != nil {
		return "", err
	}
	if current.ID != s.run.Current.ID {
		return "", stop("unresolved", "snapshot drifted before applying decision")
	}
	d.Applied = d.Action
	id := fmt.Sprintf("%02d-%s", len(s.run.Decisions)+1, point)
	s.run.Decisions = append(s.run.Decisions, id)
	record := struct {
		ID       string                 `json:"decision_id"`
		Input    decision.DecisionInput `json:"input"`
		Decision decision.StepDecision  `json:"decision"`
	}{ID: id, Input: input, Decision: d}
	if err := s.engine.Store.WriteJSON("decisions/"+id+".json", record); err != nil {
		return "", err
	}
	s.engine.Events.Emit(ui.Event{Kind: "finish", ID: "decision", Name: point, Actor: d.Source, Status: "completed", Message: string(d.Applied) + " · " + d.Reason + " " + d.FallbackReason})
	if err := s.persist(); err != nil {
		return "", err
	}
	if d.Action == decision.Clarify {
		question := "Недостаточно evidence или scope; уточните задачу"
		if len(s.latest.UnresolvedQuestions) > 0 {
			question = strings.Join(s.latest.UnresolvedQuestions, "; ")
		}
		s.run.Questions = append(s.run.Questions, question)
		if s.engine.Ask == nil {
			return "", stop("needs_input", question)
		}
		answer, err := s.engine.Ask(ctx, question)
		if err != nil && ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err != nil || answer == "" {
			return "", stop("needs_input", question)
		}
		s.run.Questions = slices.DeleteFunc(s.run.Questions, func(q string) bool { return q == question })
		s.mu.Lock()
		s.pending = append(s.pending, Update{Text: answer, Kind: "correction"})
		s.mu.Unlock()
		if err := s.updates(); err != nil {
			return "", err
		}
		return "", errReplan
	}
	if d.Action == decision.Stop {
		return "", stop("unresolved", "policy stopped with unmet prerequisites")
	}
	return d.Action, nil
}

// progressSignature ignores rewritten prose and changed finding IDs; only verified facts count.
func (s *session) progressSignature() string {
	var values []string
	for id, q := range s.criteria {
		values = append(values, "criterion:"+id+":"+q.Status)
		for _, e := range q.Evidence {
			values = append(values, "evidence:"+e.Kind+":"+e.Reference)
		}
	}
	for _, f := range s.findings {
		status := f.Status
		if status == "fixed" && !s.verified(f) {
			status = "unverified"
		}
		values = append(values, "finding:"+f.File+fmt.Sprint(f.Line)+f.Category+":"+status)
		for _, e := range f.Evidence {
			values = append(values, "evidence:"+e.Kind+":"+e.Reference)
		}
	}
	slices.Sort(values)
	values = slices.Compact(values)
	return s.run.Current.ID + "|" + strings.Join(values, "|")
}
func (s *session) progress(before string) error {
	if s.progressSignature() == before {
		s.run.Budgets.NoProgress++
	} else {
		s.run.Budgets.NoProgress = 0
	}
	if s.run.Budgets.NoProgress >= s.engine.Config.Workflow.MaxNoProgressRounds {
		return stop("unresolved", "no_progress")
	}
	return nil
}

func (s *session) handoffJSON() string {
	r := struct {
		Criteria   map[string]report.Requirement `json:"criteria"`
		Findings   map[string]report.Finding     `json:"findings"`
		Validation validation.Result             `json:"validation"`
	}{Criteria: s.criteria, Findings: s.findings, Validation: s.run.Validation}
	b, err := json.Marshal(r)
	if err != nil {
		return s.handoff
	}
	return s.handoff + "\nLocal structured handoff:\n" + string(b)
}
