// Package agent adapts installed CLI providers to the engine's common contract.
package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dev-orchestrator/internal/project"
	"dev-orchestrator/internal/report"
	"dev-orchestrator/internal/runner"
	"dev-orchestrator/internal/safety"
)

type Mode string

const (
	ReadOnly Mode = "read_only"
	Write    Mode = "local_write"
	DocsOnly Mode = "documentation_only"
)

type Request struct {
	ProjectDir string
	RunDir     string
	StepID     string
	Stage      string
	Prompt     string
	Mode       Mode
	Timeout    time.Duration
	DocsPaths  []string
	ProbeReply string // A fixed ping/pong response; uses the minimal connection-test contract.
}
type Result struct {
	Report      report.StepReport
	Output      string
	ErrorOutput string
	ExitCode    int
	ReportError string
}
type Agent interface {
	Run(context.Context, Request) (Result, error)
}
type Executor interface {
	Run(context.Context, runner.Request) (runner.Result, error)
}
type CLI struct {
	Provider  string
	Command   string
	Exec      Executor
	Home      string
	StateRoot string
	Cleaner   safety.Cleaner
}

// Preflight verifies actual installed capabilities and the outer filesystem sandbox.
func (c *CLI) Preflight(ctx context.Context, dir string) error {
	help, err := c.Exec.Run(ctx, runner.Request{Command: c.Command, Args: []string{"--help"}, Dir: dir, Timeout: 10 * time.Second})
	if err != nil {
		return err
	}
	required := []string{"--restricted", "--json-schema", "--strict-mcp-config"}
	if c.Provider == "codex" {
		help, err = c.Exec.Run(ctx, runner.Request{Command: c.Command, Args: []string{"exec", "--help"}, Dir: dir, Timeout: 10 * time.Second})
		if err != nil {
			return err
		}
		required = []string{"--output-schema", "--ignore-user-config", "--sandbox"}
	}
	for _, flag := range required {
		if !strings.Contains(help.Stdout, flag) {
			return fmt.Errorf("%s lacks required capability %s", c.Provider, flag)
		}
	}
	return CheckSandbox(ctx, c.Exec, dir, c.Cleaner)
}

// CheckSandbox probes filesystem isolation without launching a provider or writing to the project.
func CheckSandbox(ctx context.Context, executor Executor, dir string, cleaner safety.Cleaner) error {
	result, err := executor.Run(ctx, runner.Request{
		Command: "bwrap",
		Args: []string{
			"--unshare-user", "--unshare-pid", "--die-with-parent",
			"--ro-bind", "/", "/", "--proc", "/proc", "--dev", "/dev", "/bin/true",
		},
		Dir: dir, Timeout: 10 * time.Second,
	})
	if err != nil {
		detail := stderrSummary(result.Stderr, cleaner)
		if detail != "" {
			return fmt.Errorf("filesystem sandbox unavailable: %w; bwrap stderr: %s; check user namespace/AppArmor permissions (README: Sandbox setup)", err, detail)
		}
		return fmt.Errorf("filesystem sandbox unavailable: %w; check bubblewrap installation and user namespace/AppArmor permissions (README: Sandbox setup)", err)
	}
	return nil
}

func stderrSummary(stderr string, cleaner safety.Cleaner) string {
	detail := strings.TrimSpace(cleaner.Clean(stderr))
	if len(detail) > 2048 {
		return detail[:2048] + " [truncated]"
	}
	return detail
}

