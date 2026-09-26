package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dev-orchestrator/internal/config"
	"dev-orchestrator/internal/router/jev"
	"dev-orchestrator/internal/safety"
	"dev-orchestrator/internal/workflow"
)

type selector struct {
	calls    int
	question string
}

func (s *selector) Line(question string) (string, error) {
	s.calls++
	s.question = question
	return "bug", nil
}
func number(n float64) *float64 { return &n }
func routeResponse(conf float64) jev.Response {
	probabilities := map[string]float64{}
	for _, name := range workflow.Names() {
		probabilities[name] = 0
	}
	probabilities["bug"] = 1
	score := jev.Answer{Type: "score", Score: number(2), Confidence: number(.95), Probabilities: map[string]float64{"0": 0, "1": 0, "2": 1, "3": 0}, Legend: map[string]string{"0": "trivial", "1": "low", "2": "medium", "3": "high"}}
	risk := score
	risk.Legend = map[string]string{"0": "low", "1": "medium", "2": "high", "3": "critical"}
	return jev.Response{Model: "typesafe/test", Answers: map[string]jev.Answer{"workflow": {Type: "choice", Choice: "bug", Confidence: number(conf), Probabilities: probabilities}, "complexity": score, "risk": risk, "plan_review_required": {Type: "noul", Noul: number(.95)}, "code_review_required": {Type: "noul", Noul: number(.95)}, "parallel_investigation_required": {Type: "noul", Noul: number(.1)}}}
}
func TestJevRouterSuccessAndFallback(t *testing.T) {
	for _, tt := range []struct {
		name       string
		confidence float64
		status     int
		key        string
		task       string
		source     string
	}{{name: "accepted", confidence: .95, status: 200, key: "key", task: "Fix duplicate requests", source: "jev"}, {name: "low confidence", confidence: .5, status: 200, key: "key", task: "Fix duplicate requests", source: "manual"}, {name: "missing key", status: 200, task: "Fix duplicate requests", source: "manual"}, {name: "500", status: 500, key: "key", task: "Fix duplicate requests", source: "manual"}, {name: "unsafe task", status: 200, key: "key", task: "Customer card 4111111111111111", source: "manual"}} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_ = json.NewEncoder(w).Encode(routeResponse(tt.confidence))
			}))
			defer server.Close()
			input := &selector{}
			j := JevRouter{Client: jev.New(server.URL, tt.key, "test", time.Second, &jev.Budget{Max: 12}), Config: config.Defaults().Router, Fallback: ManualRouter{Input: input}, Cleaner: safety.New()}
			route, err := j.Route(context.Background(), RouteInput{Task: tt.task})
			if err != nil || route.Source != tt.source || route.Workflow != "bug" {
				t.Fatalf("route %+v error %v", route, err)
			}
			if tt.name == "low confidence" && route.Telemetry == nil {
				t.Fatal("rejected response telemetry lost")
			}
			if tt.source == "manual" {
				if !strings.Contains(input.question, route.FallbackReason) || !strings.Contains(input.question, "Ручной выбор") {
					t.Fatalf("fallback reason absent before user selection: %q", input.question)
				}
				if !strings.Contains(input.question, "Введите номер") {
					t.Fatalf("selection instructions absent: %q", input.question)
				}
			}
		})
	}
}

func TestConnectionProbeLocalRouting(t *testing.T) {
	for _, phrase := range []string{"проверяю связь", "Проверка связи!", "ping", "ping-pong", "это просто проверка. Ответь, что ты меня слышишь", "проверка связи, тестируем соединение", "Просто проверяем связь и тестируем соединение.", "ping, проверка связи\nтест соединения"} {
		t.Run(phrase, func(t *testing.T) {
			if !ConnectionProbe(phrase) {
				t.Fatalf("connection-test phrase not recognized: %q", phrase)
			}
			input := &selector{}
			// A nil JEV client must remain unused; no routing request or manual selection.
			for _, router := range []Router{JevRouter{Fallback: ManualRouter{Input: input}}, ManualRouter{Input: input}} {
				r, err := router.Route(t.Context(), RouteInput{Task: phrase})
				if err != nil || r.Workflow != "ping" || r.Source != "local" || input.calls != 0 {
					t.Fatalf("probe route=%+v err=%v manual calls=%d", r, err, input.calls)
				}
			}
		})
	}
	for _, phrase := range []string{"проверяю связь, затем исправь код", "проверка связи, тестируем соединение и исправь код", "проверка связи и", "проверить связь с базой данных", "add ping workflow", "исправь ping endpoint", "проанализируй проверку связи", ""} {
		if ConnectionProbe(phrase) {
			t.Fatalf("work request incorrectly routed to probe: %q", phrase)
		}
	}
}

