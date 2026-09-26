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
	switch task {
	case "ping", "ping pong", "проверяю связь", "проверь связь", "проверка связи", "тест связи", "проверка соединения", "connection test", "test connection", "это просто проверка ответь что ты меня слышишь":
		return true
	default:
		return false
	}
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
		criteria[d.Name] = d.Label
	}
	questions := map[string]jev.Question{
		"workflow":                        {Type: "choice", Instructions: "Select the primary requested workflow. Read-only requests never imply implementation.", Criteria: criteria},
		"complexity":                      {Type: "score", Instructions: "Assess task complexity.", Criteria: []string{"trivial", "low", "medium", "high"}},
		"risk":                            {Type: "score", Instructions: "Assess impact of an incorrect result.", Criteria: []string{"low", "medium", "high", "critical"}},
		"plan_review_required":            {Type: "noul", Instructions: "Would independent plan review be useful?"},
		"code_review_required":            {Type: "noul", Instructions: "Would independent code review be useful?"},
		"parallel_investigation_required": {Type: "noul", Instructions: "Would independent parallel read-only investigation be useful? This branch is supported only by bug, server_bug, architecture, research, investigate and incident; answer no for other workflows."},
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
		return j.rejected(ctx, input, err.Error(), response)
	}
	d := RouteDecision{Source: "jev", Provider: "openrouter", Model: response.Model, RequestedModel: j.Client.Model, Thresholds: j.Config.Confidence, Telemetry: &response}
	d.Workflow, err = jev.Choice(response.Answers["workflow"], workflow.Names(), j.Config.Confidence["workflow"])
	if err != nil {
		return j.rejected(ctx, input, err.Error(), response)
	}
	d.Confidence = response.Answers["workflow"].Confidence
	d.Complexity, err = jev.Score(response.Answers["complexity"], []string{"trivial", "low", "medium", "high"}, j.Config.Confidence["complexity"])
	if err != nil {
		return j.rejected(ctx, input, err.Error(), response)
	}
	d.Risk, err = jev.Score(response.Answers["risk"], []string{"low", "medium", "high", "critical"}, j.Config.Confidence["risk"])
	if err != nil {
		return j.rejected(ctx, input, err.Error(), response)
	}
	for _, pair := range []struct {
		name  string
		value *bool
	}{{"plan_review_required", &d.PlanReviewRequired}, {"code_review_required", &d.CodeReviewRequired}, {"parallel_investigation_required", &d.ParallelRequired}} {
		*pair.value, err = jev.Noul(response.Answers[pair.name], j.Config.NoulNo, j.Config.NoulYes)
		if err != nil {
			return j.rejected(ctx, input, err.Error(), response)
		}
	}
	return d, nil
}

func (j JevRouter) rejected(ctx context.Context, input RouteInput, reason string, response jev.Response) (RouteDecision, error) {
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