func (c *CLI) Run(ctx context.Context, req Request) (Result, error) {
	var result Result
	result.ExitCode = -1
	if c.Provider == "codex" && req.Mode != ReadOnly {
		return result, errors.New("codex workflow roles are read-only")
	}
	if req.ProbeReply != "" && (req.Mode != ReadOnly || (req.ProbeReply != "ping" && req.ProbeReply != "pong")) {
		return result, errors.New("connection probe requires a read-only ping/pong reply")
	}
	runtimeDir, err := os.MkdirTemp("", "dev-agent-step-*")
	if err != nil {
		return result, err
	}
	defer func() { _ = os.RemoveAll(runtimeDir) }()
	schema := report.Schema()
	if req.ProbeReply != "" {
		schema = probeSchema(req.ProbeReply)
		if err := os.WriteFile(filepath.Join(runtimeDir, "instructions.md"), []byte("Connection test. Return only the requested JSON reply. Do not use tools."), 0600); err != nil {
			return result, err
		}
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "schema.json"), []byte(schema), 0600); err != nil {
		return result, err
	}
	args, err := c.arguments(req)
	if err != nil {
		return result, err
	}
	sandbox, err := c.sandbox(req, runtimeDir)
	if err != nil {
		return result, err
	}
	if req.ProbeReply != "" {
		sandbox = append(sandbox, "--dir", "/tmp/probe", "--chdir", "/tmp/probe")
		for _, path := range []string{filepath.Join(c.Home, ".codex", "skills"), filepath.Join(c.Home, ".agents", "skills")} {
			if info, err := os.Stat(path); err == nil && info.IsDir() {
				sandbox = append(sandbox, "--tmpfs", path)
			}
		}
	}
	sandbox = append(sandbox, c.Command)
	sandbox = append(sandbox, args...)
	output, runErr := c.Exec.Run(ctx, runner.Request{Command: "bwrap", Args: sandbox, Dir: req.ProjectDir, Input: req.Prompt, Timeout: req.Timeout})
	result.Output = c.Cleaner.Clean(output.Stdout)
	result.ErrorOutput = c.Cleaner.Clean(output.Stderr)
	result.ExitCode = output.ExitCode
	if runErr != nil {
		if detail := stderrSummary(output.Stderr, c.Cleaner); detail != "" {
			return result, fmt.Errorf("%s agent process failed: %w; stderr: %s", c.Provider, runErr, detail)
		}
		return result, runErr
	}
	if output.Truncated {
		return result, errors.New("provider output exceeds size limit")
	}
	var raw string
	if req.ProbeReply != "" && c.Provider == "claude" {
		raw, err = decodeProbeClaude(output.Stdout)
	} else {
		raw, err = decodeOutput(c.Provider, output.Stdout)
	}
	if err != nil {
		if req.ProbeReply != "" {
			return result, fmt.Errorf("connection probe response: %w", err)
		}
		result.ReportError = err.Error()
		return result, nil
	}
	if req.ProbeReply != "" {
		result.Report, err = c.probeReport(req, raw)
		return result, err
	}
	result.Report, err = report.Decode(raw, req.StepID, req.Stage)
	if err != nil {
		result.ReportError = err.Error()
		return result, nil
	}
	// Clean structured string fields without corrupting JSON quotes or schema types.
	result.Report = cleanReport(result.Report, c.Cleaner)
	return result, nil
}

func cleanReport(r report.StepReport, c safety.Cleaner) report.StepReport {
	r.Summary = c.Clean(r.Summary)
	r.Markdown = c.Clean(r.Markdown)
	for i := range r.Requirements {
		r.Requirements[i].Description = c.Clean(r.Requirements[i].Description)
		for j := range r.Requirements[i].Evidence {
			r.Requirements[i].Evidence[j].Detail = c.Clean(r.Requirements[i].Evidence[j].Detail)
		}
	}
	for i := range r.Findings {
		f := &r.Findings[i]
		f.Problem = c.Clean(f.Problem)
		f.Impact = c.Clean(f.Impact)
		f.SuggestedDirection = c.Clean(f.SuggestedDirection)
		f.Rationale = c.Clean(f.Rationale)
		for j := range f.Evidence {
			f.Evidence[j].Detail = c.Clean(f.Evidence[j].Detail)
		}
	}
	for _, list := range [][]string{r.MissingEvidence, r.Disagreements, r.UnresolvedQuestions, r.ScopeChanges} {
		for i := range list {
			list[i] = c.Clean(list[i])
		}
	}
	return r
}

