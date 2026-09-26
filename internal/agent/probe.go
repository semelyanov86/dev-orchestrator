package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"dev-orchestrator/internal/report"
)

func probeSchema(reply string) string {
	return fmt.Sprintf(`{"type":"object","additionalProperties":false,"properties":{"reply":{"type":"string","enum":[%q]}},"required":["reply"]}`, reply)
}

// decodeProbeClaude reads one plain completion without a structured-output tool round.
func decodeProbeClaude(output string) (string, error) {
	var envelope struct {
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		return "", err
	}
	if envelope.IsError || envelope.Result == "" {
		return "", errors.New("claude connection probe failed")
	}
	return envelope.Result, nil
}

// probeReport records the observed literal reply locally; the model sends no StepReport.
func (c *CLI) probeReport(req Request, raw string) (report.StepReport, error) {
	var response struct {
		Reply string `json:"reply"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil || response.Reply != req.ProbeReply {
		return report.StepReport{}, errors.New("connection probe returned unexpected reply")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return report.StepReport{}, errors.New("connection probe contains trailing data")
	}
	return report.StepReport{
		SchemaVersion: 1, StepID: req.StepID, Stage: req.Stage, Outcome: "completed",
		Summary: req.ProbeReply, Markdown: req.ProbeReply,
		Requirements: []report.Requirement{{ID: "connection-" + c.Provider, Description: c.Provider + " replies to the connection test", Status: "satisfied", Evidence: []report.Evidence{{Kind: "artifact", Reference: "steps/" + req.StepID + "/output.md", Detail: "Observed reply: " + req.ProbeReply}}}},
		Findings:     []report.Finding{}, MissingEvidence: []string{}, Disagreements: []string{}, UnresolvedQuestions: []string{}, ScopeChanges: []string{}, ArtifactReferences: []string{},
		Diagnosis: "not_applicable", FailureClass: "not_applicable", Verdict: "not_applicable",
	}, nil
}
