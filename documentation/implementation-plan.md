# План реализации dev

Работа по `documentation/tz.md`, без изменения требований и без автоматических commits.

- [x] Discovery: прочитать ТЗ, инструкции, README; сохранить исходное Git state.
- [x] Exploration: параллельные read-only исследования engine, adapters/security, JEV/config/UI.
- [x] Clarification: реальные противоречия не обнаружены; мелкие решения разрешены ТЗ.
- [x] Architecture: небольшие внутренние пакеты, явные зависимости, общий validation/review loop.
- [x] Implementation: contracts/config, безопасный runner/project/remote, agents/prompts, routing/decisions, engine, artifacts/UI/CLI.
- [x] Verification: fake/HTTP/process tests, task all, race detection, независимые reviews.
- [x] Delivery: README, installation, реальные read-only smoke и итоговый Git status.

Исходное состояние: branch `main`, staged/unstaged/untracked изменений нет; Go 1.26.6; Go skeleton и Taskfile только с `default`.

Архитектура следует ТЗ: `cmd/dev`, `internal/{agent,workflow,router,decision,project,runner,validation,remote,config,artifacts,ui,report,safety}`, embedded `prompts`. Stdlib CLI/HTTP/processes/testing; отдельная YAML dependency нужна для указанной схемы config. Engine устанавливает permissions и budgets, а typed decisions выбирают только разрешённые transitions. Reports и validation привязаны к фактическому snapshot; writer не закрывает собственные findings. Runtime state хранится вне рабочего проекта.

Подходы: единый большой engine минимизировал бы файлы, но смешал бы safety и workflow; отдельный engine для каждого workflow дублировал бы loops. Выбран общий engine с небольшими family flows и явными gates.

Unit tests не запускают настоящие providers и SSH. Live smoke выполняется отдельно на synthetic Git project в read-only режиме.

## Проверенные результаты

- `task all`: PASS (gofmt check, vet, isolated unit tests, golangci-lint 0 issues, build).
- `task race`: PASS для всех packages; deterministic cancellation/process/group и independent investigation tests.
- `govulncheck ./...`: No vulnerabilities found.
- Read-only live Claude Code 2.1.280 / Codex 0.157.1: PASS на synthetic Git project, реально прочитаны файлы, snapshot unchanged.
- Native OpenRouter/JEV `choice`/`score`/`noul`: PASS, returned model `typesafe/jev-1.13-20260917`.
- Initial JEV six-question routing: native response проверен; при default confidence для score продемонстрирован manual fallback.
- `task install`: выполнен; binary `~/.local/bin/dev`; `dev --help`, `dev config` redaction и `dev doctor`: PASS.
- OpenRouter key найден в исходном локальном `.env`, настроен в private global config `~/.config/dev-agent/config.yaml` с mode 0600. Значение не выводилось; пользовательский `.env` не менялся.

Независимые reviews проверили engine, config/router/UI и sandbox/Git/remote. Исправлены findings о generation, непроверенных fixes, incomplete applicability/attribution, no-progress, cancellation/EOF, scope normalization и truncated inventories. README отражает Linux/bwrap prerequisites и фактические ограничения SSH diagnostics; ТЗ и установленные skills не менялись. Commits/push не выполнялись.

- Installed interactive startup из `/tmp/dev-manual-smoke-705a6l8s`: manual research завершён `success`; stdout final содержательный, artifacts доступны через `dev show`; initial/current snapshot совпадают, `diff.patch` пустой, file hashes неизменны, artifact files mode 0600.
- Финальный `task smoke`: PASS для Claude/Codex (27.95s), native JEV primitives и initial routing с ожидаемым confidence fallback; integration checks используют `-count=1`.
- Read-only validation фиксируется явно как `not_required`; operational diagnosis не требует validation target до возможного local writer. Подтверждены regression tests.
- Реальный SSH и real-provider writing runs не запускались; remote quoting/commands и writing transitions/failure paths проверены fakes/process/HTTP fixtures. Linux/bwrap prerequisite задокументирован.
