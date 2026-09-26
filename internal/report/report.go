// Package report defines the versioned local handoff between agents and the engine.
package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// Evidence identifies locally checkable evidence. Kind is file, artifact, or observation.
type Evidence struct {
	Kind      string `json:"kind"`
	Reference string `json:"reference"`
	Detail    string `json:"detail"`
}

// Requirement is an acceptance criterion; unknown is never implicit success.
type Requirement struct {
	ID          string     `json:"id"`
	Description string     `json:"description"`
	Status      string     `json:"status"`
	Evidence    []Evidence `json:"evidence"`
}

// Finding keeps stable identity and the rationale behind its current status.
type Finding struct {
	ID                 string     `json:"id"`
	Severity           string     `json:"severity"`
	Category           string     `json:"category"`
	Status             string     `json:"status"`
	Problem            string     `json:"problem"`
	Impact             string     `json:"impact"`
	SuggestedDirection string     `json:"suggested_direction"`
	File               string     `json:"file"`
	Line               int        `json:"line"`
	Evidence           []Evidence `json:"evidence"`
	Rationale          string     `json:"rationale"`
}

// StepReport is an agent claim; process, snapshot and validation facts are engine-owned.
type StepReport struct {
	SchemaVersion       int           `json:"schema_version"`
	StepID              string        `json:"step_id"`
	Stage               string        `json:"stage"`
	Outcome             string        `json:"outcome"`
	Summary             string        `json:"summary"`
	Markdown            string        `json:"markdown"`
	Requirements        []Requirement `json:"requirements"`
	Findings            []Finding     `json:"findings"`
	MissingEvidence     []string      `json:"missing_evidence"`
	Disagreements       []string      `json:"disagreements"`
	UnresolvedQuestions []string      `json:"unresolved_questions"`
	ScopeChanges        []string      `json:"scope_changes"`
	ArtifactReferences  []string      `json:"artifact_references"`
	Diagnosis           string        `json:"diagnosis"`     // local_code, operational, unknown, not_applicable
	FailureClass        string        `json:"failure_class"` // current_change, pre_existing, infrastructure, unknown, not_applicable
	Verdict             string        `json:"verdict"`       // pass, findings, inconclusive, not_applicable
}

func Categories() []string {
	return []string{"security", "correctness", "compatibility", "missing_tests", "requirements_gap", "operations", "documentation"}
}

