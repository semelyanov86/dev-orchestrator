package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"dev-orchestrator/internal/config"
	"dev-orchestrator/internal/router/jev"
	"dev-orchestrator/internal/safety"
	"dev-orchestrator/internal/workflow"
)

type selector struct{ calls int }

func (s *selector) Line(string) (string, error) { s.calls++; return "bug", nil }
func number(n float64) *float64                 { return &n }
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
		})
	}
}
