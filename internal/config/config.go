// Package config loads typed YAML configuration in documented precedence order.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Agent struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}
type Router struct {
	Type       string             `yaml:"type"`
	Confidence map[string]float64 `yaml:"confidence"`
	NoulYes    float64            `yaml:"noul_yes"`
	NoulNo     float64            `yaml:"noul_no"`
}
type Decisions struct {
	Type        string             `yaml:"type"`
	MaxRequests int                `yaml:"max_requests_per_run"`
	Confidence  map[string]float64 `yaml:"confidence"`
	NoulYes     float64            `yaml:"noul_yes"`
	NoulNo      float64            `yaml:"noul_no"`
}
type Server struct {
	Host        string `yaml:"host"`
	User        string `yaml:"user"`
	Port        int    `yaml:"port"`
	ProjectPath string `yaml:"project_path"`
}
type Config struct {
	Validation struct {
		Command []string      `yaml:"command"`
		Timeout time.Duration `yaml:"timeout"`
	} `yaml:"validation"`
	Agents struct {
		Claude Agent `yaml:"claude"`
		Codex  Agent `yaml:"codex"`
	} `yaml:"agents"`
	Router     Router    `yaml:"router"`
	Decisions  Decisions `yaml:"decisions"`
	OpenRouter struct {
		BaseURL string        `yaml:"base_url"`
		APIKey  string        `yaml:"api_key"`
		Model   string        `yaml:"jev_model"`
		Timeout time.Duration `yaml:"timeout"`
	} `yaml:"openrouter"`
	Review struct {
		MaxIterations int `yaml:"max_iterations"`
		MaxPasses     int `yaml:"max_passes"`
	} `yaml:"review"`
	Workflow struct {
		Timeout                 time.Duration `yaml:"timeout"`
		MaxSteps                int           `yaml:"max_steps"`
		MaxPlanRevisions        int           `yaml:"max_plan_revisions"`
		MaxInvestigationRounds  int           `yaml:"max_investigation_rounds"`
		MaxAnswerRevisions      int           `yaml:"max_answer_revisions"`
		MaxImplementationRounds int           `yaml:"max_implementation_rounds"`
		MaxNoProgressRounds     int           `yaml:"max_no_progress_rounds"`
	} `yaml:"workflow"`
	ValidationRepair struct {
		MaxIterations int `yaml:"max_iterations"`
	} `yaml:"validation_repair"`
	Servers map[string]Server `yaml:"servers"`
}

func DecisionPoints() []string {
	return []string{"task_readiness", "diagnosis", "plan_review", "implementation_result", "validation_failure", "code_review", "answer_review", "review_verification"}
}

// Defaults returns a fresh configuration; maps are never shared between runs.
func Defaults() Config {
	var c Config
	c.Validation.Command = []string{"task", "all"}
	c.Validation.Timeout = 20 * time.Minute
	c.Agents.Claude = Agent{Command: "claude", Timeout: 30 * time.Minute}
	c.Agents.Codex = Agent{Command: "codex", Timeout: 30 * time.Minute}
	c.Router = Router{Type: "jev", Confidence: map[string]float64{"workflow": .75, "complexity": .70, "risk": .80}, NoulYes: .85, NoulNo: .15}
	c.Decisions = Decisions{Type: "jev", MaxRequests: 12, Confidence: map[string]float64{"task_readiness": .80, "diagnosis": .85, "plan_review": .85, "implementation_result": .80, "validation_failure": .85, "code_review": .90, "answer_review": .85, "review_verification": .90}, NoulYes: .85, NoulNo: .15}
	c.OpenRouter.BaseURL = "https://openrouter.ai"
	c.OpenRouter.Model = "~typesafe/jev-latest"
	c.OpenRouter.Timeout = 10 * time.Second
	c.Review.MaxIterations = 2
	c.Review.MaxPasses = 3
	c.Workflow.Timeout = 2 * time.Hour
	c.Workflow.MaxSteps = 40
	c.Workflow.MaxPlanRevisions = 2
	c.Workflow.MaxInvestigationRounds = 2
	c.Workflow.MaxAnswerRevisions = 2
	c.Workflow.MaxImplementationRounds = 2
	c.Workflow.MaxNoProgressRounds = 2
	c.ValidationRepair.MaxIterations = 2
	c.Servers = map[string]Server{}
	return c
}