// Decode rejects unknown fields and trailing values as well as malformed reports.
func Decode(data string, stepID, stage string) (StepReport, error) {
	var r StepReport
	d := json.NewDecoder(strings.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		return r, fmt.Errorf("decode step report: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return r, errors.New("step report contains trailing data")
	}
	return r, r.Validate(stepID, stage)
}

// Validate checks the local contract. Evidence must be checked by the engine too.
func (r StepReport) Validate(stepID, stage string) error {
	if r.SchemaVersion != 1 || r.StepID != stepID || r.Stage != stage {
		return errors.New("report version, step id or stage mismatch")
	}
	if !slices.Contains([]string{"completed", "incomplete", "needs_input"}, r.Outcome) {
		return errors.New("invalid report outcome")
	}
	if strings.TrimSpace(r.Markdown) == "" || strings.TrimSpace(r.Summary) == "" {
		return errors.New("empty report result")
	}
	if r.Requirements == nil || r.Findings == nil || r.MissingEvidence == nil || r.Disagreements == nil || r.UnresolvedQuestions == nil || r.ScopeChanges == nil || r.ArtifactReferences == nil {
		return errors.New("report collections must be explicit arrays")
	}
	if len(r.Requirements) == 0 {
		return errors.New("report must cover at least one acceptance criterion")
	}
	if len(r.Requirements) > 256 || len(r.Findings) > 256 {
		return errors.New("report exceeds collection limit")
	}
	ids := map[string]bool{}
	for _, q := range r.Requirements {
		if q.ID == "" || q.Description == "" || ids[q.ID] || !slices.Contains([]string{"satisfied", "unsatisfied", "unknown"}, q.Status) {
			return errors.New("invalid or duplicate requirement")
		}
		ids[q.ID] = true
		if q.Evidence == nil || (q.Status == "satisfied" && len(q.Evidence) == 0) {
			return errors.New("requirement lacks evidence")
		}
		if err := validateEvidence(q.Evidence); err != nil {
			return err
		}
	}
	ids = map[string]bool{}
	for _, f := range r.Findings {
		if f.ID == "" || ids[f.ID] || f.Problem == "" || f.Impact == "" || f.SuggestedDirection == "" || f.Rationale == "" || f.Line < 0 {
			return errors.New("invalid or duplicate finding")
		}
		ids[f.ID] = true
		if !slices.Contains([]string{"critical", "high", "medium", "low"}, f.Severity) || !slices.Contains(Categories(), f.Category) || !slices.Contains([]string{"proposed", "confirmed", "disputed", "fixed", "dismissed"}, f.Status) {
			return errors.New("invalid finding enum")
		}
		if f.Evidence == nil || (f.Status != "proposed" && len(f.Evidence) == 0) {
			return errors.New("finding lacks evidence")
		}
		if err := validateEvidence(f.Evidence); err != nil {
			return err
		}
	}
	if !slices.Contains([]string{"local_code", "operational", "unknown", "not_applicable"}, r.Diagnosis) || !slices.Contains([]string{"current_change", "pre_existing", "infrastructure", "unknown", "not_applicable"}, r.FailureClass) || !slices.Contains([]string{"pass", "findings", "inconclusive", "not_applicable"}, r.Verdict) {
		return errors.New("invalid report classification")
	}
	return nil
}

func validateEvidence(es []Evidence) error {
	for _, e := range es {
		if !slices.Contains([]string{"file", "artifact", "observation"}, e.Kind) || e.Reference == "" || e.Detail == "" {
			return errors.New("invalid evidence reference")
		}
	}
	return nil
}

// Schema returns the same strict JSON schema to both CLI adapters.
func Schema() string {
	str := map[string]any{"type": "string"}
	en := func(v ...string) map[string]any { return map[string]any{"type": "string", "enum": v} }
	arr := func(v any) map[string]any { return map[string]any{"type": "array", "items": v} }
	obj := func(p map[string]any) map[string]any {
		keys := make([]string, 0, len(p))
		for k := range p {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		return map[string]any{"type": "object", "additionalProperties": false, "properties": p, "required": keys}
	}
	evidence := arr(obj(map[string]any{"kind": en("file", "artifact", "observation"), "reference": str, "detail": str}))
	requirement := obj(map[string]any{"id": str, "description": str, "status": en("satisfied", "unsatisfied", "unknown"), "evidence": evidence})
	finding := obj(map[string]any{"id": str, "severity": en("critical", "high", "medium", "low"), "category": en(Categories()...), "status": en("proposed", "confirmed", "disputed", "fixed", "dismissed"), "problem": str, "impact": str, "suggested_direction": str, "file": str, "line": map[string]any{"type": "integer", "minimum": 0}, "evidence": evidence, "rationale": str})
	p := map[string]any{"schema_version": map[string]any{"type": "integer", "enum": []int{1}}, "step_id": str, "stage": str, "outcome": en("completed", "incomplete", "needs_input"), "summary": str, "markdown": str, "requirements": arr(requirement), "findings": arr(finding), "diagnosis": en("local_code", "operational", "unknown", "not_applicable"), "failure_class": en("current_change", "pre_existing", "infrastructure", "unknown", "not_applicable"), "verdict": en("pass", "findings", "inconclusive", "not_applicable")}
	for _, k := range []string{"missing_evidence", "disagreements", "unresolved_questions", "scope_changes", "artifact_references"} {
		p[k] = arr(str)
	}
	b, err := json.Marshal(obj(p))
	if err != nil {
		return "{}"
	}
	return string(b)
}
