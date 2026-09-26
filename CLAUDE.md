# Claude Code: dev-orchestrator

@AGENTS.md

Общие инструкции проекта находятся в [AGENTS.md](AGENTS.md) и обязательны для Claude Code. Перед работой прочитай их, [documentation/tz.md](documentation/tz.md) и актуальный [README.md](README.md). Если импорт не был загружен автоматически, прочитай файл напрямую.

## Skills

Установленные Go-skills доступны в `.claude/skills/` через symlinks на `.agents/skills/`. Полный список и правила выбора находятся в `AGENTS.md`; используй соответствующие `SKILL.md` и references до изменения Go-кода. Не редактируй skills или symlinks без поручения.

Проектные уточнения `AGENTS.md` имеют приоритет над generic skill-примерами: Taskfile вместо Makefile, один writer на рабочий tree, сохранение dirty state, отсутствие auto-commit, проверка API по `go.mod` и реальному CLI. OpenRouter/JEV использует native Decisions API; Claude/Codex работают через установленные CLI, без передачи им OpenRouter key.

## Роль Claude в workflows

- **Feature:** изучить проект и подготовить план без изменения source code; при реализации учесть проверенные замечания Codex, выполнить focused changes и исправить реальные failures/findings.
- **Bug / Server bug:** проверить diagnosis и план Codex, исправить root cause в локальном working project, добавить regression coverage по необходимости. SSH investigation не разрешает deployment или remote writes.
- **Refactor:** реализовать план с сохранением observable behavior; структурные изменения не смешивать с unrelated features.
- **Tests:** реализовать test plan; production code менять только при обоснованной необходимости.
- **Architecture / Research:** критически проверить draft Codex, assumptions, alternatives и operational costs; source code read-only.
- **Review:** проверить findings Codex по repository evidence и независимо искать серьёзные пропуски; source code read-only.
- **Incident:** провести независимое read-only investigation, не читая выводы Codex до synthesis stage.

Эта таблица описывает роли приложения `dev`. В текущей сессии выполняй порученную пользователем задачу и назначенную роль; не запускай весь workflow только потому, что прочитал ТЗ.

## Перед завершением

После изменения source code выполни `task all` и сообщи фактический результат. Не скрывай pre-existing failures и не ослабляй проверки ради зелёного результата. При отсутствии задачи `all` явно укажи ограничение.

Сохрани пользовательские изменения и проверь итоговый diff, включая новые файлы. Обнови README при изменении публичного поведения, config, команд или установки. Отчёт пользователю — на русском, с результатом, проверками и ограничениями.
