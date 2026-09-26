# dev-orchestrator

`dev` — глобальный CLI на Go, который запускает установленные Claude Code и Codex CLI в рабочих Git-проектах. Go engine управляет stages, правами, validation и ограниченными review/fix cycles. Опциональный TypeSafe JEV через OpenRouter рекомендует workflow и разрешённые transitions; без ключа доступны все workflows через manual routing и local decisions.

Требования: [documentation/tz.md](documentation/tz.md). Правила репозитория: [AGENTS.md](AGENTS.md). Реализация находится в `cmd/dev`, `internal/` и embedded `prompts/`; установленному binary исходники оркестратора не нужны.

## Установка

Поддерживается Linux с `bubblewrap` (`bwrap`) и разрешёнными user namespaces. Для сборки нужны Go 1.26.6 или новее, Task и golangci-lint v2. Минимальный patch release закреплён в `go.mod`, который также определяет Go toolchain в CI. Для workflows нужны Git, Task, авторизованные Claude Code и Codex CLI; SSH нужен только для server profiles. Проверенные CLI: Claude Code 2.1.280, Codex 0.157.1. Перед run адаптеры проверяют доступные flags и sandbox; несовместимый CLI прекращает run до writer.

Из каталога исходников:

```bash
task all
task race
task install
```

Binary устанавливается в `~/.local/bin/dev`, сборка остаётся в `build/dev`. Установка идемпотентна и не меняет shell profile. Если каталог отсутствует в PATH:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

В рабочем проекте:

```bash
cd /path/to/your/git-project
dev --help
dev doctor
dev
```

`doctor` проверяет инструменты, config, Git root, offline listing validation target, SSH metadata и при наличии ключа — маленький JEV health request. Он не выполняет `task all`; отсутствие OpenRouter — нормальное состояние. Рабочий проект определяется через Git, включая nested directories и worktrees.

## Запуск

`dev` без аргументов принимает многострочную задачу; завершите её строкой `:done`. Если нужен ручной выбор, перед меню показывается причина. Введите номер или название workflow и нажмите Enter (например, `5` или `research` для вопроса). Статус «Нужен ваш ответ» означает ожидание ввода; индикаторы agents/JEV на это время приостановлены. Explicit commands обходят начальный router:

```bash
dev ping
dev feature "Add bulk cancellation"
dev bug "Duplicate appointments are created"
dev bug --server production "API intermittently returns 502"
dev architecture "Should this become a separate service?"
dev research "Explain the authorization flow"
dev refactor "Simplify payment logic while preserving behavior"
dev tests "Cover concurrent subscription updates"
dev review --base main
dev incident --server production "Workers stopped responding"
dev investigate "Find why queue latency increased; do not fix"
dev plan "Design bulk cancellation without implementation"
dev implement --plan /path/to/plan.md "Implement this plan"
dev docs --docs-path documentation "Update the API documentation"
dev chore "Update CI for the current Go version"
dev fix-review --report /path/to/review.md "Fix confirmed findings"
```

`dev ping` проверяет связь с установленными CLI двумя короткими запросами: Codex отвечает `ping`, Claude — `pong`. JEV, планирование, review, validation и повторные запросы не запускаются. На каждый provider действует timeout не более 30s. Вместо project instructions и полного report schema используется минимальный контракт `{"reply":"ping"}` / `{"reply":"pong"}`; tools выключены, cwd — пустой временный каталог, доступ к проекту остаётся read-only. Внутренний report и artifacts формируются локально из проверенного ответа. Служебный контекст самих CLI всё равно может расходовать tokens; точное количество не гарантируется.

В интерактивном `dev` фразы `проверяю связь`, `проверка связи`, `ping`, `ping pong` и `это просто проверка. Ответь, что ты меня слышишь` выбирают `ping` локально, без JEV и ручного меню. Распознаётся вся фраза целиком; просьба проверить связь и затем изменить код проходит обычный routing.

Для большого текста используйте `--task-file path` или `--task-file -` (stdin до EOF). `--no-jev` отключает initial JEV routing и subsequent JEV decisions. Non-interactive запуск с недостающим workflow, plan/report или обязательным scope сохраняет вопрос и завершает `needs_input`, без ожидания ввода.

