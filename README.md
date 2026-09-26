# dev-orchestrator

`dev` — CLI-оркестратор разработки на Go. Он координирует Claude Code, Codex CLI, Git, проверку рабочего проекта через `task all` и диагностику серверов по SSH. TypeSafe JEV через OpenRouter Decisions API опционально выбирает workflow и рекомендует следующие шаги после ответов агентов.

Полное техническое задание: [documentation/tz.md](documentation/tz.md). Правила для агентов: [AGENTS.md](AGENTS.md); входная инструкция Claude Code: [CLAUDE.md](CLAUDE.md).

## Состояние проекта

Разработка приложения ещё не начата. Сейчас в проекте есть стартовый `main.go`, `go.mod` с Go 1.26, Taskfile с единственной задачей `default` и 18 установленных Go-skills. Команда `dev`, workflows, конфигурация, `task all` и установка пока не реализованы.

Разделы ниже описывают целевое поведение по ТЗ. Примеры запуска станут рабочими после реализации соответствующих команд. По мере разработки этот раздел и инструкции установки нужно обновлять по фактически проверенному состоянию.

## Назначение

Основной сценарий: перейти в Git-репозиторий рабочего проекта, запустить `dev` и описать задачу. Оркестратор определяет корень проекта через `git rev-parse --show-toplevel`, использует его инструкции и выполняет нужный workflow.

Репозиторий исходников `dev` и репозиторий рабочего проекта — разные сущности. После установки binary работа не должна зависеть от каталога исходников оркестратора.

Оркестрация выполняется на Go. Внешние CLI запускаются как subprocesses; workflow engine не содержит их конкретных flags и shell-сценариев.

## Сборка и установка после реализации

Для сборки нужны Go версии, совместимой с `go.mod`, и Task. Для работы нужны Git, Task, настроенные Claude Code и Codex CLI. Их API не заменяют установленные CLI. SSH требуется для серверных workflows; OpenRouter/JEV не является обязательной зависимостью.

URL репозитория пока не указан. Замените `<REPOSITORY_URL>` на фактический адрес:

```bash
git clone <REPOSITORY_URL> dev-orchestrator
cd dev-orchestrator
task all
task install
```

Целевые задачи Taskfile:

| Команда | Назначение |
| --- | --- |
| `task fmt` | Форматировать Go-код |
| `task vet` | Выполнить `go vet` |
| `task build` | Собрать binary `dev` |
| `task test` | Запустить Go-тесты |
| `task lint` | Проверить форматирование и статический анализ |
| `task all` | Выполнить полный набор проверок и сборку |
| `task install` | Собрать и идемпотентно установить `~/.local/bin/dev` |
| `task clean` | Удалить только результаты сборки этого проекта |

`task all` должен включать проверку gofmt, `go vet`, тесты и сборку, а также настроенный linter, если он используется. Проверки не должны молча пропускаться из-за отсутствующего инструмента.

Если `~/.local/bin` отсутствует в `PATH`, добавьте в профиль используемого shell:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

Установка не должна менять shell profile автоматически. После установки проверьте:

```bash
command -v dev
dev --help
dev doctor
```

## Использование после реализации

В рабочем проекте должны быть Git-репозиторий, Taskfile и доступная задача `all`.

```bash
cd ~/projects/my-project
dev
```

Интерактивный режим начинает с запроса:

```text
What do you want to work on?
>
```

С настроенным OpenRouter/JEV workflow определяется автоматически. Без него появляется ручной выбор любого workflow из таблицы ниже. Внутренние переходы выполняет LocalDecisionService. Отсутствие API key — нормальный режим; Claude и Codex не вызываются только ради классификации.

Явная команда обходит начальный router; JEV может использоваться в последующих decision points:

```bash
dev feature "Add bulk cancellation"
dev bug "Duplicate appointments are created"
dev bug --server production "API intermittently returns 502"
dev architecture "Should this become a separate service?"
dev research "How does authorization work here?"
dev refactor "Simplify the payment service without changing behavior"
dev tests "Cover concurrent subscription updates"
dev review
dev review --base main
dev incident "Workers started consuming 100% CPU"
dev incident --server production "Workers stopped responding"
dev investigate "Find why queue latency increased; do not fix yet"
dev plan "Design bulk cancellation without implementing it"
dev implement --plan /path/to/plan.md "Implement the agreed scope"
dev docs "Update the subscription API documentation"
dev chore "Update CI for the current Go version"
dev fix-review --report /path/to/review.md "Fix confirmed findings"
```

