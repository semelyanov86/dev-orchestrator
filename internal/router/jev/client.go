// Package jev implements the native OpenRouter Decisions protocol, not chat completions.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}
type Answer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Noul          *float64           `json:"noul,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Legend        map[string]string  `json:"legend,omitempty"`
}
type Response struct {
	Answers  map[string]Answer  `json:"answers"`
	Model    string             `json:"model"`
	Provider string             `json:"provider"`
	Usage    map[string]float64 `json:"usage,omitempty"`
	Duration time.Duration      `json:"duration"`
}

// Budget counts HTTP attempts, shared by initial routing and later decisions.
type Budget struct {
	mu   sync.Mutex
	Max  int
	used int
}

func (b *Budget) Take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used >= b.Max {
		return false
	}
	b.used++
	return true
}
func (b *Budget) Used() int { b.mu.Lock(); defer b.mu.Unlock(); return b.used }

type Client struct {
	HTTP    *http.Client
	BaseURL string
	APIKey  string
	Model   string
	Budget  *Budget
}

func New(baseURL, key, model string, timeout time.Duration, budget *Budget) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: key, Model: model, Budget: budget, HTTP: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("jev redirects are not allowed") }}}
}

// Ask makes one bounded billable request. Failures fall back instead of retrying POST.
func (c *Client) Ask(ctx context.Context, questions map[string]Question, state any) (result Response, err error) {
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if c.APIKey == "" {
		return result, errors.New("missing_api_key")
	}
	b, err := json.Marshal(struct {
		Model     string              `json:"model"`
		Questions map[string]Question `json:"questions"`
		State     any                 `json:"state"`
	}{Model: c.Model, Questions: questions, State: state})
	if err != nil {
		return result, err
	}
	if len(b) > 128<<10 {
		return result, errors.New("jev request exceeds limit")
	}
	if c.Budget == nil || !c.Budget.Take() {
		return result, errors.New("request_budget_exhausted")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/alpha/decisions", bytes.NewReader(b))
	if err != nil {
		return result, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	started := time.Now()
	defer func() { result.Duration = time.Since(started) }()
	response, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, errors.New("jev_network_or_timeout")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return result, fmt.Errorf("jev_http_%d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return result, errors.New("jev_response_read_failed")
	}
	if len(data) > 1<<20 {
		return result, errors.New("jev_response_too_large")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if err := d.Decode(&result); err != nil {
		return result, errors.New("jev_invalid_json")
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return result, errors.New("jev_trailing_json")
	}
	if len(result.Answers) != len(questions) {
		return result, errors.New("jev_missing_or_unknown_questions")
	}
	if result.Model == "" {
		return result, errors.New("jev_missing_model")
	}
	for name, q := range questions {
		a, ok := result.Answers[name]
		if !ok || a.Type != q.Type {
			return result, errors.New("jev_question_type_mismatch")
		}
		switch q.Type {
		case "choice":
			criteria, ok := q.Criteria.(map[string]string)
			if !ok {
				return result, errors.New("invalid choice question criteria")
			}
			keys := make([]string, 0, len(criteria))
			for k := range criteria {
				keys = append(keys, k)
			}
			if _, err := Choice(a, keys, 0); err != nil {
				return result, err
			}
		case "score":
			levels, ok := q.Criteria.([]string)
			if !ok {
				return result, errors.New("invalid score criteria")
			}
			if _, err := Score(a, levels, 0); err != nil {
				return result, err
			}
		case "noul":
			if a.Noul == nil || !probability(*a.Noul) {
				return result, errors.New("invalid native noul")
			}
		default:
			return result, errors.New("unknown question type")
		}
	}
	safeUsage := map[string]float64{}
	for _, k := range []string{"input_tokens", "output_tokens", "cost"} {
		if n, ok := result.Usage[k]; ok && finite(n) && n >= 0 {
			safeUsage[k] = n
		}
	}
	result.Usage = safeUsage
	result.Duration = time.Since(started)
	return result, nil
}

func finite(n float64) bool      { return !math.IsNaN(n) && !math.IsInf(n, 0) }
func probability(n float64) bool { return finite(n) && n >= 0 && n <= 1 }
func distribution(values map[string]float64, keys []string) error {
	if len(values) != len(keys) {
		return errors.New("invalid probability keys")
	}
	sum := 0.0
	for _, k := range keys {
		n, ok := values[k]
		if !ok || !probability(n) {
			return errors.New("invalid probability")
		}
		sum += n
	}
	if math.Abs(sum-1) > 0.02 {
		return errors.New("probabilities do not sum to one")
	}
	return nil
}
func Choice(a Answer, choices []string, threshold float64) (string, error) {
	if a.Type != "choice" || a.Confidence == nil || !probability(*a.Confidence) || *a.Confidence < threshold || !slices.Contains(choices, a.Choice) {
		return "", errors.New("invalid_or_low_confidence_choice")
	}
	if err := distribution(a.Probabilities, choices); err != nil {
		return "", err
	}
	for _, n := range a.Probabilities {
		if n > a.Probabilities[a.Choice]+1e-6 {
			return "", errors.New("choice contradicts probabilities")
		}
	}
	return a.Choice, nil
}
func Score(a Answer, levels []string, threshold float64) (string, error) {
	if a.Type != "score" || a.Score == nil || a.Confidence == nil || !probability(*a.Confidence) || *a.Confidence < threshold || !finite(*a.Score) || *a.Score < 0 || *a.Score > float64(len(levels)-1) {
		return "", errors.New("invalid_or_low_confidence_score")
	}
	keys := make([]string, len(levels))
	for i := range levels {
		keys[i] = strconv.Itoa(i)
	}
	if err := distribution(a.Probabilities, keys); err != nil {
		return "", err
	}
	if len(a.Legend) != len(levels) {
		return "", errors.New("invalid score legend")
	}
	expected := 0.0
	dominant := 0
	for i, k := range keys {
		if !strings.EqualFold(a.Legend[k], levels[i]) {
			return "", errors.New("score legend mismatch")
		}
		expected += float64(i) * a.Probabilities[k]
		if a.Probabilities[k] > a.Probabilities[strconv.Itoa(dominant)] {
			dominant = i
		}
	}
	if math.Abs(expected-*a.Score) > .05 {
		return "", errors.New("score contradicts probabilities")
	}
	return levels[dominant], nil
}
func Noul(a Answer, no, yes float64) (bool, error) {
	if a.Type != "noul" || a.Noul == nil || !probability(*a.Noul) {
		return false, errors.New("invalid_noul")
	}
	if *a.Noul >= yes {
		return true, nil
	}
	if *a.Noul <= no {
		return false, nil
	}
	return false, errors.New("uncertain_noul")
}
