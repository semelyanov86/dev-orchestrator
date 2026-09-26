package jev

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func ptr(n float64) *float64 { return &n }
func TestAskNativeProtocolAndBudgets(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/alpha/decisions" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer synthetic-secret" {
			t.Error("wrong http contract")
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if _, ok := request["messages"]; ok {
			t.Error("chat completions payload")
		}
		if _, ok := request["questions"]; !ok {
			t.Error("native questions missing")
		}
		fmtResponse(w, `{"model":"typesafe/test","answers":{"available":{"type":"noul","noul":0.99}}}`)
	}))
	defer server.Close()
	b := &Budget{Max: 1}
	client := New(server.URL, "synthetic-secret", "~typesafe/jev-latest", time.Second, b)
	response, err := client.Ask(t.Context(), map[string]Question{"available": {Type: "noul", Instructions: "Health"}}, map[string]string{"purpose": "health"})
	if err != nil || response.Model != "typesafe/test" {
		t.Fatalf("Ask %v", err)
	}
	if _, err := client.Ask(t.Context(), map[string]Question{"available": {Type: "noul"}}, nil); err == nil {
		t.Fatal("budget ignored")
	}
	if calls.Load() != 1 || b.Used() != 1 {
		t.Fatal("request budget mismatch")
	}
}
func fmtResponse(w http.ResponseWriter, body string) { _, _ = w.Write([]byte(body)) }
func TestAskFailurePaths(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		status     int
	}{{name: "500", status: 500}, {name: "400", status: 400}, {name: "invalid json", body: "{", status: 200}, {name: "missing model", body: `{"answers":{"q":{"type":"noul","noul":0.9}}}`, status: 200}, {name: "missing value", body: `{"model":"test","answers":{"q":{"type":"noul"}}}`, status: 200}, {name: "unknown question", body: `{"model":"test","answers":{"other":{"type":"noul","noul":0.9}}}`, status: 200}, {name: "oversized", body: strings.Repeat("x", (1<<20)+1), status: 200}, {name: "trailing json", body: `{"model":"test","answers":{"q":{"type":"noul","noul":0.9}}}{}`, status: 200}} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tt.status); fmtResponse(w, tt.body) }))
			defer server.Close()
			c := New(server.URL, "key", "test", time.Second, &Budget{Max: 1})
			if _, err := c.Ask(t.Context(), map[string]Question{"q": {Type: "noul"}}, nil); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}
func TestAskCancellationAndMissingKey(t *testing.T) {
	c := New("http://127.0.0.1:1", "", "test", time.Second, &Budget{Max: 1})
	if _, err := c.Ask(t.Context(), nil, nil); err == nil || c.Budget.Used() != 0 {
		t.Fatal("missing key made request")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	c.APIKey = "key"
	if _, err := c.Ask(ctx, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost %v", err)
	}
}
func TestNativePrimitives(t *testing.T) {
	choice := Answer{Type: "choice", Choice: "bug", Confidence: ptr(.95), Probabilities: map[string]float64{"bug": .9, "feature": .1}}
	if got, err := Choice(choice, []string{"bug", "feature"}, .9); err != nil || got != "bug" {
		t.Fatalf("choice %s %v", got, err)
	}
	choice.Choice = "feature"
	if _, err := Choice(choice, []string{"bug", "feature"}, .9); err == nil {
		t.Fatal("contradictory choice accepted")
	}
	score := Answer{Type: "score", Score: ptr(2.9), Confidence: ptr(.95), Probabilities: map[string]float64{"0": 0, "1": 0, "2": .1, "3": .9}, Legend: map[string]string{"0": "trivial", "1": "low", "2": "medium", "3": "high"}}
	if got, err := Score(score, []string{"trivial", "low", "medium", "high"}, .9); err != nil || got != "high" {
		t.Fatalf("fractional score %s %v", got, err)
	}
	for _, tt := range []struct {
		name  string
		p     float64
		want  bool
		valid bool
	}{{name: "yes threshold", p: .85, want: true, valid: true}, {name: "no threshold", p: .15, valid: true}, {name: "uncertain", p: .5}, {name: "out of bounds", p: 1.1}} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Noul(Answer{Type: "noul", Noul: ptr(tt.p)}, .15, .85)
			if (err == nil) != tt.valid || got != tt.want {
				t.Fatalf("noul %v %v", got, err)
			}
		})
	}
}

func TestHTTPTimeoutAndZeroRequestBudget(t *testing.T) {
	for _, budget := range []int{0, 1} {
		name := "zero requests"
		if budget == 1 {
			name = "timeout"
		}
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer server.Close()
			c := New(server.URL, "synthetic-key", "test", time.Second, &Budget{Max: budget})
			r, err := c.Ask(t.Context(), map[string]Question{"q": {Type: "noul"}}, nil)
			close(release)
			if err == nil || c.Budget.Used() != budget || int(calls.Load()) != budget {
				t.Fatalf("timeout/budget lost: %v calls=%d", err, calls.Load())
			}
			if budget == 1 && (err.Error() != "jev_network_or_timeout" || r.Duration <= 0) {
				t.Fatal("timeout telemetry missing")
			}
		})
	}
}