Для полностью локального режима предусмотрен `--no-jev`. Длинные задачи принимаются через многострочный paste или `--task-file <path>`; `--task-file -` читает stdin. Non-interactive run с недостающим workflow, plan/report или неоднозначным scope завершает `needs_input`, сохраняя вопрос в artifacts.

Служебные команды:

| Команда | Целевое поведение |
| --- | --- |
| `dev init` | Подготовить инструкции проекта, сохранив существующие файлы |
| `dev doctor` | Проверить инструменты, конфигурацию, OpenRouter/JEV, рабочий проект, наличие target `all` в Taskfile и SSH |
| `dev history` | Показать историю запусков |
| `dev show <run-id>` | Показать сохранённый запуск |
| `dev config` | Показать effective config с redacted secrets |

Дополнительно ТЗ рекомендует `dev config path` для вывода пути global config. `dev doctor` проверяет наличие `task all` без запуска дорогой проверки; маленький безопасный JEV health request допустим, без передачи исходников.

Предусмотрены flags `--verbose`, `--quiet`, `--no-color`, `--dry-run`. Dry-run показывает stages, права, gates и budgets без запуска agents, validation, SSH и JEV; будущие решения отображаются как возможные branches.

### Отображение процесса после реализации

По [разделу 2.1 ТЗ](documentation/tz.md#21-отображение-процесса-в-терминале) запуск показывает проект, branch, исходный dirty state, run ID, workflow, источник routing, права и каталог artifacts. Затем появляется компактная лента этапов: название, агент, status, длительность и короткий итог. Активный этап имеет индикатор и таймер; завершённые этапы остаются в истории терминала. Во время долгого ожидания status обновляется даже без сообщений агента. Повторные fixes/review показывают причину, round и лимит.

Итог содержит outcome, результат, validation/review и оставшиеся blockers; findings, ошибка, cancellation и исчерпание лимита видны явно. Progress и вопросы идут в stderr, итоговый результат — в stdout. При перенаправлении соответствующего потока используется обычный текст без анимации и ANSI sequences. Цвет отключается через `--no-color`, `NO_COLOR` или `TERM=dumb`. `--quiet` оставляет итог, ошибки и обязательные вопросы; `--verbose` добавляет очищенные подробности. Эти два режима взаимоисключающие. Вывод агентов и внешних команд очищается от secrets и недоверенных terminal control sequences.

## Workflows

| Workflow | Последовательность |
| --- | --- |
| `feature` | Claude: план → Codex: review плана → plan gate → Claude: реализация → общий цикл validation/review |
| `bug` | Codex: investigation и план → diagnosis gate → Claude: исправление → общий цикл validation/review |
| `server_bug` | Codex: SSH investigation read-only → при дефекте кода Claude: локальное исправление → `task all` → Codex: review |
| `architecture` | Codex: исследование и draft → Claude: критический review → Codex: final |
| `research` | Codex: investigation/draft → Claude: review → Codex: final |
| `refactor` | Codex: анализ и план → Claude: реализация с сохранением поведения → общий цикл validation/review |
| `tests` | Codex: анализ и test plan → Claude: тесты → общий цикл validation/review |
| `review` | Codex: основной review → Claude: проверка findings и независимый поиск пропусков → единый final review |
| `incident` | Независимые read-only investigations Claude и Codex → Codex: synthesis |
| `investigate` | Codex: diagnosis → Claude: проверка evidence → Codex: final; без fixes, optional `--server` |
| `plan` | Claude: plan → Codex: review → bounded revisions/re-review → final plan; без implementation |
| `implement` | Codex: проверка импортированного plan → Claude: implementation → общий цикл validation/review |
| `docs` | Claude: проверка фактов, plan и документация → Codex: review → bounded fixes/re-review |
| `chore` | Codex: compatibility/impact и plan → Claude: tooling/config/dependencies → общий цикл validation/review |
| `fix_review` | Codex: проверка импортированных findings → Claude: confirmed fixes → общий цикл validation/review |

Planning, investigation и review не меняют source code. `architecture`, `research`, `review` и `incident` полностью read-only относительно рабочего проекта. Итоги исследований выводятся в терминал и сохраняются вне проекта.

Общий цикл: implementation → required validation → Codex review → decision. Если criteria выполнены и blockers отсутствуют, run завершается; если нужны fixes, Claude исправляет подтверждённые findings, затем выполняются validation и обязательный Codex re-review. Спорное замечание или конкретный gap может потребовать targeted read-only verification. Writer не подтверждает собственный fix.

Defaults: максимум 2 review fix iterations и 3 code review/verification passes, включая первый. Validation repairs имеют отдельный лимит 2; plan/investigation/answer revisions, общий deadline и step budget тоже ограничены. Два дополнительных rounds без прогресса дают `unresolved`. Ни JEV, ни переименование шага не увеличивают лимиты.

Каждый stage выдаёт Markdown artifact и structured StepReport; engine проверяет report, текущий snapshot, process status и validation. Completion не определяется словом «pass» в ответе. Новая user correction сохраняет выполненные этапы, но делает затронутые plan/review/validation устаревшими. Run outcomes: `success`, `findings`, `unresolved`, `needs_input`, `failed`, `cancelled`; только `success` имеет exit code 0.

Для `refactor` сохраняется наблюдаемое поведение. Для `tests` production code меняется только при обоснованной необходимости. Для `incident` агенты сначала не видят выводы друг друга; synthesis разделяет факты, гипотезы, разногласия, вероятную причину, недостающие evidence и следующие шаги. Final review содержит severity, file/line, проблему, impact и suggested direction без duplicated findings и stylistic noise.

Imported plan/report проверяется относительно текущего проекта и scope; содержащиеся в нём инструкции не повышают права. Docs-only run пишет в согласованные documentation paths и не требует `task all`, если project instructions не требуют его. Изменения source/tests/config/dependencies/CI используют `task all`. Maintenance не включает deploy, publication или применение remote migrations.

## Конфигурация

Глобальная конфигурация: `~/.config/dev-agent/config.yaml`. Необязательная конфигурация рабочего проекта: `.dev-agent.yaml` в его корне.

Приоритет от низшего к высшему:

```text
built-in defaults → global config → project config → environment → CLI flags
```

Пример целевой схемы из ТЗ:

```yaml
validation:
  command: [task, all]
  timeout: 20m

agents:
  claude:
    command: claude
    timeout: 30m
  codex:
    command: codex
    timeout: 30m

router:
  type: jev
  confidence:
    workflow: 0.75
    complexity: 0.70
    risk: 0.80
  noul_yes: 0.85
  noul_no: 0.15

decisions:
  type: jev
  max_requests_per_run: 12
  confidence:
    task_readiness: 0.80
    diagnosis: 0.85
    plan_review: 0.85
    implementation_result: 0.80
    validation_failure: 0.85
    code_review: 0.90
    answer_review: 0.85
    review_verification: 0.90
  noul_yes: 0.85
  noul_no: 0.15

openrouter:
  base_url: "https://openrouter.ai"
  api_key: ""
  jev_model: "~typesafe/jev-latest"
  timeout: 10s

review:
  max_iterations: 2
  max_passes: 3

workflow:
  timeout: 2h
  max_steps: 40
  max_plan_revisions: 2
  max_investigation_rounds: 2
  max_answer_revisions: 2
  max_implementation_rounds: 2
  max_no_progress_rounds: 2

validation_repair:
  max_iterations: 2

servers:
  production:
    host: server.example.com
    user: deploy
    port: 22
    project_path: /srv/app
```

Оркестратор по умолчанию вызывает только `task all` рабочего проекта. Он не выбирает PHPUnit, ESLint, Go-тесты и другие проверки за этот проект. Для явно настроенной альтернативы изменяется `validation.command` в global или project config: это массив executable и отдельных arguments, а не shell command string. Поле не является поводом определять стек автоматически; стандартный contract остаётся `task all`.

`.dev-agent.yaml` можно хранить в Git, если он содержит только общие настройки без секретов. Локальные API keys и другие credentials в репозиторий не помещаются. Если key хранится в global config, файл должен иметь безопасные permissions, предпочтительно `0600`. Наличие `.env` само по себе не означает, что приложение автоматически его загружает: это поведение ТЗ не задаёт.

### OpenRouter / JEV

По ТЗ router использует `POST https://openrouter.ai/api/alpha/decisions` и native decision types `choice`, `score`, `noul`. Это typed decisions с probabilities, а не prose или JSON, сгенерированный через chat completions. Актуальная схема: [OpenRouter Decisions API](https://openrouter.ai/docs/api/api-reference/alphadecisions/submit-a-decisions-request); описание primitives: [JEV на OpenRouter](https://openrouter.ai/docs/guides/community/jev).

Для настройки предусмотрена переменная `OPENROUTER_API_KEY` либо `openrouter.api_key` в global config. Environment имеет приоритет над файлом. Пример с placeholder, который нужно заменить своим key в локальном окружении:

```bash
export OPENROUTER_API_KEY="<your-openrouter-api-key>"
```

Key передаётся только в HTTP header `Authorization: Bearer ...` Go-клиента. Он не должен попадать в process arguments, environment Claude/Codex, prompts, logs, artifacts или diagnostic output.

Имя переменной также показано в [.env.example](.env.example). Храните реальный key только в локальном окружении или защищённом global config; `.env` исключён из Git. Автозагрузка env-файлов пока не реализована и не задана ТЗ.

Default model — `~typesafe/jev-latest`; `openrouter.jev_model` позволяет закрепить версию, например `typesafe/jev-1.13`. Model не фиксируется в workflow engine. Порог workflow confidence настраивается через `router.confidence.workflow`; пример выше — `0.75`, а не общий порог для всех decisions.

Один routing request по возможности определяет `workflow`, `complexity` (`trivial/low/medium/high`), `risk` (`low/medium/high/critical`), `plan_review_required`, `code_review_required`, `parallel_investigation_required`. Отрицательный ответ не отменяет обязательный review или независимые incident investigations. К request прикладываются безопасный task text и минимальные metadata. Source code, git diff, credentials, production logs, customer data и database content не отправляются; небезопасное содержимое task тоже требует local fallback.

Для работы без OpenRouter оставьте key ненастроенным либо используйте `--no-jev`. Нет key, низкая confidence, timeout, network error, 4xx/5xx, invalid response или недоступная модель — initial fallback на ManualRouter, stage fallback на LocalDecisionService. Для `noul` проверяется probability yes/no; промежуточная вероятность считается неопределённой. Cancellation не вызывает новый fallback run.

Stage decision points: готовность задачи, diagnosis, review плана, итог implementation, failed validation, code review, review ответа и проверка findings. JEV получает allowlist metadata из StepReport и engine facts, без полного ответа агента, snippets, paths и logs. Он рекомендует следующий action из разрешённых вариантов; Go policy проверяет prerequisites, permissions, completion и budgets. Например review «pass» с unknown criterion не разрешает complete, а fixes всегда требуют re-review.

`router.type: manual` отключает initial JEV routing, `decisions.type: local` — внутренние decisions. Общий HTTP budget включает routing и retry attempts; после его исчерпания действуют local rules. Не требуется JEV request при единственном обязательном переходе. Перед реализацией integration нужно повторно проверить актуальный Decisions API; расхождения с ТЗ документируются.

Требования к prompts по [OpenAI latest-model guide](https://developers.openai.com/api/docs/guides/latest-model) изложены в разделе 32.1 ТЗ. Model/reasoning/steering проверяются по capabilities установленного CLI.

### Серверы

Server profiles находятся в глобальном config. Подключение использует стандартные `~/.ssh/config`, SSH agent и SSH keys. Пароли и private keys в config не хранятся.

`dev bug --server production "..."` по умолчанию выполняет только диагностику: logs, status сервисов, процессы, порты, health, Git revision и metadata. При просмотре environment разрешены имена переменных без secret values. Вывод удалённых команд также требует фильтрации секретов.

Если найден дефект source code, исправление выполняется в локальном рабочем проекте. Deployment не запускается автоматически. Remote writes требуют отдельного явно разрешённого режима и подтверждения либо explicit flag.

## Безопасность рабочего проекта

- До работы сохраняется исходный Git status, включая staged, unstaged и untracked изменения. Пользовательские изменения должны сохраняться; review по возможности выделяет diff текущего run.
- Автоматические `git reset --hard`, `git clean`, `git checkout .`, `git restore .`, `git commit`, `git push` и `git rebase` запрещены.
- Параллельно разрешены только read-only agents. В одном working tree одновременно работает максимум один writing agent.
- Существующие `AGENTS.md`, `CLAUDE.md`, нативные инструкции и skills рабочего проекта используются без автоматического перезаписывания.
- Пользовательский prompt передаётся через безопасный ввод subprocess, без интерполяции в shell command. Timeout, Ctrl+C и SIGTERM должны останавливать дочерние процессы.

## Артефакты запусков

Runtime artifacts сохраняются вне рабочего репозитория:

```text
~/.local/state/dev-agent/runs/<project>/<run-id>/
```

Набор зависит от workflow: `task.md`, `route.json`, `investigation.md`, `remote-investigation.md`, `draft.md`, `plan.md`, `plan-review.md`, `implementation.md`, `diff.patch`, `review.md`, `validation.log`, `final.md`, `run.json`.

Каждый stage сохраняет отдельные `steps/<step-id>/report.json` и `output.md`; каждый decision — `decisions/<decision-id>.json`. Reports предыдущих rounds не перезаписываются. Decision history показывает allowed/recommended/applied actions, probabilities/thresholds, snapshot references, budgets и причины fallback/rejection.

`run.json` содержит run ID, project, workflow, router и routing confidence, время начала и завершения, agents, steps, step durations, exit codes, validation result и review iterations. `route.json` хранит безопасные provider/model/workflow/confidence/complexity/risk metadata; допустимы безопасные usage и request duration. Секреты не сохраняются ни в metadata, ни в захваченном stdout/stderr или diff. История доступна через `dev history` и `dev show <run-id>`.

## Структура разработки

Целевая структура из ТЗ; каталоги создаются по мере появления кода:

```text
cmd/dev/             # CLI entrypoint и wiring
internal/agent/      # Контракты и adapters Claude/Codex
internal/workflow/   # Шаги, переходы и review/fix cycles
internal/router/     # JEV и manual selection
internal/router/jev/ # OpenRouter Decisions HTTP client и typed responses
internal/decision/   # Stage decisions, policy inputs и local fallback
internal/project/    # Git root, instructions и исходное состояние
internal/git/        # Git status, branch и сравнение изменений
internal/runner/     # Subprocesses, cancellation и exit status
internal/validation/ # task all рабочего проекта
internal/remote/     # SSH и режимы доступа
internal/config/     # Defaults, loading, merge и validation
internal/artifacts/  # Run storage и metadata
internal/ui/         # Interactive selector и terminal output
prompts/             # Prompt templates, при необходимости go:embed
documentation/tz.md  # Полное ТЗ
.agents/skills/      # 18 установленных Go-skills
.claude/skills/      # Symlinks на .agents/skills
skills-lock.json     # Метаданные установленных skills
```

Не требуется создавать пустые слои или публичный `pkg/` без потребителя. Контракты `Agent.Run(ctx, Request)`, `Router.Route(ctx, RouteInput)` и `DecisionService.Decide(ctx, DecisionInput)` отделяют engine от providers и HTTP shape; зависимости передаются явно через constructors.

### Расширение

- **Новый workflow:** определить шаги и права каждого шага, добавить отдельные prompt templates, зарегистрировать CLI/manual/routing варианты, написать тесты переходов и failures, обновить документацию.
- **Новый Router:** реализовать контракт Router, проверить typed response, confidence и fallback, подключить config без изменения workflow engine; добавить fake/HTTP tests без реального API key.
- **Новый decision point:** задать допустимые transitions, prerequisites, threshold, local fallback и бюджет, покрыть rejected/low-confidence/stale responses; не передавать управление subprocess модели.
- **Новый Agent provider:** реализовать контракт Agent через runner, исследовать реальный `--version`/`--help`, проверить prompt input, cwd, read-only/write mode, cancellation и exit codes; покрыть adapter тестами.

## Проверки разработки

Установленные skills и правила их применения перечислены в [AGENTS.md](AGENTS.md). Skill-примеры адаптируются к ТЗ, версии Go и фактическому инструментарию, а не копируются без проверки.

Unit tests используют fake agents, routers, validators и remote executors. Обязательные сценарии по ТЗ: feature/bug state machines, architecture/research/review/incident, JEV success/low confidence/timeout/missing key/OpenRouter 500/invalid response, manual fallback, validation failure и успешный retry, review без findings и с fixes, max iterations, agent crash, cancellation, dirty tree, config merge, artifacts и независимые параллельные read-only investigations.

Дополнительные scenarios: FakeDecisionService и каждый stage gate, local fallback, completion при полном coverage, disputed/unverified findings, обязательный re-review после fixes, budgets/no progress, imported plans/reports, новые workflows, payload allowlist, non-interactive/`--no-jev`/dry-run и user updates. Prompt changes проверяются на небольших versioned fixtures с ожидаемыми transitions; live provider evals выполняются отдельно.

OpenRouter client реализуется через Go HTTP client с context, timeout, проверкой status/JSON, bounded response body и только оправданными безопасными retries. Он тестируется через `httptest.Server`; shell/curl не используется в production implementation.

Для concurrency нужна race detection и проверка завершения goroutines/processes. Тесты внешних CLI и smoke checks с реальными Claude/Codex выполняются отдельно от обычных unit tests.

После полной реализации проверка готовности включает `task all`, `task install`, `dev --help`, `dev doctor`, интерактивный запуск из другого Git-репозитория, режим без JEV, безопасный JEV routing при наличии credentials, read-only smoke каждого provider и появление artifacts. Запуск большой feature не нужен для smoke verification.
