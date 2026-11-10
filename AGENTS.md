# Правила проекта Ledger

Область действия — весь проект. Для каждой задачи сначала прочитайте
[rules/core/001-dev-workflow.mdc](rules/core/001-dev-workflow.mdc),
затем правила затронутой области из таблицы и соответствующий код.

| Изменение | Правило |
| --- | --- |
| Процесс разработки, проверки и готовность результата | [rules/core/001-dev-workflow.mdc](rules/core/001-dev-workflow.mdc) |
| Написание или изменение тестов, фикстуры и проверка отказов | [rules/core/002-testing.mdc](rules/core/002-testing.mdc) |
| Модели, состояние, функции, публичные интерфейсы и зависимости | [rules/backend/100-architecture.mdc](rules/backend/100-architecture.mdc) |
| Валидация, загрузка JSON, ошибки, ввод-вывод и сохранность данных | [rules/backend/104-error-handling.mdc](rules/backend/104-error-handling.mdc) |
| SQL-схема, версии миграций, применение, откат и сохранность данных при преобразовании | [rules/backend/105-migrations.mdc](rules/backend/105-migrations.mdc) |

Все пути указаны относительно корня Ledger. Правила физически находятся
в `rules/` и описывают разработку сервисов `gateway/` и `ledger/`, включая Go-код
и SQL-миграции. Читайте их по таблице, даже если редактор не подключает
файлы `.mdc` автоматически.

Все правила относятся к Ledger и самодостаточны. `AGENTS.md` служит навигатором;
содержательные правила поддерживаются в соответствующих `.mdc`,
без дублирующих копий.

## Документация библиотек — Context7

<!-- context7 -->
Use Context7 MCP to fetch current documentation whenever the user asks about a library, framework, SDK, API, CLI tool, or cloud service — even well-known ones like React, Next.js, Prisma, Express, Tailwind, Django, or Spring Boot. This includes API syntax, configuration, version migration, library-specific debugging, setup instructions, and CLI tool usage. Use even when you think you know the answer — your training data may not reflect recent changes. Prefer this over web search for library docs.

Do not use for: refactoring, writing scripts from scratch, debugging business logic, code review, or general programming concepts.

### Steps

1. Always start with `resolve-library-id` using the library name and the user's question, unless the user provides an exact library ID in `/org/project` format.
2. Pick the best match (ID format: `/org/project`) by: exact name match, description relevance, code snippet count, source reputation (High/Medium preferred), and benchmark score (higher is better). If results don't look right, try alternate names or queries (e.g., "next.js" not "nextjs", or rephrase the question). Use version-specific IDs when the user mentions a version.
3. `query-docs` with the selected library ID and the user's full question (not single words), scoped to a single concept. If the question spans multiple distinct concepts (e.g. routing and auth and caching), make a separate `query-docs` call per concept with the same library ID, unless the question is about how the concepts interact — combined queries dilute ranking and return shallow results for each topic.
4. Answer using the fetched docs.
<!-- context7 -->