func GlobalPath(home string) string {
	return filepath.Join(home, ".config", "dev-agent", "config.yaml")
}

// Load preserves explicit zeros and deeply merges maps via decoding into defaults.
func Load(home, project string, environ []string, noJEV bool) (Config, error) {
	c := Defaults()
	defaults, err := yaml.Marshal(c)
	if err != nil {
		return c, err
	}
	merged := map[string]any{}
	if err := yaml.Unmarshal(defaults, &merged); err != nil {
		return c, err
	}
	for _, path := range []string{GlobalPath(home), filepath.Join(project, ".dev-agent.yaml")} {
		if project == "" && path == ".dev-agent.yaml" {
			continue
		}
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return c, fmt.Errorf("read config: %w", err)
		}
		if len(data) > 1<<20 {
			return c, errors.New("config exceeds size limit")
		}
		var supplied Config
		if err := yaml.Unmarshal(data, &supplied); err != nil {
			return c, fmt.Errorf("parse config: %w", err)
		}
		if supplied.OpenRouter.APIKey != "" {
			info, err := os.Stat(path)
			if err != nil {
				return c, fmt.Errorf("stat secret config: %w", err)
			}
			if info.Mode().Perm()&0077 != 0 {
				return c, errors.New("secret config must have private permissions (0600)")
			}
			if filepath.Clean(path) != filepath.Clean(GlobalPath(home)) {
				return c, errors.New("openrouter key must not be stored in project config")
			}
		}
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err := decoder.Decode(&c); err != nil {
			return c, fmt.Errorf("decode config: %w", err)
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			return c, errors.New("config must contain exactly one yaml document")
		}
		patch := map[string]any{}
		if err := yaml.Unmarshal(data, &patch); err != nil {
			return c, err
		}
		mergeMaps(merged, patch)
	}
	data, err := yaml.Marshal(merged)
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("decode merged config: %w", err)
	}
	env := map[string]string{}
	for _, entry := range environ {
		key, val, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = val
		}
	}
	if err := applyEnv(reflect.ValueOf(&c).Elem(), "DEV", env); err != nil {
		return c, err
	}
	if key, ok := env["OPENROUTER_API_KEY"]; ok {
		c.OpenRouter.APIKey = key
	}
	if noJEV {
		c.Router.Type = "manual"
		c.Decisions.Type = "local"
	}
	return c, c.Validate()
}

func mergeMaps(dst, src map[string]any) {
	for k, value := range src {
		next, isMap := value.(map[string]any)
		current, exists := dst[k].(map[string]any)
		if isMap && exists {
			mergeMaps(current, next)
		} else {
			dst[k] = value
		}
	}
}

func applyEnv(v reflect.Value, prefix string, env map[string]string) error {
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		key := prefix + "_" + strings.ToUpper(t.Field(i).Tag.Get("yaml"))
		if field.Kind() == reflect.Struct {
			if err := applyEnv(field, key, env); err != nil {
				return err
			}
			continue
		}
		if field.Kind() == reflect.Map && field.Type().Elem().Kind() == reflect.Float64 {
			for _, k := range field.MapKeys() {
				if raw, ok := env[key+"_"+strings.ToUpper(k.String())]; ok {
					n, err := strconv.ParseFloat(raw, 64)
					if err != nil {
						return fmt.Errorf("parse %s: %w", key, err)
					}
					field.SetMapIndex(k, reflect.ValueOf(n))
				}
			}
			continue
		}
		raw, ok := env[key]
		if !ok {
			continue
		}
		switch {
		case field.Type() == reflect.TypeFor[time.Duration]():
			d, err := time.ParseDuration(raw)
			if err != nil {
				return fmt.Errorf("parse %s: %w", key, err)
			}
			field.SetInt(int64(d))
		case field.Kind() == reflect.String:
			field.SetString(raw)
		case field.Kind() == reflect.Int:
			n, err := strconv.Atoi(raw)
			if err != nil {
				return fmt.Errorf("parse %s: %w", key, err)
			}
			field.SetInt(int64(n))
		case field.Kind() == reflect.Float64:
			n, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return fmt.Errorf("parse %s: %w", key, err)
			}
			field.SetFloat(n)
		case field.Kind() == reflect.Slice:
			var argv []string
			if err := json.Unmarshal([]byte(raw), &argv); err != nil {
				return fmt.Errorf("parse %s argv: %w", key, err)
			}
			field.Set(reflect.ValueOf(argv))
		default:
			return fmt.Errorf("unsupported environment config %s", key)
		}
	}
	return nil
}

