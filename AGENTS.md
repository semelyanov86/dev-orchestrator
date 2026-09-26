# Инструкции для агентов

## Контекст и приоритеты

Этот репозиторий содержит исходники глобального CLI `dev` на Go. Он координирует Claude Code, Codex CLI, optional TypeSafe JEV через OpenRouter Decisions API, Git, Task и SSH в других рабочих проектах. Установленные Claude/Codex CLI не заменяются их API.

Перед задачей прочитай [documentation/tz.md](documentation/tz.md), эти инструкции и актуальный [README.md](README.md). Объём работы определяет текущий запрос пользователя: чтение ТЗ не означает поручение реализовать все его разделы в рамках любой задачи.

Явные инструкции пользователя имеют приоритет. ТЗ задаёт продуктовые требования; этот файл уточняет правила работы в репозитории. При конфликте с общими рекомендациями установленных skills применяй требования проекта, изложенные здесь. Не меняй ТЗ ради упрощения реализации.

Мелкие обратимые архитектурные решения принимай самостоятельно по ТЗ. Не запрашивай повторное разрешение на уже порученную работу. Обсуди с пользователем реальное противоречие требований или действие, выходящее за разрешённый scope.

В начале разработки здесь только Go-каркас и Taskfile с задачей `default`. Проверяй фактическое состояние: не утверждай, что `dev`, `task all`, config или установка уже работают, пока это не подтверждено. README должен соответствовать реализации.

## Начало работы

1. Проверь cwd, структуру и ближайшие применимые инструкции. При работе самого `dev` working project определяется через `git rev-parse --show-toplevel`; не используй каталог исходников оркестратора вместо него.
2. Если Git repository обнаружен, прочитай branch, `git status --short`, staged/unstaged diff и untracked paths. Сохрани исходное состояние для сравнения. Если Git ещё не инициализирован, сообщи об этом; не создавай репозиторий без поручения. `.git` может быть файлом в worktree.
3. Проверь `go.mod`, реальный `go version`, Taskfile, конфигурацию lint и тесты. Сейчас `go.mod` задаёт Go 1.26; module path `dev-orchestrator` не заменяй выдуманным remote URL.
4. Прочитай подходящие `SKILL.md` и нужные references из таблицы ниже. Сообщи кратко, какие skills применяешь.
5. Исследуй существующие контракты и callers, затем делай минимальные изменения, соответствующие задаче.

Общайся с пользователем на русском, если он не просит другой язык. Идентификаторы, CLI commands, config keys, Go comments и error strings пиши на английском.

## Установленные skills

Источник skills — `.agents/skills/`; `.claude/skills/` содержит ссылки на те же каталоги. `skills-lock.json` фиксирует источники и hashes. Сохраняй skills, references, assets, scripts, symlinks и lockfile; не переустанавливай и не редактируй их без поручения.

Перед Go-изменениями используй базовые `golang-patterns`, `golang-code-style`, `golang-naming`, `golang-error-handling` и `golang-testing`. При изменении CLI также используй `golang-cli`; остальные выбирай по задаче. Прочитанный skill не нужно перечитывать в рамках той же задачи без причины.

| Skill | Когда применять |
| --- | --- |
| [go-code-review](.agents/skills/go-code-review/SKILL.md) | Review Go-изменений: checklist и проверка findings |
| [golang-cli](.agents/skills/golang-cli/SKILL.md) | CLI commands, flags, config precedence, I/O, exit codes, signals |
| [golang-code-style](.agents/skills/golang-code-style/SKILL.md) | Ясный control flow, declarations и организация функций |
| [golang-concurrency](.agents/skills/golang-concurrency/SKILL.md) | Goroutines, channels, synchronization, shutdown и race safety |
| [golang-continuous-integration](.agents/skills/golang-continuous-integration/SKILL.md) | CI checks, automation и releases в рамках порученной задачи |
| [golang-data-structures](.agents/skills/golang-data-structures/SKILL.md) | Slices/maps, ownership, copying и выбор структур данных |
| [golang-design-patterns](.agents/skills/golang-design-patterns/SKILL.md) | Constructors, небольшие interfaces, lifecycle и timeout |
| [golang-error-handling](.agents/skills/golang-error-handling/SKILL.md) | Error wrapping, inspection, single handling и logging |
| [golang-lint](.agents/skills/golang-lint/SKILL.md) | Linter config, diagnostics и обоснованные suppressions |
| [golang-modernize](.agents/skills/golang-modernize/SKILL.md) | Версионные изменения, совместимые с `go.mod`, в scope задачи |
| [golang-naming](.agents/skills/golang-naming/SKILL.md) | Packages, identifiers, initialisms и errors |
| [golang-patterns](.agents/skills/golang-patterns/SKILL.md) | Общие Go-идиомы и простая структура |
| [golang-performance](.agents/skills/golang-performance/SKILL.md) | Измеренный bottleneck, profiling и сравнение benchmarks |
| [golang-popular-libraries](.agents/skills/golang-popular-libraries/SKILL.md) | Выбор зависимости и проверка альтернатив stdlib |
| [golang-project-layout](.agents/skills/golang-project-layout/SKILL.md) | CLI layout, boundaries пакетов и test layout |
| [golang-refactoring](.agents/skills/golang-refactoring/SKILL.md) | Малые проверяемые шаги с сохранением поведения |
| [golang-testing](.agents/skills/golang-testing/SKILL.md) | Tests, fakes, isolation, concurrency и regression coverage |
| [golang-troubleshooting](.agents/skills/golang-troubleshooting/SKILL.md) | Воспроизведение, evidence и root cause перед исправлением |