func TestJevRouterAmbiguousAnswersSelectManually(t *testing.T) {
	for _, tt := range []struct {
		name     string
		question string
		answer   jev.Answer
		reason   string
		detail   string
	}{
		{name: "risk confidence", question: "risk", answer: jev.Answer{Type: "score", Score: number(0), Confidence: number(.66), Probabilities: map[string]float64{"0": 1, "1": 0, "2": 0, "3": 0}, Legend: map[string]string{"0": "low", "1": "medium", "2": "high", "3": "critical"}}, reason: "invalid_or_low_confidence_score", detail: "risk, confidence=0.66; порог=0.8"},
		{name: "complexity confidence", question: "complexity", answer: jev.Answer{Type: "score", Score: number(1), Confidence: number(.47), Probabilities: map[string]float64{"0": 0, "1": 1, "2": 0, "3": 0}, Legend: map[string]string{"0": "trivial", "1": "low", "2": "medium", "3": "high"}}, reason: "invalid_or_low_confidence_score", detail: "complexity, confidence=0.47; порог=0.7"},
		{name: "optional review uncertainty", question: "code_review_required", answer: jev.Answer{Type: "noul", Noul: number(.7)}, reason: "uncertain_noul", detail: "code_review_required, probability(yes)=0.7; требуется <=0.15 или >=0.85"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := routeResponse(.99)
			response.Answers[tt.question] = tt.answer
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if err := json.NewEncoder(w).Encode(response); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(server.Close)
			input := &selector{}
			j := JevRouter{Client: jev.New(server.URL, "key", "test", time.Second, &jev.Budget{Max: 1}), Config: config.Defaults().Router, Fallback: ManualRouter{Input: input}, Cleaner: safety.New()}
			r, err := j.Route(t.Context(), RouteInput{Task: "Diagnose and fix duplicate requests"})
			if err != nil || r.Source != "manual" || r.FallbackReason != tt.reason || input.calls != 1 {
				t.Fatalf("uncertain answer did not require selection: route=%+v err=%v calls=%d", r, err, input.calls)
			}
			if !strings.Contains(input.question, tt.detail) {
				t.Fatalf("question confidence/threshold not shown before selection: %q", input.question)
			}
			if r.Telemetry == nil || r.Telemetry.Answers[tt.question].Type != tt.answer.Type {
				t.Fatal("rejected routing telemetry lost")
			}
		})
	}
}

func TestJevRouterProjectQuestion(t *testing.T) {
	const task = "посмотри и скажи о чём этот проект"
	response := routeResponse(.99)
	answer := response.Answers["workflow"]
	answer.Choice = "research"
	answer.Probabilities["bug"] = 0
	answer.Probabilities["research"] = 1
	response.Answers["workflow"] = answer
	answer = response.Answers["complexity"]
	answer.Score = number(1)
	answer.Probabilities = map[string]float64{"0": 0, "1": 1, "2": 0, "3": 0}
	response.Answers["complexity"] = answer
	answer = response.Answers["risk"]
	answer.Score = number(0)
	answer.Probabilities = map[string]float64{"0": 1, "1": 0, "2": 0, "3": 0}
	response.Answers["risk"] = answer
	response.Answers["plan_review_required"] = jev.Answer{Type: "noul", Noul: number(.1)}
	response.Answers["code_review_required"] = jev.Answer{Type: "noul", Noul: number(.1)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var payload struct {
			Questions map[string]jev.Question `json:"questions"`
			State     struct {
				Task string `json:"task"`
			} `json:"state"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if payload.State.Task != task || len(payload.Questions) != 6 {
			t.Errorf("routing request: task=%q questions=%d", payload.State.Task, len(payload.Questions))
		}
		criteria, ok := payload.Questions["workflow"].Criteria.(map[string]any)
		if !ok || len(criteria) != len(workflow.Catalog()) {
			t.Errorf("workflow criteria missing: %v", criteria)
		}
		for _, d := range workflow.Catalog() {
			intent, ok := criteria[d.Name].(string)
			if !ok || strings.TrimSpace(intent) == "" || intent == d.Label {
				t.Errorf("%s lacks a meaningful classification criterion: %q", d.Name, intent)
			}
		}
		for _, name := range []string{"plan_review_required", "code_review_required", "parallel_investigation_required"} {
			q := payload.Questions[name]
			criteria, ok := q.Criteria.(map[string]any)
			if q.Type != "noul" || !ok || criteria["false"] == nil || criteria["true"] == nil {
				t.Errorf("%s lacks explicit yes/no criteria: %+v", name, q)
			}
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	input := &selector{}
	budget := &jev.Budget{Max: 1}
	j := JevRouter{Client: jev.New(server.URL, "key", "test", time.Second, budget), Config: config.Defaults().Router, Fallback: ManualRouter{Input: input}, Cleaner: safety.New()}
	r, err := j.Route(t.Context(), RouteInput{Task: task, Languages: []string{"Go"}, HasTaskfile: true})
	if err != nil || r.Source != "jev" || r.Workflow != "research" || r.Complexity != "low" || r.Risk != "low" {
		t.Fatalf("project question route=%+v err=%v", r, err)
	}
	if r.PlanReviewRequired || r.CodeReviewRequired || r.ParallelRequired || input.calls != 0 || budget.Used() != 1 {
		t.Fatalf("unexpected extra branches, selection or request: route=%+v manual=%d requests=%d", r, input.calls, budget.Used())
	}
}