func (c Config) Validate() error {
	if !slices.Contains([]string{"jev", "manual"}, c.Router.Type) || !slices.Contains([]string{"jev", "local"}, c.Decisions.Type) {
		return errors.New("invalid router or decisions type")
	}
	timeouts := []time.Duration{c.Validation.Timeout, c.Agents.Claude.Timeout, c.Agents.Codex.Timeout, c.OpenRouter.Timeout, c.Workflow.Timeout}
	for _, d := range timeouts {
		if d <= 0 {
			return errors.New("timeouts must be positive")
		}
	}
	if len(c.Validation.Command) == 0 || c.Validation.Command[0] == "" || c.Agents.Claude.Command == "" || c.Agents.Codex.Command == "" {
		return errors.New("commands must not be empty")
	}
	if c.Workflow.MaxSteps <= 0 || c.Workflow.MaxImplementationRounds <= 0 || c.Workflow.MaxNoProgressRounds <= 0 || c.Review.MaxPasses < 1 {
		return errors.New("step, implementation, no-progress and review pass limits must be positive")
	}
	for _, n := range []int{c.Decisions.MaxRequests, c.Review.MaxIterations, c.Workflow.MaxPlanRevisions, c.Workflow.MaxInvestigationRounds, c.Workflow.MaxAnswerRevisions, c.ValidationRepair.MaxIterations} {
		if n < 0 {
			return errors.New("extra round and request limits must not be negative")
		}
	}
	for _, bounds := range [][2]float64{{c.Router.NoulNo, c.Router.NoulYes}, {c.Decisions.NoulNo, c.Decisions.NoulYes}} {
		if !(bounds[0] >= 0 && bounds[0] < bounds[1] && bounds[1] <= 1) {
			return errors.New("invalid noul thresholds")
		}
	}
	for _, set := range []struct {
		values map[string]float64
		keys   []string
	}{{c.Router.Confidence, []string{"workflow", "complexity", "risk"}}, {c.Decisions.Confidence, DecisionPoints()}} {
		for _, k := range set.keys {
			n, ok := set.values[k]
			if !ok || !(n >= 0 && n <= 1) {
				return fmt.Errorf("invalid or missing confidence threshold %s", k)
			}
		}
		for k := range set.values {
			if !slices.Contains(set.keys, k) {
				return fmt.Errorf("unknown threshold %s", k)
			}
		}
	}
	u, err := url.Parse(c.OpenRouter.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("invalid openrouter base url")
	}
	if u.Scheme == "http" && u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1" {
		return errors.New("openrouter requires https outside localhost")
	}
	if c.OpenRouter.Model == "" {
		return errors.New("openrouter model must not be empty")
	}
	for name, s := range c.Servers {
		if name == "" || s.Host == "" || s.Port < 1 || s.Port > 65535 || !filepath.IsAbs(s.ProjectPath) {
			return fmt.Errorf("invalid server profile %s", name)
		}
	}
	return nil
}

func (c Config) Redacted() ([]byte, error) {
	if c.OpenRouter.APIKey != "" {
		c.OpenRouter.APIKey = "configured"
	} else {
		c.OpenRouter.APIKey = "not configured"
	}
	return yaml.Marshal(c)
}
