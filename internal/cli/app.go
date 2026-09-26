package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dev-orchestrator/internal/agent"
	"dev-orchestrator/internal/artifacts"
	"dev-orchestrator/internal/config"
	"dev-orchestrator/internal/decision"
	"dev-orchestrator/internal/project"
	"dev-orchestrator/internal/remote"
	"dev-orchestrator/internal/router"
	"dev-orchestrator/internal/router/jev"
	"dev-orchestrator/internal/runner"
	"dev-orchestrator/internal/safety"
	"dev-orchestrator/internal/ui"
	"dev-orchestrator/internal/validation"
	"dev-orchestrator/internal/workflow"
)

type App struct {
	In          io.Reader
	Out         io.Writer
	Err         io.Writer
	Home        string
	Cwd         string
	Env         []string
	Interactive bool
	Version     string
}

func (a App) Run(ctx context.Context, args []string) int {
	o, err := Parse(args)
	if err != nil {
		a.log(err, safety.New())
		return 2
	}
	cleaner := safety.New(envValue(a.Env, "OPENROUTER_API_KEY"), envValue(a.Env, "DEV_OPENROUTER_API_KEY"))
	interfaceUI := ui.New(a.Out, a.Err, cleaner, ui.Options{Quiet: o.Quiet, Verbose: o.Verbose, NoColor: o.NoColor || envValue(a.Env, "NO_COLOR") != "", StderrTTY: ui.IsTTY(a.Err), StdoutTTY: ui.IsTTY(a.Out), Dumb: envValue(a.Env, "TERM") == "dumb", ASCII: envValue(a.Env, "LANG") == "C"})
	finish := func(text string, code int) int {
		if err := interfaceUI.Final(text); err != nil {
			return 1
		}
		return code
	}
	if o.Help {
		return finish(Help(), 0)
	}
	if o.Command == "version" {
		return finish(a.Version, 0)
	}
	// Dry-run reads only local config and metadata; no Git process, providers, SSH or HTTP.
	if o.DryRun {
		return a.dryRun(o, interfaceUI)
	}
	progressCtx, stopProgress := context.WithCancel(ctx)
	progressDone := make(chan struct{})
	go func() { defer close(progressDone); interfaceUI.Run(progressCtx) }()
	defer func() { stopProgress(); <-progressDone }()
	input := ui.NewInput(a.In, a.Interactive, interfaceUI)
	input.Context = ctx
	executor := runner.New()
	inspector := project.Inspector{Exec: executor}
	interfaceUI.Emit(ui.Event{Kind: "start", ID: "discovery", Name: "Определение рабочего проекта", Actor: "Git"})
	p, discoveryErr := inspector.Discover(ctx, a.Cwd, o.Base)
	interfaceUI.Emit(ui.Event{Kind: "finish", ID: "discovery", Name: "Определение рабочего проекта", Status: status(discoveryErr)})
	c, err := config.Load(a.Home, p.Root, a.Env, o.NoJEV)
	if err != nil {
		a.log(err, cleaner)
		return 2
	}
	cleaner = safety.New(c.OpenRouter.APIKey)
	interfaceUI.Cleaner = cleaner
	if o.Command == "config" {
		if len(o.Args) == 1 && o.Args[0] == "path" {
			return finish(config.GlobalPath(a.Home), 0)
		}
		if len(o.Args) != 0 {
			return finish("invalid config arguments", 2)
		}
		data, err := c.Redacted()
		if err != nil {
			a.log(err, cleaner)
			return 1
		}
		return finish(string(data), 0)
	}
	if o.Command == "history" {
		entries, err := artifacts.History(artifacts.Root(a.Home))
		if err != nil {
			a.log(err, cleaner)
			return 1
		}
		var b strings.Builder
		b.WriteString("RUN ID                         PROJECT    WORKFLOW    OUTCOME\n")
		for _, e := range entries {
			fmt.Fprintf(&b, "%s  %s  %s  %s\n", e.RunID, filepath.Base(e.Project), e.Workflow, e.Outcome)
		}
		return finish(b.String(), 0)
	}
	if o.Command == "show" {
		if len(o.Args) != 1 {
			return finish("provide run id", 2)
		}
		dir, err := artifacts.Find(artifacts.Root(a.Home), o.Args[0])
		if err != nil {
			a.log(err, cleaner)
			return 2
		}
		var b strings.Builder
		for _, name := range []string{"task.md", "run.json", "final.md"} {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				a.log(err, cleaner)
				return 1
			}
			b.Write(data)
			b.WriteString("\n")
		}
		return finish(b.String(), 0)
	}
	if o.Command == "doctor" {
		return a.doctor(ctx, c, p, discoveryErr, executor, interfaceUI)
	}
	if discoveryErr != nil {
		a.log(discoveryErr, cleaner)
		return 2
	}
	if o.Command == "init" {
		if len(o.Args) > 0 {
			return finish("init takes no arguments", 2)
		}
		path := filepath.Join(p.Root, "AGENTS.md")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if errors.Is(err, os.ErrExist) {
			return finish("AGENTS.md уже существует; сохранён", 0)
		}
		if err != nil {
			a.log(err, cleaner)
			return 1
		}
		_, writeErr := io.WriteString(f, "# Project instructions\n\nUse repository evidence. Preserve user changes. Run `task all` after source changes.\nDo not commit, push, deploy or apply migrations without explicit authorization.\n")
		if err := errors.Join(writeErr, f.Close()); err != nil {
			a.log(err, cleaner)
			return 1
		}
		return finish("Создан AGENTS.md", 0)
	}
	store, err := artifacts.New(artifacts.Root(a.Home), p.Root, cleaner)
	if err != nil {
		a.log(err, cleaner)
		return 1
	}
	fail := func(outcome, reason string) int {
		code := workflow.ExitCodeContext(ctx, outcome)
		meta := map[string]any{"run_id": store.ID, "project": p.Root, "workflow": o.Command, "outcome": outcome, "termination_reason": reason, "exit_code": code, "started_at": time.Now(), "artifacts": store.Dir, "pending_questions": []string{reason}}
		if err := store.WriteJSON("run.json", meta); err != nil {
			a.log(err, cleaner)
			return 1
		}
		if err := store.Write("task.md", o.Task); err != nil {
			a.log(err, cleaner)
			return 1
		}
		if err := store.Write("final.md", outcome+" · "+reason); err != nil {
			a.log(err, cleaner)
			return 1
		}
		return finish(fmt.Sprintf("%s · %s\nArtifacts: %s", outcome, reason, store.Dir), code)
	}
	ctx, cancelRun := context.WithTimeout(ctx, c.Workflow.Timeout)
	defer cancelRun()
	input.Context = ctx
	task := o.Task
	if o.TaskFile != "" {
		reader := a.In
		var f *os.File
		if o.TaskFile != "-" {
			f, err = os.Open(o.TaskFile)
			if err != nil {
				return fail("needs_input", err.Error())
			}
			defer func() { _ = f.Close() }()
			reader = f
		}
		if o.TaskFile == "-" {
			task, err = input.ReadAll(ctx, 1<<20)
		} else {
			task, err = runner.ReadBounded(reader, 1<<20)
		}
		if err != nil {
			if ctx.Err() != nil {
				return fail(contextOutcome(ctx), ctx.Err().Error())
			}
			return fail("needs_input", err.Error())
		}
	}
	if task == "" && o.Command == "review" {
		task = "Review the pinned current changes; report correctness and security findings without editing code."
	}
	if task == "" && o.Command == "ping" {
		task = "Connection test: Codex ping, Claude pong."
	}
	if strings.TrimSpace(task) == "" {
		task, err = input.Task()
		if err != nil || task == "" {
			if ctx.Err() != nil {
				return fail(contextOutcome(ctx), ctx.Err().Error())
			}
			return fail("needs_input", "provide task and workflow")
		}
	}
	o.Task = task
	budget := &jev.Budget{Max: c.Decisions.MaxRequests}
	client := jev.New(c.OpenRouter.BaseURL, c.OpenRouter.APIKey, c.OpenRouter.Model, c.OpenRouter.Timeout, budget)
	route := router.RouteDecision{Workflow: o.Command, Source: "explicit", Risk: "high"}
	if o.Command == "" {
		interfaceUI.Emit(ui.Event{Kind: "start", ID: "route", Name: "Выбор workflow", Actor: "JEV / manual"})
		var selector router.Router = router.ManualRouter{Input: input, Reason: "manual_config"}
		if c.Router.Type == "jev" {
			selector = router.JevRouter{Client: client, Config: c.Router, Fallback: router.ManualRouter{Input: input}, Cleaner: cleaner}
		}
		route, err = selector.Route(ctx, router.RouteInput{Task: task, Languages: p.Languages, HasTaskfile: p.HasTaskfile})
		interfaceUI.Emit(ui.Event{Kind: "finish", ID: "route", Name: "Выбор workflow", Actor: route.Source, Status: status(err), Message: route.FallbackReason})
		if err != nil {
			if ctx.Err() != nil {
				return fail(contextOutcome(ctx), ctx.Err().Error())
			}
			return fail("needs_input", err.Error())
		}
		o.Command = route.Workflow
	}
	if err := store.WriteJSON("route.json", route); err != nil {
		return fail("failed", err.Error())
	}
	if o.Command == "bug" && o.Server != "" {
		o.Command = "server_bug"
	}
	d, _ := workflow.Lookup(o.Command)
	if o.Server != "" && o.Command != "server_bug" && o.Command != "incident" && o.Command != "investigate" {
		return fail("needs_input", "selected workflow does not support --server")
	}
	importedPath := ""
	if d.Input == "plan" {
		importedPath = o.Plan
	}
	if d.Input == "report" {
		importedPath = o.Report
	}
	if (d.Input == "plan" || d.Input == "report") && importedPath == "" {
		importedPath, err = input.Line("Укажите локальный путь к " + d.Input)
		if err != nil {
			if ctx.Err() != nil {
				return fail(contextOutcome(ctx), ctx.Err().Error())
			}
			return fail("needs_input", err.Error())
		}
	}
	var imported string
	if importedPath != "" {
		f, err := os.Open(importedPath)
		if err != nil {
			return fail("needs_input", err.Error())
		}
		imported, err = runner.ReadBounded(f, 1<<20)
		closeErr := f.Close()
		if err := errors.Join(err, closeErr); err != nil {
			return fail("needs_input", err.Error())
		}
	}
	docsPaths := o.DocsPaths
	if o.Command == "docs" && len(docsPaths) == 0 {
		for _, path := range []string{"README.md", "docs", "documentation"} {
			if _, err := os.Stat(filepath.Join(p.Root, path)); err == nil {
				docsPaths = append(docsPaths, path)
			}
		}
	}
	for i, path := range docsPaths {
		docsPaths[i], err = project.ResolveScope(p.Root, path)
		if err != nil {
			return fail("needs_input", err.Error())
		}
	}
	interfaceUI.Emit(ui.Event{Kind: "message", Name: "dev · " + o.Command, Message: fmt.Sprintf("Проект: %s · %s\nGit: %d staged, %d unstaged, %d untracked\nRun: %s\nRouting: %s · decisions: %s\nРежим: %s\nArtifacts: %s\nМаршрут: %s", p.Root, p.Initial.Branch, p.Initial.Staged, p.Initial.Unstaged, p.Initial.Untracked, store.ID, route.Source, c.Decisions.Type, d.Access, store.Dir, d.Route)})
	var lock *project.Lock
	if workflow.Writing(o.Command) {
		lock, err = project.AcquireLock(filepath.Dir(artifacts.Root(a.Home)), p.Root)
		if err != nil {
			return fail("needs_input", err.Error())
		}
		defer func() {
			if err := lock.Close(); err != nil {
				a.log(err, cleaner)
			}
		}()
	}
	claude := &agent.CLI{Provider: "claude", Command: c.Agents.Claude.Command, Exec: executor, Home: a.Home, StateRoot: artifacts.Root(a.Home), Cleaner: cleaner}
	codex := &agent.CLI{Provider: "codex", Command: c.Agents.Codex.Command, Exec: executor, Home: a.Home, StateRoot: artifacts.Root(a.Home), Cleaner: cleaner}
	interfaceUI.Emit(ui.Event{Kind: "start", ID: "preflight", Name: "Проверка CLI capabilities и sandbox", Actor: "local policy"})
	preflightErr := errors.Join(claude.Preflight(ctx, p.Root), codex.Preflight(ctx, p.Root))
	interfaceUI.Emit(ui.Event{Kind: "finish", ID: "preflight", Name: "CLI preflight", Status: status(preflightErr)})
	if preflightErr != nil {
		return fail("failed", preflightErr.Error())
	}
	decisions := decision.JevDecisionService{Client: client, Config: c.Decisions, Disabled: !safety.SafeTask(task, cleaner)}
	updates := make(chan workflow.Update)
	broker := &questionBroker{answers: make(chan string), done: make(chan struct{})}
	controlCtx, stopControls := context.WithCancel(ctx)
	controlsDone := make(chan struct{})
	go func() { defer close(controlsDone); a.controls(controlCtx, input, updates, broker, interfaceUI) }()
	defer func() { stopControls(); <-controlsDone }()
	engine := workflow.Engine{Config: c, Claude: claude, Codex: codex, Decisions: decisions, Validator: validation.Validator{Exec: executor, Command: c.Validation.Command, Timeout: c.Validation.Timeout}, Inspector: inspector, Remote: remote.RemoteExecutor{Exec: executor, Cleaner: cleaner}, Events: interfaceUI, Store: store, Requests: budget, Updates: updates}
	if a.Interactive {
		engine.Ask = func(askCtx context.Context, question string) (string, error) {
			return broker.ask(askCtx, interfaceUI, question)
		}
	}
	result, runErr := engine.Run(ctx, workflow.Input{Project: p, Workflow: o.Command, Task: task, Imported: imported, ImportedPath: importedPath, DocsPaths: docsPaths, Server: o.Server, Router: route.Source, Risk: route.Risk, PlanReviewRequired: route.PlanReviewRequired, ParallelRequired: route.ParallelRequired, RouteConfidence: route.Confidence, Complexity: route.Complexity, RoutingProvider: route.Provider, RoutingModel: route.Model})
	if runErr != nil && result.Outcome == "failed" {
		interfaceUI.Emit(ui.Event{Kind: "error", Name: "Run failed", Message: result.Reason})
	}
	if o.Command == "ping" {
		interfaceUI.Emit(ui.Event{Kind: "message", Name: "Проверка связи: " + result.Outcome, Message: "Artifacts: " + result.Artifacts})
		text := result.Final
		if result.Outcome != "success" {
			text += "\n" + result.Outcome + " · " + result.Reason
		}
		return finish(text, result.ExitCode)
	}
	return finish(fmt.Sprintf("%s · %s · %s\nValidation: %s\nReview passes: %d/%d\nArtifacts: %s\n\n%s", result.Outcome, result.Reason, result.Finished.Sub(result.Started).Round(time.Second), result.Validation.Status, result.Budgets.ReviewPasses, c.Review.MaxPasses, result.Artifacts, result.Final), result.ExitCode)
}

