//go:build integration

package router

import (
	"os"
	"testing"

	"dev-orchestrator/internal/config"
	"dev-orchestrator/internal/router/jev"
	"dev-orchestrator/internal/safety"
)

type liveSelector struct{}

func (liveSelector) Line(string) (string, error) { return "research", nil }

func TestLiveRouting(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(home, "", os.Environ(), false)
	if err != nil {
		t.Fatal(err)
	}
	if c.OpenRouter.APIKey == "" {
		t.Skip("OpenRouter key absent")
	}
	budget := &jev.Budget{Max: 1}
	router := JevRouter{Client: jev.New(c.OpenRouter.BaseURL, c.OpenRouter.APIKey, c.OpenRouter.Model, c.OpenRouter.Timeout, budget), Config: c.Router, Fallback: ManualRouter{Input: liveSelector{}}, Cleaner: safety.New(c.OpenRouter.APIKey)}
	r, err := router.Route(t.Context(), RouteInput{Task: "Research how authorization works in this synthetic project. Explain it using documentation. Do not modify files.", Languages: []string{"Go"}, HasTaskfile: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != "jev" {
		if r.FallbackReason != "invalid_or_low_confidence_score" && r.FallbackReason != "invalid_or_low_confidence_choice" && r.FallbackReason != "uncertain_noul" {
			t.Fatalf("unexpected live fallback: %s", r.FallbackReason)
		}
		if r.Telemetry == nil || r.Telemetry.Model == "" {
			t.Fatal("valid native response telemetry missing")
		}
		t.Logf("native six-question routing verified; default thresholds rejected %s; manual fallback applied", r.FallbackReason)
		return
	}
	if r.Workflow != "research" || budget.Used() != 1 {
		t.Fatalf("unexpected route: %s", r.Workflow)
	}
	t.Logf("native six-question routing: workflow=%s complexity=%s risk=%s model=%s", r.Workflow, r.Complexity, r.Risk, r.Model)
}