func (c *CLI) arguments(req Request) ([]string, error) {
	if c.Provider == "claude" {
		tools := "Read,Glob,Grep"
		mode := "plan"
		if req.Mode != ReadOnly {
			tools += ",Edit,Write"
			mode = "acceptEdits"
		}
		if req.Mode == DocsOnly {
			mode = "dontAsk"
		}
		if req.ProbeReply != "" {
			tools = ""
			mode = "dontAsk"
		}
		args := []string{"--print", "--input-format", "text", "--output-format", "json", "--no-session-persistence", "--restricted", "--tools", tools, "--permission-mode", mode, "--permission-prompts", "none", "--mcp-config", `{"mcpServers":{}}`, "--strict-mcp-config", "--settings", `{"disableAllHooks":true}`, "--no-chrome"}
		if req.ProbeReply != "" {
			args = append(args, "--safe-mode", "--effort", "low", "--system-prompt", "Connection test. Return only the requested JSON reply.")
		} else {
			args = append(args, "--json-schema", report.Schema())
		}
		if req.Mode == DocsOnly {
			if len(req.DocsPaths) == 0 {
				return nil, errors.New("docs writer requires explicit paths")
			}
			args = append(args, "--allowedTools")
			for _, path := range req.DocsPaths {
				if strings.ContainsAny(path, "*?[]()\\") {
					return nil, errors.New("documentation path contains permission pattern metacharacters")
				}
				pattern := "/" + filepath.ToSlash(path)
				info, err := os.Stat(filepath.Join(req.ProjectDir, path))
				if (err == nil && info.IsDir()) || (errors.Is(err, os.ErrNotExist) && filepath.Ext(path) == "") {
					pattern += "/**"
				}
				args = append(args, "Edit("+pattern+")")
			}
		}
		return args, nil
	}
	if c.Provider != "codex" {
		return nil, errors.New("unknown agent provider")
	}
	dir := req.ProjectDir
	if req.ProbeReply != "" {
		dir = "/tmp/probe"
	}
	args := []string{"--no-daemon", "--ask-for-approval", "never", "exec", "--sandbox", "read-only", "--cd", dir, "--json", "--color", "never", "--ephemeral", "--ignore-user-config", "--ignore-rules", "--output-schema", "/tmp/schema.json"}
	for _, flag := range []string{"hooks", "plugins", "apps", "multi_agent", "browser_use", "browser_use_external", "browser_use_full_cdp_access", "in_app_browser"} {
		args = append(args, "--disable", flag)
	}
	args = append(args, "-c", `mcp_servers={}`) // Config files are masked by the outer sandbox as well.
	if req.ProbeReply != "" {
		args = append(args, "--skip-git-repo-check", "--disable", "shell_tool", "--disable", "unified_exec", "--disable", "code_mode_host", "--disable", "memories", "--disable", "skill_search", "--disable", "computer_use", "--disable", "image_generation", "--disable", "view_image", "--enable", "skip_host_skill_discovery", "-c", `project_doc_max_bytes=0`, "-c", `model_instructions_file="/tmp/instructions.md"`, "-c", `model_reasoning_effort="low"`)
	}
	return append(args, "-"), nil
}

