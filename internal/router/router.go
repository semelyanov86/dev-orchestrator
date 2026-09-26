// Package router selects one registered workflow; explicit commands bypass it.
package router

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"dev-orchestrator/internal/config"
	"dev-orchestrator/internal/router/jev"
	"dev-orchestrator/internal/safety"
	"dev-orchestrator/internal/workflow"
)

type RouteInput struct {
	Task        string
	Languages   []string
	HasTaskfile bool
}
type RouteDecision struct {
	Workflow           string             `json:"workflow"`
	Source             string             `json:"router"`
	Provider           string             `json:"provider,omitempty"`
	Model              string             `json:"model,omitempty"`
	Confidence         *float64           `json:"confidence,omitempty"`
	Complexity         string             `json:"complexity,omitempty"`
	Risk               string             `json:"risk,omitempty"`
	PlanReviewRequired bool               `json:"plan_review_required"`
	CodeReviewRequired bool               `json:"code_review_required"`
	ParallelRequired   bool               `json:"parallel_investigation_required"`
	FallbackReason     string             `json:"fallback_reason,omitempty"`
	Telemetry          *jev.Response      `json:"telemetry,omitempty"`
	RequestedModel     string             `json:"requested_model,omitempty"`
	Thresholds         map[string]float64 `json:"thresholds,omitempty"`
}
type Router interface {
	Route(context.Context, RouteInput) (RouteDecision, error)
}
type Selector interface{ Line(string) (string, error) }
type ManualRouter struct {
	Input  Selector
	Reason string
	Detail string
}

func (m ManualRouter) Route(ctx context.Context, input RouteInput) (RouteDecision, error) {
	if err := ctx.Err(); err != nil {
		return RouteDecision{}, err
	}
	if ConnectionProbe(input.Task) {
		return probeRoute(), nil
	}
	var b strings.Builder
	b.WriteString("Ручной выбор workflow. ")
	if m.Reason != "" {
		fmt.Fprintf(&b, "%s (%s)\n", manualReason(m.Reason), m.Reason)
	} else {
		b.WriteByte('\n')
	}
	if m.Detail != "" {
		fmt.Fprintln(&b, m.Detail)
	}
	b.WriteString("Выберите workflow:\n")
	for i, d := range workflow.Catalog() {
		fmt.Fprintf(&b, "%d. %s\n", i+1, d.Label)
	}
	b.WriteString("Введите номер или название workflow и нажмите Enter. Для отмены: Ctrl+C.")
	answer, err := m.Input.Line(b.String())
	if err != nil {
		return RouteDecision{Source: "manual", FallbackReason: m.Reason}, err
	}
	if n, err := strconv.Atoi(answer); err == nil && n > 0 && n <= len(workflow.Catalog()) {
		answer = workflow.Catalog()[n-1].Name
	}
	d, ok := workflow.Lookup(answer)
	if !ok {
		return RouteDecision{Source: "manual", FallbackReason: m.Reason}, errors.New("needs_input: select a registered workflow")
	}
	return RouteDecision{Workflow: d.Name, Source: "manual", Risk: "high", Complexity: "unknown", FallbackReason: m.Reason}, nil
}

