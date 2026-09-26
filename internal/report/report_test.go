package report

import (
	"encoding/json"
	"strings"
	"testing"
)

func validReport() StepReport {
	return StepReport{SchemaVersion: 1, StepID: "01", Stage: "review", Outcome: "completed", Summary: "review done", Markdown: "Evidence-based result", Requirements: []Requirement{{ID: "task", Description: "task criterion", Status: "satisfied", Evidence: []Evidence{{Kind: "observation", Reference: "contract", Detail: "covered"}}}}, Findings: []Finding{}, MissingEvidence: []string{}, Disagreements: []string{}, UnresolvedQuestions: []string{}, ScopeChanges: []string{}, ArtifactReferences: []string{}, Diagnosis: "not_applicable", FailureClass: "not_applicable", Verdict: "pass"}
}
func TestDecodeStrictContract(t *testing.T) {
	r := validReport()
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(string(data), "01", "review"); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ name, input string }{{name: "empty", input: ""}, {name: "unknown field", input: strings.TrimSuffix(string(data), "}") + `,"invented":true}`}, {name: "trailing document", input: string(data) + "{}"}, {name: "wrong id", input: strings.Replace(string(data), `"step_id":"01"`, `"step_id":"02"`, 1)}, {name: "missing collections", input: strings.Replace(string(data), `"findings":[]`, `"findings":null`, 1)}, {name: "empty acceptance", input: strings.Replace(string(data), `"requirements":[`, `"requirements":[`, 1) + "invalid"}} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Decode(tt.input, "01", "review"); err == nil {
				t.Fatal("invalid report accepted")
			}
		})
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(Schema()), &schema); err != nil {
		t.Fatal(err)
	}
	if schema["additionalProperties"] != false {
		t.Fatal("schema permits arbitrary facts")
	}
}
