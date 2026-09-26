// Package router selects one registered workflow; explicit commands bypass it.
package router

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

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

func (m ManualRouter) Route(ctx context.Context, _ RouteInput) (RouteDecision, error) {
	if err := ctx.Err(); err != nil {
		return RouteDecision{}, err
	}
	var b strings.Builder
	b.WriteString("Выберите workflow:\n")
	for i, d := range workflow.Catalog() {
		fmt.Fprintf(&b, "%d. %s\n", i+1, d.Label)
	}
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

type JevRouter struct {
	Client   *jev.Client
	Config   config.Router
	Fallback ManualRouter
	Cleaner  safety.Cleaner
}

func (j JevRouter) Route(ctx context.Context, input RouteInput) (RouteDecision, error) {
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
