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
	Intent string // Requested outcome presented to the classifier, distinct from the execution route.
}

func Catalog() []Definition {
	return []Definition{
		{Name: "feature", CLI: "feature", Label: "Feature", Intent: "Implement a new feature or capability; explicitly requested source changes.", Access: "local_write", Route: "Claude plan → Codex plan review → Claude implementation → task all → Codex review; optional fixes/verification"},
		{Name: "bug", CLI: "bug", Label: "Bug", Intent: "Diagnose AND fix a specific local software defect; behavior is reported broken and a fix is requested.", Access: "local_write", Route: "Codex diagnosis → Claude diagnosis verification → Claude fix → task all → Codex review; optional fixes"},
		{Name: "server_bug", CLI: "server-bug", Label: "Production/server bug", Intent: "Diagnose a concrete production/server failure through remote evidence and fix a confirmed local code defect.", Access: "local_write", Route: "remote read-only evidence → Codex diagnosis → Claude verification → local fix for code defect → task all → Codex review", Input: "server"},
		{Name: "architecture", CLI: "architecture", Label: "Architecture / design", Intent: "Propose or evaluate a NEW architecture/design change. Explaining the existing project is research instead. Read-only proposal.", Access: "read_only", Route: "Codex draft → Claude critical review → Codex final; optional revisions"},
		{Name: "research", CLI: "research", Label: "Research / question", Intent: "Answer a question; inspect and explain what the existing project does or how existing code works; summarize documentation or compare ideas. Examples: о чём этот проект; что делает проект; объясни существующую архитектуру. No bug/root-cause search or file changes.", Access: "read_only", Route: "Codex draft → Claude critical review → Codex final; optional revisions"},
		{Name: "refactor", CLI: "refactor", Label: "Refactor", Intent: "Restructure existing code while preserving observable behavior; explicitly requested code edits.", Access: "local_write", Route: "Codex invariant/compatibility plan → critical review → Claude implementation → task all → Codex review"},
		{Name: "tests", CLI: "tests", Label: "Tests", Intent: "Write or improve automated tests; test files must change, production behavior should remain unchanged.", Access: "local_write", Route: "Codex test plan → plan gate → Claude tests → task all → Codex review"},
		{Name: "review", CLI: "review", Label: "Code review", Intent: "Review existing code or changes for defects and report findings; no fixes requested. A general project explanation is research instead.", Access: "read_only", Route: "Codex findings → Claude independent verification → Codex final; optional targeted verification"},
		{Name: "incident", CLI: "incident", Label: "Incident investigation", Intent: "Investigate an outage or incident using independent evidence collection and synthesis; no remediation requested.", Access: "read_only", Route: "independent parallel Claude/Codex investigations → Codex synthesis; optional diagnostics"},
		{Name: "investigate", CLI: "investigate", Label: "Investigation without fixes", Intent: "Find the cause of a SPECIFIC failure, error or unexpected behavior without fixing it. Requires a concrete symptom; ordinary questions about what a project does are research instead.", Access: "read_only", Route: "Codex diagnosis → Claude evidence verification → Codex final; optional diagnostics"},
		{Name: "plan", CLI: "plan", Label: "Plan without implementation", Intent: "Produce an implementation plan for requested changes, explicitly without implementing them yet.", Access: "read_only", Route: "Claude plan → Codex plan review → final plan; optional bounded revisions"},
		{Name: "implement", CLI: "implement", Label: "Implement an existing plan", Intent: "Implement an already supplied plan; an existing plan is the input, rather than a new feature description.", Access: "local_write", Route: "Codex imported plan applicability → Claude implementation → task all → Codex review", Input: "plan"},
		{Name: "docs", CLI: "docs", Label: "Documentation", Intent: "Write or update documentation files. Reading documentation to answer a question is research instead.", Access: "documentation_only", Route: "Claude fact-check/plan → Claude documentation → validation policy → Codex review; optional fixes"},
		{Name: "chore", CLI: "chore", Label: "Maintenance / chore", Intent: "Perform requested maintenance of dependencies, configuration, build or tooling; no new product capability.", Access: "local_write", Route: "Codex compatibility plan → Claude maintenance → task all → Codex review"},
		{Name: "fix_review", CLI: "fix-review", Label: "Fix an existing review", Intent: "Fix findings from an already supplied code review; the existing review report is the input.", Access: "local_write", Route: "Codex imported finding verification → Claude confirmed fixes → task all → Codex re-review", Input: "report"},
		{Name: "ping", CLI: "ping", Label: "Connection test / ping-pong", Intent: "Only test communication with Codex and Claude by requesting short ping/pong replies; no project analysis, server diagnostics or code changes.", Access: "read_only", Route: "Codex ping → Claude pong; two short replies, no JEV, reviews or validation"},
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
