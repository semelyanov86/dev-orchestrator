package workflow

import "slices"

// Definition is the single catalog used by CLI, manual selection, JEV and dry-run.
type Definition struct {
	Name   string
	CLI    string
	Label  string
	Access string
	Route  string
	Input  string
}

func Catalog() []Definition {
	return []Definition{
		{Name: "feature", CLI: "feature", Label: "Feature", Access: "local_write", Route: "Claude plan → Codex plan review → Claude implementation → task all → Codex review; optional fixes/verification"},
		{Name: "bug", CLI: "bug", Label: "Bug", Access: "local_write", Route: "Codex diagnosis → Claude diagnosis verification → Claude fix → task all → Codex review; optional fixes"},
		{Name: "server_bug", CLI: "server-bug", Label: "Production/server bug", Access: "local_write", Route: "remote read-only evidence → Codex diagnosis → Claude verification → local fix for code defect → task all → Codex review", Input: "server"},
		{Name: "architecture", CLI: "architecture", Label: "Architecture / design", Access: "read_only", Route: "Codex draft → Claude critical review → Codex final; optional revisions"},
		{Name: "research", CLI: "research", Label: "Research / question", Access: "read_only", Route: "Codex draft → Claude critical review → Codex final; optional revisions"},
		{Name: "refactor", CLI: "refactor", Label: "Refactor", Access: "local_write", Route: "Codex invariant/compatibility plan → critical review → Claude implementation → task all → Codex review"},
		{Name: "tests", CLI: "tests", Label: "Tests", Access: "local_write", Route: "Codex test plan → plan gate → Claude tests → task all → Codex review"},
		{Name: "review", CLI: "review", Label: "Code review", Access: "read_only", Route: "Codex findings → Claude independent verification → Codex final; optional targeted verification"},
		{Name: "incident", CLI: "incident", Label: "Incident investigation", Access: "read_only", Route: "independent parallel Claude/Codex investigations → Codex synthesis; optional diagnostics"},
		{Name: "investigate", CLI: "investigate", Label: "Investigation without fixes", Access: "read_only", Route: "Codex diagnosis → Claude evidence verification → Codex final; optional diagnostics"},
		{Name: "plan", CLI: "plan", Label: "Plan without implementation", Access: "read_only", Route: "Claude plan → Codex plan review → final plan; optional bounded revisions"},
		{Name: "implement", CLI: "implement", Label: "Implement an existing plan", Access: "local_write", Route: "Codex imported plan applicability → Claude implementation → task all → Codex review", Input: "plan"},
		{Name: "docs", CLI: "docs", Label: "Documentation", Access: "documentation_only", Route: "Claude fact-check/plan → Claude documentation → validation policy → Codex review; optional fixes"},
		{Name: "chore", CLI: "chore", Label: "Maintenance / chore", Access: "local_write", Route: "Codex compatibility plan → Claude maintenance → task all → Codex review"},
		{Name: "fix_review", CLI: "fix-review", Label: "Fix an existing review", Access: "local_write", Route: "Codex imported finding verification → Claude confirmed fixes → task all → Codex re-review", Input: "report"},
	}
}
func Names() []string {
	var out []string
	for _, d := range Catalog() {
		out = append(out, d.Name)
	}
	return out
}
func Lookup(name string) (Definition, bool) {
	for _, d := range Catalog() {
		if d.Name == name || d.CLI == name {
			return d, true
		}
	}
	return Definition{}, false
}
func Writing(name string) bool {
	d, ok := Lookup(name)
	return ok && slices.Contains([]string{"local_write", "documentation_only"}, d.Access)
}