func (a App) log(err error, c safety.Cleaner) {
	slog.New(slog.NewTextHandler(a.Err, nil)).Error("dev operation failed", "error", c.Clean(err.Error()))
}
func envValue(env []string, name string) string {
	for _, entry := range env {
		k, v, ok := strings.Cut(entry, "=")
		if ok && k == name {
			return v
		}
	}
	return ""
}
func status(err error) string {
	if err != nil {
		return "failed"
	}
	return "completed"
}

func contextOutcome(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.Canceled) {
		return "cancelled"
	}
	return "failed"
}

func (a App) dryRun(o Options, u *ui.UI) int {
	root := a.Cwd
	for {
		if _, err := os.Lstat(filepath.Join(root, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			root = a.Cwd
			break
		}
		root = parent
	}
	c, err := config.Load(a.Home, root, a.Env, o.NoJEV)
	if err != nil {
		a.log(err, u.Cleaner)
		return 2
	}
	var b strings.Builder
	b.WriteString("dry-run — planned; subprocess/network не запускаются\n")
	for _, d := range workflow.Catalog() {
		if o.Command == "" || o.Command == d.Name {
			fmt.Fprintf(&b, "%s · %s\n  %s\n", d.CLI, d.Access, d.Route)
		}
	}
	if o.Command == "ping" {
		b.WriteString("Два коротких read-only запроса; timeout на provider <=30s. Без JEV, gates, validation и retries.\n")
		if err := u.Final(b.String()); err != nil {
			return 1
		}
		return 0
	}
	fmt.Fprintf(&b, "Budgets: steps=%d review fixes=%d passes=%d validation repairs=%d requests=%d timeout=%s\nGates: task_readiness, diagnosis, plan_review, implementation_result, validation_failure, code_review, answer_review, review_verification\nOptional branches применяются только после engine policy.\n", c.Workflow.MaxSteps, c.Review.MaxIterations, c.Review.MaxPasses, c.ValidationRepair.MaxIterations, c.Decisions.MaxRequests, c.Workflow.Timeout)
	if err := u.Final(b.String()); err != nil {
		return 1
	}
	return 0
}

func (a App) doctor(ctx context.Context, c config.Config, p project.Project, projectErr error, executor *runner.Runner, u *ui.UI) int {
	var b strings.Builder
	b.WriteString("Environment\n")
	failed := false
	for _, command := range []string{"git", "task", c.Agents.Claude.Command, c.Agents.Codex.Command, "bwrap", "ssh"} {
		if _, err := exec.LookPath(command); err != nil {
			fmt.Fprintf(&b, "[!] %s unavailable\n", command)
			failed = true
		} else {
			fmt.Fprintf(&b, "[ok] %s\n", command)
		}
	}
	b.WriteString("[ok] config valid\nRouter\n")
	if c.OpenRouter.APIKey == "" || c.Decisions.MaxRequests == 0 || c.Router.Type == "manual" {
		b.WriteString("[i] OpenRouter/JEV not configured or disabled; manual/local fallback available\n")
	} else {
		u.Emit(ui.Event{Kind: "start", ID: "health", Name: "JEV health request", Actor: "JEV"})
		client := jev.New(c.OpenRouter.BaseURL, c.OpenRouter.APIKey, c.OpenRouter.Model, c.OpenRouter.Timeout, &jev.Budget{Max: 1})
		response, err := client.Ask(ctx, map[string]jev.Question{"available": {Type: "noul", Instructions: "Is this a health check?"}}, struct {
			Purpose string `json:"purpose"`
		}{Purpose: "health check"})
		if err == nil {
			_, err = jev.Noul(response.Answers["available"], c.Router.NoulNo, c.Router.NoulYes)
		}
		u.Emit(ui.Event{Kind: "finish", ID: "health", Name: "JEV health request", Status: status(err)})
		if err != nil {
			b.WriteString("[!] OpenRouter/JEV unavailable; manual/local fallback available\n")
		} else {
			b.WriteString("[ok] JEV reachable\n")
		}
	}
	b.WriteString("Project\n")
	if projectErr != nil {
		b.WriteString("[!] current directory is not a Git project\n")
		failed = true
	} else {
		fmt.Fprintf(&b, "[ok] %s\n", p.Root)
		v := validation.Validator{Exec: executor, Command: c.Validation.Command, Timeout: c.Validation.Timeout}
		if err := v.Preflight(ctx, p.Root); err != nil {
			fmt.Fprintf(&b, "[!] %s\n", err)
			failed = true
		} else {
			b.WriteString("[ok] validation target (listing only)\n")
		}
	}
	if _, err := os.Stat(filepath.Join(a.Home, ".ssh", "config")); err == nil {
		b.WriteString("[ok] SSH config available\n")
	} else {
		b.WriteString("[i] SSH default config/agent/keys; no profiles required for local work\n")
	}
	code := 0
	if failed {
		code = 1
	}
	if err := u.Final(b.String()); err != nil {
		return 1
	}
	return code
}
