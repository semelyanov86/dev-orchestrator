//go:build integration

package jev

import (
	"os"
	"testing"

	"dev-orchestrator/internal/config"
)

func TestLiveNativeDecisions(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(home, "", os.Environ(), false)
	if err != nil {
		t.Fatal(err)
	}
	if c.OpenRouter.APIKey == "" {
		t.Skip("OpenRouter key absent; manual/local operation is available")
	}
	client := New(c.OpenRouter.BaseURL, c.OpenRouter.APIKey, c.OpenRouter.Model, c.OpenRouter.Timeout, &Budget{Max: 1})
	questions := map[string]Question{
		"workflow":   {Type: "choice", Instructions: "Select the requested workflow.", Criteria: map[string]string{"research": "Read-only research", "feature": "Implement a feature"}},
		"complexity": {Type: "score", Instructions: "Assess complexity.", Criteria: []string{"low", "high"}},
		"read_only":  {Type: "noul", Instructions: "Is this task read-only?"},
	}
	r, err := client.Ask(t.Context(), questions, struct {
		Task string `json:"task"`
	}{Task: "Read the documentation and summarize this synthetic project's purpose; do not change files."})
	if err != nil {
		t.Fatalf("live native JEV unavailable: %v; manual/local fallback remains available", err)
	}
	if _, err := Choice(r.Answers["workflow"], []string{"research", "feature"}, c.Router.Confidence["workflow"]); err != nil {
		t.Fatalf("live workflow decision rejected: %v", err)
	}
	t.Logf("native choice/score/noul parsed; returned model %s; duration %s", r.Model, r.Duration)
}