func (c *CLI) sandbox(req Request, runtimeDir string) ([]string, error) {
	emptyConfig := filepath.Join(runtimeDir, "empty-config")
	if err := os.WriteFile(emptyConfig, nil, 0600); err != nil {
		return nil, fmt.Errorf("prepare sandbox config mask: %w", err)
	}
	args := []string{"--unshare-user", "--unshare-pid", "--die-with-parent", "--ro-bind", "/", "/", "--proc", "/proc", "--dev", "/dev", "--bind", runtimeDir, "/tmp", "--setenv", "TMPDIR", "/tmp", "--chdir", req.ProjectDir}
	// Provider authentication/runtime storage remains available; project/artifact permissions are separate.
	for _, path := range []string{filepath.Join(c.Home, "."+c.Provider)} {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			args = append(args, "--bind", path, path)
		}
	}
	// Re-expose the project after /tmp isolation, including projects living in /tmp.
	args = append(args, "--ro-bind", req.ProjectDir, req.ProjectDir)
	other := "claude"
	if c.Provider == "claude" {
		other = "codex"
	}
	for _, path := range []string{filepath.Join(c.Home, "."+other), filepath.Join(c.Home, ".ssh")} {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			args = append(args, "--tmpfs", path)
		}
	}
	if req.Mode == Write {
		args = append(args, "--bind", req.ProjectDir, req.ProjectDir)
	}
	if req.Mode == DocsOnly {
		for _, path := range req.DocsPaths {
			clean, err := project.ResolveScope(req.ProjectDir, path)
			if err != nil {
				return nil, err
			}
			absolute := filepath.Join(req.ProjectDir, clean)
			if _, err := os.Stat(absolute); errors.Is(err, os.ErrNotExist) {
				if err := os.MkdirAll(filepath.Dir(absolute), 0755); err != nil {
					return nil, err
				}
				if filepath.Ext(clean) == "" {
					err = os.Mkdir(absolute, 0755)
				} else {
					var f *os.File
					f, err = os.OpenFile(absolute, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
					if err == nil {
						err = f.Close()
					}
				}
				if err != nil {
					return nil, fmt.Errorf("prepare documentation scope: %w", err)
				}
			} else if err != nil {
				return nil, err
			}
			args = append(args, "--bind", absolute, absolute)
		}
	}
	// Git metadata is read-only in every mode; inspect without allowing index/commit changes.
	gitPath := filepath.Join(req.ProjectDir, ".git")
	if _, err := os.Lstat(gitPath); err == nil {
		args = append(args, "--ro-bind", gitPath, gitPath)
	}
	// Use a regular empty file: /dev/null bind mounts cannot be read on nodev mounts.
	// Mask every project/ancestor Codex config: empty map overrides alone do not disable MCP.
	for dir := req.ProjectDir; ; dir = filepath.Dir(dir) {
		path := filepath.Join(dir, ".codex", "config.toml")
		if _, err := os.Stat(path); err == nil {
			args = append(args, "--ro-bind", emptyConfig, path)
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	for _, path := range []string{filepath.Join(c.Home, ".codex", "config.toml"), filepath.Join(c.Home, ".config", "dev-agent", "config.yaml"), filepath.Join(req.ProjectDir, ".dev-agent.yaml"), filepath.Join(req.ProjectDir, ".env")} {
		if _, err := os.Stat(path); err == nil {
			args = append(args, "--ro-bind", emptyConfig, path)
		}
	}
	// Initial incident agents cannot inspect another agent's run artifacts before synthesis.
	if info, err := os.Stat(c.StateRoot); err == nil && info.IsDir() {
		args = append(args, "--tmpfs", c.StateRoot)
	}
	return args, nil
}

func decodeOutput(provider, output string) (string, error) {
	if provider == "claude" {
		var envelope struct {
			IsError    bool            `json:"is_error"`
			Structured json.RawMessage `json:"structured_output"`
		}
		if err := json.Unmarshal([]byte(output), &envelope); err != nil {
			return "", err
		}
		if envelope.IsError {
			return "", errors.New("claude reported an error")
		}
		if len(envelope.Structured) == 0 || string(envelope.Structured) == "null" {
			return "", errors.New("claude structured output missing")
		}
		return string(envelope.Structured), nil
	}
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 4096), 4<<20)
	var final string
	for scanner.Scan() {
		var event struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return "", err
		}
		if event.Type == "turn.failed" || event.Type == "error" {
			return "", errors.New("codex turn failed")
		}
		if event.Type == "item.completed" && event.Item.Type == "agent_message" {
			final = event.Item.Text
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if final == "" {
		return "", errors.New("codex final report missing")
	}
	return final, nil
}
