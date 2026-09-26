package decision

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dev-orchestrator/internal/config"
	"dev-orchestrator/internal/router/jev"
)

func fp(n float64) *float64 { return &n }
func TestJevDecisionEveryPoint(t *testing.T) {
	for _, point := range config.DecisionPoints() {
		for _, kind := range []string{"accepted", "low confidence", "500", "missing key", "uncertain noul", "contradictory"} {
			t.Run(point+"/"+kind, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var request map[string]any
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
					}
					if kind == "500" {
						w.WriteHeader(500)
						return
					}
					confidence := .95
					if kind == "low confidence" {
						confidence = .1
					}
					noul := .95
					if kind == "uncertain noul" {
						noul = .5
					}
					if kind == "contradictory" {
						noul = .1
					}
					_ = json.NewEncoder(w).Encode(jev.Response{Model: "test", Answers: map[string]jev.Answer{"next_action": {Type: "choice", Choice: "proceed", Confidence: fp(confidence), Probabilities: map[string]float64{"proceed": 1, "clarify": 0}}, "evidence_sufficient": {Type: "noul", Noul: fp(noul)}}})
				}))
				defer server.Close()
				key := "synthetic"
				if kind == "missing key" {
					key = ""
				}
				j := JevDecisionService{Client: jev.New(server.URL, key, "test", time.Second, &jev.Budget{Max: 12}), Config: config.Defaults().Decisions}
				d, err := j.Decide(t.Context(), DecisionInput{Point: point, State: SafeState{Workflow: "feature", Outcome: "completed", Categories: []string{}}, AllowedActions: []Action{Proceed, Clarify}})
				if err != nil {
					t.Fatal(err)
				}
				want := "local"
				if kind == "accepted" {
					want = "jev"
				}
				if d.Source != want {
					t.Fatalf("source %s want %s reason %s", d.Source, want, d.FallbackReason)
				}
				if kind == "low confidence" && d.Telemetry == nil {
					t.Fatal("rejected typed response not retained")
				}
			})
		}
	}
}
func TestDecisionPayloadAllowlist(t *testing.T) {
	input := DecisionInput{Point: "code_review", State: SafeState{Workflow: "feature", Stage: "review", Categories: []string{"correctness"}, Findings: FindingCounts{Blocking: 1}, Remaining: Remaining{FixIterations: 2}}, AllowedActions: []Action{Fix, Verify}}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"problem", "file", "path", "stdout", "stderr", "markdown", "command", "task", "detail"} {
		if strings.Contains(string(data), `"`+name+`"`) {
			t.Fatalf("unsafe payload key %s", name)
		}
	}
}
func TestLocalDecisionSafety(t *testing.T) {
	for _, tt := range []struct {
		name, point string
		state       SafeState
		allowed     []Action
		want        Action
	}{{name: "clean review", point: "code_review", allowed: []Action{Complete}, want: Complete}, {name: "confirmed blocker", point: "code_review", state: SafeState{Findings: FindingCounts{Blocking: 1}}, allowed: []Action{Fix, Stop}, want: Fix}, {name: "disputed", point: "code_review", state: SafeState{Findings: FindingCounts{Disputed: 1}}, allowed: []Action{Verify, Stop}, want: Verify}, {name: "current failure", point: "validation_failure", state: SafeState{FailureClass: "current_change"}, allowed: []Action{Fix, Stop}, want: Fix}, {name: "preexisting failure", point: "validation_failure", state: SafeState{FailureClass: "pre_existing"}, allowed: []Action{Stop}, want: Stop}, {name: "missing scope", point: "task_readiness", state: SafeState{Questions: true}, allowed: []Action{Clarify, Proceed}, want: Clarify}} {
		t.Run(tt.name, func(t *testing.T) {
			d, err := (LocalDecisionService{}).Decide(t.Context(), DecisionInput{Point: tt.point, State: tt.state, AllowedActions: tt.allowed})
			if err != nil || d.Action != tt.want {
				t.Fatalf("action %s err %v want %s", d.Action, err, tt.want)
			}
		})
	}
}