Ссылки внутри skills могут вести к неустановленным skills; не считай их доступными и не добавляй обязательный import отсутствующего файла. При необходимости используй установленные аналоги и официальную документацию. В частности, `golang-how-to` здесь не установлен.

### Проектные уточнения к skills

- **Build automation:** используется Taskfile, как требует ТЗ; Makefile из примеров не нужен. Canonical validation — `task all`.
- **Архитектура:** исходная разбивка задана ТЗ. Выбирай простой layout и ручную constructor injection; DI framework и дополнительные слои добавляй только при реальной необходимости.
- **Версии:** Go API, flags CLI, linter schemas и версии CI actions проверяй по установленным инструментам и официальным источникам. Упоминания Go 1.27 и других версий в skills не разрешают автоматически менять `go.mod` или использовать несовместимый API.
- **Данные:** nil slices допустимы внутри Go-кода, если контракт их допускает. Для JSON явно решай `[]` против `null`; map инициализируй до записи. Не копируй structs с mutex и не предполагай глубокое копирование slice/map fields.
- **Изменения:** рекомендации skills про commits, revert, широкую модернизацию и auto-fix не отменяют Git safety и scope задачи. Автоматических commits и сброса дерева нет.
- **Параллельность:** разрешены параллельные read-only investigations. Даже рекомендация skill запустить background lint auto-fix не разрешает второго writer в том же tree.
- **Review:** проверяй correctness и соответствие ТЗ; не заполняй отчёт stylistic noise. Замечание должно иметь конкретное место, воспроизводимый сценарий или доказанную цепочку вызовов и понятное последствие.

## Архитектура и Go-код

- `cmd/dev/` — тонкий entrypoint: CLI, dependency wiring и exit status. Логика находится в `internal/`; не создавай `pkg/`, `utils` или дополнительные layers без потребителя.
- Разделяй agent adapters, workflow engine, routing, project discovery, subprocess runner, validation, remote access, config, artifacts и UI. Структура из ТЗ — ориентир; создавай пакеты по мере реализации.
- Сохрани контракты `Agent.Run(ctx context.Context, req Request) (Result, error)` и `Router.Route(ctx context.Context, input RouteInput) (RouteDecision, error)`. Engine не должен знать синтаксис Claude/Codex CLI или детали JEV API.
- Dependencies передаются явно через constructors; небольшие interfaces определяются у потребителя. Избегай mutable globals и `init()` с I/O или скрытой регистрацией. CLI commands регистрируй явно.
- Большие prompts храни отдельно в `prompts/`, при необходимости используй `go:embed`. Установленный binary должен работать без runtime-доступа к исходникам этого репозитория.
- Предпочитай stdlib. Новую зависимость обоснуй, проверь maintenance, license, совместимость и официальное API. Cobra/Viper из CLI-skill — возможный выбор, а не уже установленные dependencies. Сохраняй `go.mod` и появившийся `go.sum` в Git.
- Используй gofmt, короткие focused functions, early returns, keyed struct literals и MixedCaps. Имена пакетов — lowercase без stuttering; initialisms — `ID`, `URL`, `HTTP`. Документируй exported contracts и неочевидные invariants.
- Возвращай ошибки с контекстом через `%w`; для цепочек используй `errors.Is`/`errors.As` или проверенный совместимый API. Ожидаемые ошибки не превращай в panic. Ошибку логируют на boundary либо возвращают, без дублирования.
- Structured diagnostics пишутся через `log/slog` в stderr; результаты CLI — в stdout. I/O передавай через `io.Reader`/`io.Writer`, чтобы CLI можно было тестировать. Не вызывай `os.Exit` внутри бизнес-логики.
- `context.Context` — первый параметр длительных операций. Каждый subprocess, network call и retry имеет ограничение времени и поддерживает cancellation. Goroutines имеют владельца, понятное завершение и ожидание cleanup.
- Оптимизируй после измерений. Не вводи pools, caches, `unsafe` и speculative preallocation ради предполагаемой производительности.

## Контракты приложения