`--dry-run` показывает planned stages, права, gates и budgets без subprocess/network. `--quiet` оставляет итог, ошибки и обязательные вопросы; `--verbose` добавляет очищенные сведения об exit status, snapshot, changed paths и report path. Flags взаимоисключающие. `--no-color`, `NO_COLOR` и `TERM=dumb` отключают цвет. При redirect вывод остаётся обычным текстом. Progress/questions идут в stderr, final — в stdout; длительные этапы показывают elapsed time и heartbeat.

Во время interactive run:

- `:update TEXT` сохраняет correction на ближайшей границе этапа и требует свежих plan/review/validation;
- `:restrict TEXT` немедленно останавливает активного writer и завершает run с вопросом о новом ограниченном scope;
- `:question TEXT` сохраняет вопрос, продолжая текущую задачу;
- `:cancel`, Ctrl+C или SIGTERM отменяют run и завершают child process groups.

Уточняющий вопрос engine принимает обычную строку ответа. Расширение permissions не происходит автоматически; resume сохранённого run пока не предусмотрен.

## Workflows

| Workflow | Основная последовательность |
| --- | --- |
| feature | Claude plan → Codex plan review → Claude implementation → validation → Codex review |
| bug | Codex diagnosis → Claude evidence verification → Claude fix → validation → Codex review |
| server-bug | Fixed SSH read-only diagnostics → diagnosis; local writer только для подтверждённого code defect |
| architecture / research | Codex draft → Claude critical review → Codex final |
| refactor | Codex invariants/compatibility plan → critical review → Claude implementation → validation/review |
| tests | Codex test plan → plan review → Claude tests → validation/review |
| review | Codex findings → Claude verification и независимый поиск пропусков → Codex final |
| incident | Независимые parallel Claude/Codex investigations → Codex synthesis |
| investigate | Codex diagnosis → Claude verification → Codex final; полностью read-only |
| plan | Claude plan → Codex review → bounded revisions; без implementation |
| implement | Проверка текущей applicability импортированного plan → implementation/validation/review |
| docs | Claude fact-check/plan → согласованные documentation paths → validation policy → Codex review |
| chore | Codex compatibility/impact plan → Claude tooling/config/dependencies → validation/review |
| fix-review | Codex проверяет импортированные findings → Claude confirmed fixes → validation/re-review |
| ping | Codex `ping` → Claude `pong`; только проверка связи, без JEV/review/validation |

Planning, investigations и reviews имеют native read-only permissions плюс внешний filesystem sandbox. Incident initial reports недоступны другому investigator до synthesis. Optional parallel investigations доступны для diagnosis/research, если router выбрал эту branch. На canonical project root действует межпроцессный lock: максимум один modifying run.

Все code-changing workflows выполняют только configured validation argv, по умолчанию `task all` рабочего проекта. Engine не выбирает framework проекта. После исправления обязательны validation и независимый re-review. Документация без executable examples может получить `not_required`, если project instructions не требуют validation. `--docs-path` повторяемый; default scope — существующие README/docs/documentation. Новый явно указанный файл или каталог создаётся только на writer boundary; source/config остаются read-only.

Structured `StepReport` версии 1 проверяется отдельно от process status. Invalid report допускает одну read-only format repair тем же provider. Writer не подтверждает собственные fixes; omission не закрывает finding. Completion требует acceptance coverage, свежих snapshot/validation/review и отсутствия blockers. `critical`, `high`, `medium` блокируют writing completion; `low` остаётся явной рекомендацией. Research может честно сообщить unknowns, если исследовательский контракт выполнен.

Defaults: review fixes 2, code review/verification passes 3, validation repairs 2, plan/investigation/answer revisions 2, implementation rounds 2, no-progress rounds 2, total steps 40, deadline 2h. Исчерпание обязательного бюджета не считается успехом. Outcomes: `success` → exit 0; `findings`, `unresolved`, `failed` → 1; `needs_input` → 2; Ctrl+C → 130; SIGTERM → 143. Артефакты и terminal status сохраняют тот же outcome/exit code.

## Конфигурация и JEV

Global config: `~/.config/dev-agent/config.yaml`. Project config: `.dev-agent.yaml` в Git root. Полная схема с defaults: [config.example.yaml](config.example.yaml). Priority:

```text
defaults → global → project → environment → CLI flags
```

Все config fields имеют environment override `DEV_` с uppercase path segments: например `DEV_AGENTS_CODEX_TIMEOUT=15m`, `DEV_ROUTER_CONFIDENCE_WORKFLOW=0.85`, `DEV_VALIDATION_COMMAND='["task","all"]'`. `OPENROUTER_API_KEY` приоритетнее `openrouter.api_key` и `DEV_OPENROUTER_API_KEY`. `--no-jev` имеет последний приоритет. `.env` автоматически не загружается; `.env.example` содержит только placeholder.

