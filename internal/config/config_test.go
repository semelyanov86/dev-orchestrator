package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func put(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}
func TestLoadMergeAndPrecedence(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	put(t, GlobalPath(home), "review:\n  max_iterations: 7\nopenrouter:\n  api_key: global-secret\nservers:\n  production:\n    host: server.example\n    user: deploy\n    port: 22\n    project_path: /srv/app\n", 0600)
	put(t, filepath.Join(project, ".dev-agent.yaml"), "review:\n  max_iterations: 0\nrouter:\n  confidence:\n    workflow: 0.91\nservers:\n  production:\n    port: 2222\n", 0644)
	c, err := Load(home, project, []string{"OPENROUTER_API_KEY=environment-secret", "DEV_WORKFLOW_MAX_STEPS=9"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Review.MaxIterations != 0 || c.Workflow.MaxSteps != 9 || c.OpenRouter.APIKey != "environment-secret" || c.Router.Type != "manual" || c.Decisions.Type != "local" {
		t.Fatalf("precedence failed: %+v", c)
	}
	s := c.Servers["production"]
	if s.Host != "server.example" || s.User != "deploy" || s.Port != 2222 || s.ProjectPath != "/srv/app" {
		t.Fatalf("partial server merge failed %+v", s)
	}
	if c.Router.Confidence["risk"] != .8 || c.Router.Confidence["workflow"] != .91 {
		t.Fatal("partial thresholds merge failed")
	}
	data, err := c.Redacted()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "environment-secret") {
		t.Fatal("config exposes key")
	}
}
func TestLoadInvalidConfig(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		mode       os.FileMode
	}{{name: "unsafe secret mode", body: "openrouter:\n  api_key: secret\n", mode: 0644}, {name: "negative budget", body: "review:\n  max_iterations: -1\n", mode: 0600}, {name: "zero passes", body: "review:\n  max_passes: 0\n", mode: 0600}, {name: "invalid thresholds", body: "router:\n  noul_yes: 0.1\n  noul_no: 0.8\n", mode: 0600}, {name: "unknown field", body: "invented: true\n", mode: 0600}, {name: "multiple documents", body: "{}\n---\n{}\n", mode: 0600}, {name: "zero timeout", body: "workflow:\n  timeout: 0s\n", mode: 0600}, {name: "remote plain http", body: "openrouter:\n  base_url: http://example.com\n", mode: 0600}} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			put(t, GlobalPath(home), tt.body, tt.mode)
			if _, err := Load(home, "", nil, false); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}
func TestDefaultsAreIndependent(t *testing.T) {
	a := Defaults()
	b := Defaults()
	a.Router.Confidence["risk"] = 0
	if b.Router.Confidence["risk"] != .8 {
		t.Fatal("defaults share map")
	}
}