// ConnectionProbe recognizes complete connection-test phrases, never mixed work requests.
func ConnectionProbe(task string) bool {
	task = strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) {
			return ' '
		}
		return unicode.ToLower(r)
	}, task)
	task = strings.Join(strings.Fields(task), " ")
	phrases := []string{
		"это просто проверка ответь что ты меня слышишь", "ping pong", "пинг понг",
		"проверяю связь", "проверяем связь", "проверь связь", "проверить связь", "проверка связи", "тест связи", "тестирую связь", "тестируем связь",
		"проверяю соединение", "проверяем соединение", "проверь соединение", "проверить соединение", "проверка соединения", "тест соединения", "тестирую соединение", "тестируем соединение",
		"connection test", "test connection", "ping",
	}
	for task != "" {
		task = strings.TrimPrefix(task, "просто ")
		found := false
		for _, phrase := range phrases {
			if task == phrase {
				return true
			}
			if rest, ok := strings.CutPrefix(task, phrase+" "); ok {
				task = strings.TrimPrefix(rest, "и ")
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return false
}

func probeRoute() RouteDecision {
	return RouteDecision{Workflow: "ping", Source: "local", Risk: "low", Complexity: "trivial"}
}

func manualReason(reason string) string {
	switch reason {
	case "manual_config":
		return "Включён ручной режим."
	case "missing_api_key":
		return "OpenRouter key не настроен; все workflows доступны вручную."
	case "unsafe_task_input":
		return "Текст задачи не отправлен JEV из-за возможных чувствительных данных."
	case "jev_network_or_timeout":
		return "Запрос JEV не завершился: сеть недоступна или истёк timeout."
	case "request_budget_exhausted":
		return "Лимит запросов JEV исчерпан."
	case "invalid_or_low_confidence_choice", "invalid_or_low_confidence_score", "uncertain_noul":
		return "Ответ JEV не прошёл проверку формата или уверенности."
	default:
		return "JEV недоступен или вернул некорректный ответ."
	}
}

type JevRouter struct {
	Client   *jev.Client
	Config   config.Router
	Fallback ManualRouter
	Cleaner  safety.Cleaner
}

func (j JevRouter) Route(ctx context.Context, input RouteInput) (RouteDecision, error) {
	if err := ctx.Err(); err != nil {
		return RouteDecision{}, err
	}
	if ConnectionProbe(input.Task) {
		return probeRoute(), nil
	}
	if !safety.SafeTask(input.Task, j.Cleaner) {
		return j.fallback(ctx, input, "unsafe_task_input")
	}
	criteria := map[string]string{}
	for _, d := range workflow.Catalog() {
		criteria[d.Name] = d.Intent
	}
	questions := map[string]jev.Question{
		"workflow":                        {Type: "choice", Instructions: "Select the requested result, not the first verb. Questions explaining an existing project, code or architecture are research, even when asking to inspect/read/look. Investigate requires a specific failure or root-cause question. Architecture requires a design proposal/change. Writing workflows require an explicit request to change files. Use the criteria to distinguish outcomes.", Criteria: criteria},
		"complexity":                      {Type: "score", Instructions: "Estimate requested work, not unknown repository size. Trivial: connection test/greeting/single fact. Low: general project overview or simple explanation; small scoped change. Medium: component diagnosis or coherent feature. High: multi-component redesign or difficult incident.", Criteria: []string{"trivial", "low", "medium", "high"}},
		"risk":                            {Type: "score", Instructions: "Assess impact of the requested actions. Low: ordinary read-only project overview/explanation/connection test or small reversible edits. Medium: ordinary local behavior changes. High: security, payments, authentication, migrations or production-sensitive work. Critical: destructive operations, financial settlement or production writes. Do not invent write/production scope for a simple project overview.", Criteria: []string{"low", "medium", "high", "critical"}},
		"plan_review_required":            {Type: "noul", Instructions: "Is additional independent review of an implementation plan needed? This is an optional branch; mandatory workflow reviews always run.", Criteria: map[string]string{"false": "No implementation plan requested, especially questions, explanations, project overviews and connection tests; or no extra plan review needed.", "true": "A substantial implementation plan needs additional independent review beyond mandatory workflow stages."}},
		"code_review_required":            {Type: "noul", Instructions: "Is additional independent review of code changes needed? Mandatory workflow reviews always run.", Criteria: map[string]string{"false": "No code changes requested, especially questions, research, architecture proposals, investigation without fixes and connection tests; or no extra review needed.", "true": "Requested code changes warrant an additional independent code review beyond mandatory workflow stages."}},
		"parallel_investigation_required": {Type: "noul", Instructions: "Are independent parallel investigations needed? Supported only by bug, server_bug, architecture, research, investigate and incident.", Criteria: map[string]string{"false": "Simple explanation, ordinary project overview, connection test, single-component task or unsupported workflow; parallel investigation is unnecessary.", "true": "Explicitly requested independent investigations, a multi-component root-cause search or incident requiring separate evidence collection."}},
	}
	state := struct {
		Task    string `json:"task"`
		Project struct {
			Languages   []string `json:"languages"`
			HasTaskfile bool     `json:"has_taskfile"`
		} `json:"project"`
	}{Task: input.Task}
	state.Project.Languages = input.Languages
	state.Project.HasTaskfile = input.HasTaskfile
	response, err := j.Client.Ask(ctx, questions, state)
	if err != nil {
		return j.rejected(ctx, input, err.Error(), response, "")
	}
	d := RouteDecision{Source: "jev", Provider: "openrouter", Model: response.Model, RequestedModel: j.Client.Model, Thresholds: j.Config.Confidence, Telemetry: &response}
	d.Workflow, err = jev.Choice(response.Answers["workflow"], workflow.Names(), j.Config.Confidence["workflow"])
	if err != nil {
		return j.rejected(ctx, input, err.Error(), response, "workflow")
	}
	d.Confidence = response.Answers["workflow"].Confidence
	d.Complexity, err = jev.Score(response.Answers["complexity"], []string{"trivial", "low", "medium", "high"}, j.Config.Confidence["complexity"])
	if err != nil {
		return j.rejected(ctx, input, err.Error(), response, "complexity")
	}
	d.Risk, err = jev.Score(response.Answers["risk"], []string{"low", "medium", "high", "critical"}, j.Config.Confidence["risk"])
	if err != nil {
		return j.rejected(ctx, input, err.Error(), response, "risk")
	}
	for _, pair := range []struct {
		name  string
		value *bool
	}{{"plan_review_required", &d.PlanReviewRequired}, {"code_review_required", &d.CodeReviewRequired}, {"parallel_investigation_required", &d.ParallelRequired}} {
		*pair.value, err = jev.Noul(response.Answers[pair.name], j.Config.NoulNo, j.Config.NoulYes)
		if err != nil {
			return j.rejected(ctx, input, err.Error(), response, pair.name)
		}
	}
	return d, nil
}

func (j JevRouter) rejected(ctx context.Context, input RouteInput, reason string, response jev.Response, question string) (RouteDecision, error) {
	answer := response.Answers[question]
	switch answer.Type {
	case "choice", "score":
		if answer.Confidence != nil {
			j.Fallback.Detail = fmt.Sprintf("JEV: %s, confidence=%g; порог=%g.", question, *answer.Confidence, j.Config.Confidence[question])
		}
	case "noul":
		if answer.Noul != nil {
			j.Fallback.Detail = fmt.Sprintf("JEV: %s, probability(yes)=%g; требуется <=%g или >=%g.", question, *answer.Noul, j.Config.NoulNo, j.Config.NoulYes)
		}
	}
	d, err := j.fallback(ctx, input, reason)
	d.Telemetry = &response
	d.RequestedModel = j.Client.Model
	d.Model = response.Model
	d.Thresholds = j.Config.Confidence
	return d, err
}
func (j JevRouter) fallback(ctx context.Context, input RouteInput, reason string) (RouteDecision, error) {
	if ctx.Err() != nil {
		return RouteDecision{}, ctx.Err()
	}
	j.Fallback.Reason = reason
	return j.Fallback.Route(ctx, input)
}