```bash
export OPENROUTER_API_KEY="your-local-key"
dev config
dev config path
```

Ключ можно хранить в global config с permissions `0600`; в project config он запрещён. `dev config` redacts secrets. Локальные credentials не должны попадать в Git. Config arrays — отдельные executable/argv, без shell interpolation.

JEV использует native [OpenRouter Decisions API](https://openrouter.ai/docs/api/api-reference/alphadecisions/submit-a-decisions-request): `POST https://openrouter.ai/api/alpha/decisions`, `questions`/`state`, primitives `choice`, `score`, `noul`. Это не chat completions. Default model — `~typesafe/jev-latest`, HTTP timeout 10s, общий бюджет initial routing + gates 12 requests. Native score — дробное weighted значение zero-based levels с legend/probabilities; noul — вероятность yes. Engine проверяет shape, consistency, thresholds для каждого вопроса и разрешённые actions.

Missing key, timeout/network error, 4xx/5xx, invalid/uncertain/low-confidence response или exhausted budget переходят к manual/local fallback. Billable POST автоматически не повторяется. JEV получает только допустимый task text и минимальные metadata; code, diff, logs, credentials и customer data ему не передаются. Неоднозначный/чувствительный текст отключает initial provider routing. Subsequent gates передают только typed allowlist counters/enums.

## Безопасность и artifacts

Provider subprocesses используют отдельные argv/stdin, context/deadline и process groups. OpenRouter key исключается из их environment/argv; provider auth остаётся у установленного CLI. Native tools ограничены текущей ролью, сторонние MCP/hooks/plugins отключены. Bubblewrap сохраняет project/source read-only вне явно разрешённого writer scope, Git metadata read-only во всех режимах. Нет auto commits/push/deploy/remote writes.

SSH использует config/agent/keys и fixed quoted read-only команды: uptime, process names/CPU/memory, listening ports, failed services, revision и Git status. Model text не превращается в remote shell command. Произвольные remote commands/remediation не предусмотрены; конкретные logs или дополнительные diagnostics отмечаются как missing evidence/recommended next steps. Настройка SSH profiles необязательна для local workflows.

Artifacts: `~/.local/state/dev-agent/runs/<project>/<run-id>/`, directories `0700`, files `0600`. Здесь находятся `task.md`, `route.json`, `run.json`, `final.md`, `diff.patch`, stage reports/output, validation logs, решения с allowed/recommended/applied actions и fallback reasons. Каждый повторный step имеет собственный ID. Secrets и terminal control sequences очищаются до вывода/сохранения.

Git snapshots учитывают HEAD, index, staged/unstaged/untracked contents, modes и symlinks. `diff.patch` и writer review показывают изменения относительно исходного пользовательского worktree. Для baseline хранится до 32 MiB текста в памяти, до 2 MiB на файл; binary/oversized content явно omitted с snapshot hashes. Truncated Git inventory отклоняется. Artifact root внутри working project запрещён.

```bash
dev history
dev show <run-id>
dev init
```

`init` создаёт минимальный AGENTS.md только при его отсутствии; обычный run instructions не создаёт. `show` читает сохранённый результат, а не возобновляет workflow.

## Проверки

| Task | Назначение |
| --- | --- |
| fmt / fmt-check | gofmt / проверка без edits |
| vet / test / lint | Go vet, isolated unit tests, golangci-lint v2 |
| build / install | build/dev / ~/.local/bin/dev |
| all | Canonical fmt-check + vet + test + lint + build |
| race | Race detection для всех packages |
| clean | Удалить только build/dev |
| smoke | Opt-in реальные read-only Claude/Codex и JEV при наличии key |

Unit tests используют fake agents/routers/validators/remote executor, httptest.Server и isolated Git/process fixtures; настоящие providers/SSH не вызываются. Покрыты workflow transitions, format repair, failure attribution, completion blockers, budgets, independent investigations, correction/cancellation, config merge, redaction, UI и artifacts. CI вызывает `task all`, `task race` и `govulncheck`.

```bash
task smoke
```

Live smoke требует настроенных provider CLI и sandbox; JEV test пропускается без key. Реальный SSH и writing provider smoke в unit/обычную validation не входят. Проверенные версии инструментов и результаты текущей реализации записаны в [documentation/implementation-plan.md](documentation/implementation-plan.md).