### CLI, routing и config

- Основной UX — `dev` без аргументов: ввод задачи и запуск workflow. Явные workflow-команды обходят router и пригодны для non-interactive использования.
- OpenRouter/JEV опционален. Нет key, сервис/модель недоступны, timeout, network error, 4xx/5xx, invalid response или confidence недостаточна — ManualRouter; не вызывай Claude/Codex только ради классификации. Без JEV доступны все workflows.
- Проверяй `workflow`, `complexity`, `risk`, `plan_review_required`, `code_review_required`, `parallel_investigation_required`, а также confidence/probabilities. Используй настраиваемые thresholds по decision, например `router.confidence.workflow: 0.75`, без одного magic threshold для всех вопросов.
- Предпочитай один routing request с несколькими typed questions. JEV выбирает workflow и предусмотренные optional branches; Go engine детерминированно контролирует steps. Router response не разрешает произвольные actions, write access или ослабление обязательных safety contracts.
- JEV получает task text и минимальные безопасные metadata. Не отправляй source code, git diff, credentials, production logs, customer data и database content.
- Config precedence: defaults → global → project → environment → CLI flags. `OPENROUTER_API_KEY` имеет приоритет над `openrouter.api_key` в файле. Global config: `~/.config/dev-agent/config.yaml`; project config: `.dev-agent.yaml` рабочего проекта. Config с key должен иметь безопасные permissions, предпочтительно `0600`.
- `.env.example` содержит placeholder `OPENROUTER_API_KEY`. Сохраняй example в Git, реальные keys — только вне repository. Не предполагай автозагрузку `.env`: это поведение ТЗ не задаёт; если добавляешь его в рамках задачи, документируй и тестируй.
- Перед реализацией JEV integration проверь [официальный Decisions API](https://openrouter.ai/docs/api/api-reference/alphadecisions/submit-a-decisions-request). По ТЗ это `POST /api/alpha/decisions` на `https://openrouter.ai` с native `choice`, `score`, `noul`; не подменяй их chat completions с JSON/prose. Default model `~typesafe/jev-latest` задаётся через `openrouter.jev_model`, default HTTP timeout — `10s`. Фактические расхождения API с ТЗ фиксируй в README.
- OpenRouter client использует Go HTTP client: context, timeout, status handling, JSON validation, bounded body и безопасные обоснованные retries. Никакого shell/curl в production path; тестируй client через `httptest.Server`.
- Не выдумывай flags Claude/Codex: перед реализацией или изменением adapters изучи `claude --version`, `claude --help`, `codex --version`, `codex --help` и help нужных subcommands.
- Проверь non-interactive input, cwd, права read-only/write, timeout, cancellation и exit status. Одного текста «не меняй код» в prompt недостаточно для read-only: используй доступные ограничения CLI и runner.
- `AGENTS.md` рабочего проекта — основной context обоих агентов; также учитывай его `CLAUDE.md` и нативные инструкции. Обычный запуск не создаёт их; `dev init` создаёт минимальный `AGENTS.md` только при отсутствии файла и не перезаписывает существующий.
- `dev config` показывает effective config с redacted secrets; `dev config path` рекомендуется для вывода пути. `dev doctor` проверяет target `all` безопасным listing, не запускает дорогой `task all`; отсутствие OpenRouter key — нормальное состояние, а network failure показывает доступность manual fallback.

### Workflow roles

| Workflow | Контракт ролей |
| --- | --- |
| Feature | Claude plan → Codex plan review → Claude implement → validation → Codex review → Claude fixes → validation |
| Bug | Codex investigation/plan → Claude fix → validation → Codex review → Claude fixes → validation |
| Server bug | Codex remote read-only investigation → при дефекте кода Claude local fix → validation → Codex review |
| Architecture / Research | Codex draft → Claude critical review → Codex final; source code read-only |
| Refactor | Codex analysis/plan → Claude implement → validation → Codex review → Claude fixes → validation; поведение сохраняется |
| Tests | Codex test plan → Claude tests → validation → Codex review; production code меняется только обоснованно |
| Review | Codex findings → Claude verification и независимый поиск пропусков → единый final; source code read-only |
| Incident | Независимые read-only investigations Claude/Codex → Codex synthesis; гипотезы не запускают auto-remediation |

Planning, investigation и review stages read-only. Ограничивай review/fix cycles конфигурацией, default `review.max_iterations: 2`; не допускай бесконечного retry. Failure, cancellation и оставшиеся findings должны быть видны в artifacts и exit status. Не отмечай run успешным только потому, что достигнут max iterations. Review output содержит severity, file/line, problem, impact и suggested direction. Incident synthesis разделяет confirmed facts, hypotheses, disagreements, likely root cause, missing evidence, next diagnostics и recommended remediation.

### Git, SSH и секреты

- Сохраняй staged, unstaged и untracked пользовательские изменения. Учитывай initial state при формировании run diff; `git diff HEAD` не включает untracked files.
- Автоматически не выполняй `git reset --hard`, `git clean`, `git checkout .`, `git restore .`, `git commit`, `git push`, `git rebase`. Задача `clean` удаляет только известные generated outputs проекта.
- Одновременно максимум один writing agent на рабочий tree, включая пересекающиеся runs. Параллельность допустима только для read-only; incident investigations сначала не получают результаты друг друга.
- SSH по умолчанию read-only: logs, service status, processes, ports, revision, metadata, health и environment variable names. Remote writes требуют отдельного явно разрешённого режима с подтверждением/flag. Автоматического deploy нет.
- Используй SSH config/agent/keys. API keys, пароли, private keys и secret environment values не попадают в config examples, prompts других providers, logs, diffs и artifacts. Проверяй и захваченный вывод внешних программ. OpenRouter key используется только в HTTP Authorization header и исключается из argv и environment запускаемых Claude/Codex.
- Запускай локальные программы через `os/exec` с отдельными argv и stdin. Prompt и config values не интерполируются в `sh -c`. Для SSH учитывай отдельную границу remote shell; локальные argv сами по себе не защищают удалённую команду от injection.
- Ctrl+C, SIGTERM, deadlines и зависшие subprocesses должны приводить к завершению child processes и необходимому cleanup; не ограничивайся убийством только родительского CLI.
- Artifacts сохраняются в `~/.local/state/dev-agent/runs/<project>/<run-id>/`, вне working project. `run.json` фиксирует run ID, project, workflow, router/routing confidence, agents, steps, timestamps, step durations, exit codes, validation result и review iterations без secrets. `route.json` содержит безопасные provider/model/workflow/confidence/complexity/risk metadata, при наличии — безопасные usage и request duration.

## Проверки и завершение задачи

После изменения source code выполняй `task all`. Оркестратор проверяет рабочие проекты только через эту команду; не добавляй в engine знания об их тестовых frameworks.

Taskfile самого `dev` должен предоставить `fmt`, `vet`, `test`, `build`, `lint`, `all`, `install`, `clean`. `all` включает gofmt check, vet, tests и build, плюс подходящий настроенный linter, если он используется. Пока `all` отсутствует, зафиксируй это как недостающую инфраструктуру; не называй другой check успешным `task all`.

Unit tests используют fake agents/routers и isolated temp directories. Покрывай observable behavior и failure paths, а не детали реализации. Не запускай настоящие Claude/Codex/JEV/SSH в обычных unit tests. Внешние integration/smoke tests выделяй отдельно и документируй prerequisites.

Обязательное покрытие ТЗ: feature/bug transitions, architecture/research/review/incident, JEV success/low confidence/timeout/missing key/OpenRouter 500/invalid response и manual fallback, validation failure и successful retry, review без findings и с fixes, review limits, agent process failure, cancellation, dirty Git tree, config merge, artifacts и независимые параллельные investigations. Используй FakeClaude/FakeCodex/FakeRouter/FakeValidator/FakeRemoteExecutor. Добавляй regression test к найденному дефекту, где он разумен.

Используй named table-driven subtests, `t.TempDir`, test-local environment и `t.Cleanup`. `t.Parallel` допустим при полной независимости. Проверяй завершение goroutines и subprocesses; для concurrency changes запускай `go test -race ./...`, а в CI включай race detection. Избегай sleep-based assertions, если доступны детерминированные synchronization или совместимый `testing/synctest`.

Validation failure от изменений текущего run передавай implementing agent с полезным output. Существующие unrelated failures фиксируй отдельно без автоматического исправления или ослабления проверок. `//nolint` допускается точечно, с именем linter и причиной.

Для lint/CI используй соответствующие skills, совместимые версии и явные prerequisites. CI должен вызывать canonical checks; race/vulnerability checks добавляются по назначению. Публикация releases, remote writes и auto-merge не следуют из поручения настроить checks.

`task install` должен идемпотентно установить `~/.local/bin/dev`, создав каталог при необходимости. Shell profile автоматически не меняй; если PATH не содержит каталог, укажи точную строку `export PATH="$HOME/.local/bin:$PATH"`.

После полной реализации проверь `task all`, `task install`, `dev --help`, `dev doctor`, interactive startup из другого Git-репозитория, manual routing без JEV, JEV routing при наличии credentials, read-only smoke Claude и Codex, сохранение artifacts. Для небольшой задачи выполняй проверки по её scope; не запускай installation или providers ради изменения документации.

В конце перечисли результат, выполненные проверки и их статус, существенные ограничения и текущее состояние Git. При полной реализации дополнительно укажи binary/config/artifacts paths и способ запуска. Не выдавай незапущенные проверки за выполненные, а отсутствие JEV — за ошибку.
