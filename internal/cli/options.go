// Package cli wires the installed application with injectable I/O.
package cli

import (
	"errors"
	"fmt"
	"strings"

	"dev-orchestrator/internal/workflow"
)

type Options struct {
	Command   string
	Task      string
	Args      []string
	TaskFile  string
	Base      string
	Server    string
	Plan      string
	Report    string
	DocsPaths []string
	NoJEV     bool
	DryRun    bool
	Quiet     bool
	Verbose   bool
	NoColor   bool
	Help      bool
}

func Parse(args []string) (Options, error) {
	var o Options
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}
		name, value, hasValue := strings.Cut(arg, "=")
		switch name {
		case "--help", "-h":
			o.Help = true
		case "--no-jev":
			o.NoJEV = true
		case "--dry-run":
			o.DryRun = true
		case "--quiet":
			o.Quiet = true
		case "--verbose":
			o.Verbose = true
		case "--no-color":
			o.NoColor = true
		case "--task-file", "--base", "--server", "--plan", "--report", "--docs-path":
			if !hasValue {
				i++
				if i >= len(args) {
					return o, fmt.Errorf("flag %s needs a value", name)
				}
				value = args[i]
			}
			if value == "" {
				return o, fmt.Errorf("empty flag %s", name)
			}
			switch name {
			case "--task-file":
				o.TaskFile = value
			case "--base":
				o.Base = value
			case "--server":
				o.Server = value
			case "--plan":
				o.Plan = value
			case "--report":
				o.Report = value
			case "--docs-path":
				o.DocsPaths = append(o.DocsPaths, value)
			}
		default:
			return o, fmt.Errorf("unknown flag %s", name)
		}
		if hasValue && (name == "--quiet" || name == "--verbose" || name == "--dry-run" || name == "--no-jev" || name == "--no-color" || name == "--help") {
			return o, fmt.Errorf("boolean flag %s takes no value", name)
		}
	}
	if o.Quiet && o.Verbose {
		return o, errors.New("--quiet and --verbose are mutually exclusive")
	}
	if len(positional) > 0 {
		first := positional[0]
		if d, ok := workflow.Lookup(first); ok {
			o.Command = d.Name
			positional = positional[1:]
		} else {
			switch first {
			case "init", "doctor", "history", "show", "config", "version":
				o.Command = first
				o.Args = positional[1:]
				positional = nil
			default:
				return o, fmt.Errorf("unknown command %q; use dev --help", first)
			}
		}
	}
	o.Task = strings.Join(positional, " ")
	if o.TaskFile != "" && o.Task != "" {
		return o, errors.New("--task-file and positional task are mutually exclusive")
	}
	if o.Server != "" && o.Command == "bug" {
		o.Command = "server_bug"
	}
	if o.Server != "" && o.Command != "" && o.Command != "server_bug" && o.Command != "incident" && o.Command != "investigate" {
		return o, errors.New("--server is only supported for bug, incident and investigate")
	}
	if o.Base != "" && o.Command != "review" {
		return o, errors.New("--base requires review")
	}
	if o.Plan != "" && o.Command != "implement" {
		return o, errors.New("--plan requires implement")
	}
	if o.Report != "" && o.Command != "fix_review" {
		return o, errors.New("--report requires fix-review")
	}
	if len(o.DocsPaths) > 0 && o.Command != "docs" {
		return o, errors.New("--docs-path requires docs")
	}
	return o, nil
}

func Help() string {
	var b strings.Builder
	b.WriteString("dev — оркестратор Claude Code и Codex CLI\n\nЗапуск из рабочего Git project:\n  dev\n  dev <workflow> [flags] \"task\"\n\nWorkflows:\n")
	for _, d := range workflow.Catalog() {
		fmt.Fprintf(&b, "  %-14s %s\n", d.CLI, d.Label)
	}
	b.WriteString("\nСлужебные команды: init, doctor, history, show <run-id>, config [path], version\n\nFlags:\n  --task-file <path|->  Длинная задача из файла/stdin\n  --server <profile>   Remote read-only diagnostics\n  --base <revision>    Review относительно pinned base\n  --plan <path>        Вход implement\n  --report <path>      Вход fix-review\n  --docs-path <path>   Разрешённый documentation path (повторяемый)\n  --no-jev             Manual routing и local decisions\n  --dry-run            Маршрут и budgets без subprocess/network\n  --quiet              Итог, ошибки, обязательные вопросы\n  --verbose            Очищенные подробности\n  --no-color           Без цвета\n\nМногострочный ввод заканчивается отдельной строкой :done.\nВо время interactive run: :update TEXT, :restrict TEXT, :question TEXT, :cancel.\nArtifacts: ~/.local/state/dev-agent/runs/\n")
	return b.String()
}
