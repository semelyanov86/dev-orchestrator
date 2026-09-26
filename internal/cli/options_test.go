package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"dev-orchestrator/internal/workflow"
)

func TestParseCatalogAndFlags(t *testing.T) {
	for _, d := range workflow.Catalog() {
		t.Run(d.Name, func(t *testing.T) {
			o, err := Parse([]string{d.CLI, "long task", "--no-jev"})
			if err != nil || o.Command != d.Name || !o.NoJEV {
				t.Fatalf("Parse %+v %v", o, err)
			}
		})
	}
	for _, args := range [][]string{{"feature", "--quiet", "--verbose"}, {"feature", "task", "--task-file", "-"}, {"review", "--server", "production"}, {"bug", "--plan", "plan.md"}, {"unknown"}, {"feature", "--made-up"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("invalid args accepted %v", args)
		}
	}
}
func TestDryRunNoSubprocess(t *testing.T) {
	var out, stderr bytes.Buffer
	a := App{In: strings.NewReader(""), Out: &out, Err: &stderr, Home: t.TempDir(), Cwd: t.TempDir(), Env: []string{"PATH=/does-not-exist"}}
	code := a.Run(t.Context(), []string{"feature", "--dry-run", "--no-jev"})
	if code != 0 || !strings.Contains(out.String(), "planned") {
		t.Fatalf("dry run code %d stderr %s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "discovery") {
		t.Fatal("dryrun started process discovery")
	}
}
func TestHelpWithoutGitOrConfig(t *testing.T) {
	var out bytes.Buffer
	a := App{In: strings.NewReader(""), Out: &out, Err: io.Discard, Home: t.TempDir(), Cwd: t.TempDir()}
	if code := a.Run(t.Context(), []string{"--help"}); code != 0 || !strings.Contains(out.String(), "fix-review") {
		t.Fatal("help unavailable")
	}
}
