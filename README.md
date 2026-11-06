# Gateway и Ledger

Учебный проект на Go 1.22 или новее. Gateway на порту 8080 вызывает сервис Ledger
внутри процесса через интерфейс и сохраняет бюджеты и расходы в PostgreSQL.
Отдельный HTTP-сервис Ledger на порту 8081 использует те же данные при одинаковом
DSN и предоставляет отчёты с Redis-кэшем на 30 секунд. Gateway не требует Redis,
сохраняет прежние HTTP-контракты и не предоставляет маршрут отчёта.

## Структура

- `ledger/domain/` — сущности, валидация и контракты репозиториев, атомарных операций и кэша.
- `ledger/service/` — интерфейс приложения, правило бюджета, валидация и логика отчётов.
- `ledger/internal/repository/pg/` — SQL-реализация доменных контрактов и транзакций.
- `ledger/app/` — фабрики готового сервиса и владение подключениями.
- `ledger/cmd/ledger/` — запуск HTTP-сервиса через фабрику `app.NewReporting(ctx)`.
- `ledger/internal/db/` — конфигурация и пул PostgreSQL через `database/sql` и pgx.
- `ledger/internal/cache/` — подключение Redis и реализация контракта кэша отчётов.
- `ledger/internal/httpapi/` — HTTP API отдельного Ledger.
- `ledger/migrations/` — SQL-миграции Goose для `budgets` и `expenses`.
- `ledger/internal/integration/` — проверки сервиса с PostgreSQL, Redis и реальными миграциями.
- `ledger/ledger.go` — прежнее хранилище в памяти с сохранённым публичным API и тестами.
- `ledger/budgets.json` — пример для прежнего `LoadBudgets`, при старте не загружается.
- `gateway/` — HTTP-сервис домашнего задания 5 с внедрённым `service.Service`.
- `compose.yaml` — локальные PostgreSQL и Redis с отдельными томами данных.

Модули `gateway/` и `ledger/` имеют собственные `go.mod`; корневого модуля нет.
Поэтому `domain`, `service` и `app` доступны как публичные пакеты: соседний модуль
не может импортировать `ledger/internal/*`. В рабочем коде Gateway импортирует
только `ledger/service` и `ledger/app`.

Зависимости направлены к домену: сервис использует доменные интерфейсы,
PostgreSQL-репозитории их реализуют, фабрика соединяет реализации с сервисом.
Домен не зависит от HTTP или SQL, сервис — от конкретного хранилища.
При запуске Gateway получает `(service, closeFn, err)` от `app.New(ctx)`,
после успеха выполняет `defer closeFn()` и передаёт сервис в `api.NewHandler`.
Фабрика читает окружение, открывает пул и вызывает `PingContext` с переданным
контекстом; при ошибке Gateway логирует причину и завершает процесс.
Подробности сервисов: [Ledger](ledger/README.md), [Gateway](gateway/README.md).

## PostgreSQL, Redis и миграции

Нужны Go, Docker с Compose и утилита Goose. Все команды этого раздела выполняются
из корня репозитория. Установите закреплённую версию Goose:

```sh
go install github.com/pressly/goose/v3/cmd/goose@v3.21.1
export PATH="$(go env GOPATH)/bin:$PATH"
```

Запустите PostgreSQL 17 и Redis 7:

```sh
docker compose up -d --wait
export DATABASE_URL='postgres://postgres:postgres@localhost:5432/cashapp?sslmode=disable'
export REDIS_ADDR='localhost:6379'
export REDIS_DB='0'
export REDIS_PASSWORD=''
```

При первом запуске Compose создаёт БД `cashapp`. PostgreSQL хранит данные
в именованном томе `postgres_data`: остановка и повторный запуск сервисов их
сохраняют. Redis в этом локальном примере работает без пароля. `REDIS_PASSWORD`
нужен, если подключаетесь к другому Redis с настроенным паролем; сама переменная
не включает авторизацию в контейнере Compose.

Если локальные порты заняты, перед `docker compose up` задайте, например,
`DB_PORT=55432` и `REDIS_PORT=56379`, а в `DATABASE_URL` и `REDIS_ADDR` укажите
те же порты. Параметры `DB_USER`, `DB_PASS`, `DB_NAME` инициализируют контейнер
PostgreSQL только при создании пустого тома.

До запуска Gateway или Ledger и после изменения схемы примените миграции:

```sh
goose -dir ./ledger/migrations postgres "$DATABASE_URL" up
goose -dir ./ledger/migrations postgres "$DATABASE_URL" status
```

Миграции `00001_budgets.sql` и `00002_expenses.sql` создают таблицы и индекс
`(category, date)`. `00003_gateway_compatibility.sql` расширяет денежные поля
до `NUMERIC` без заданных точности и масштаба и меняет дату на `TEXT`. Это сохраняет
при переходе Gateway на PostgreSQL положительные конечные значения `float64`
без округления до копеек и необязательные/произвольные строковые даты.
Существующие даты сохраняются в формате `YYYY-MM-DD`. Каждый SQL-файл содержит
секции `-- +goose Up` и `-- +goose Down`.

Для строк с NUL (`\u0000` в JSON) репозиторий экранирует NUL и обратные слеши
при хранении и восстанавливает их при чтении. Миграция также преобразует прежние
обратные слеши; в HTTP строковые поля остаются прежними.

Откат последней миграции:

```sh
goose -dir ./ledger/migrations postgres "$DATABASE_URL" down
```

Откат всех миграций:

```sh
goose -dir ./ledger/migrations postgres "$DATABASE_URL" down-to 0
```

Откат миграции 3 сначала проверяет все значения и отказывается от преобразования,
если дата или сумма не представимы в прежних `DATE` и `NUMERIC(14,2)` без потери,
либо строка содержит NUL, недопустимый в прежнем текстовом представлении.
Откат миграций 1–2 удаляет соответствующие таблицы вместе с данными. После отката
выполните `up` перед запуском. Фабрики не применяют миграции автоматически.

## Настройки и запуск

Фабрика `app.New(ctx)` для Gateway подключается к PostgreSQL; `app.NewReporting(ctx)`
для отдельного Ledger дополнительно подключается к Redis. Проверка доступности
через `Ping` предшествует запуску HTTP. Ошибка подключения завершает запуск с понятным сообщением
и ненулевым кодом. Успешные подключения записываются в лог. При отсутствии нужных таблиц
или типов из миграции 3 запуск прерывается с подсказкой применить миграции. Пул PostgreSQL:
10 открытых соединений, 5 простаивающих, срок жизни соединения 5 минут.

| Переменная | Значение по умолчанию | Назначение |
| --- | --- | --- |
| `DATABASE_URL` | не задана | Полный DSN; при наличии имеет приоритет над `DB_*` |
| `DB_HOST` | `localhost` | Узел PostgreSQL |
| `DB_PORT` | `5432` | Порт PostgreSQL |
| `DB_USER` | `postgres` | Пользователь PostgreSQL |
| `DB_PASS` | `postgres` | Пароль PostgreSQL |
| `DB_NAME` | `cashapp` | База PostgreSQL |
| `REDIS_ADDR` | `localhost:6379` | Адрес Redis |
| `REDIS_DB` | `0` | Номер логической БД Redis |
| `REDIS_PASSWORD` | пустая строка | Пароль Redis, если настроен на сервере |
| `LEDGER_ADDR` | `:8081` | Адрес HTTP-сервиса Ledger |
| `GATEWAY_ADDR` | `:8080` | Адрес HTTP-сервиса Gateway |

При сборке DSN из `DB_*` используется `sslmode=disable` для локальной разработки.
Запустите Ledger из терминала с указанными переменными окружения:

```sh
cd ledger
go run ./cmd/ledger
```

Затем запустите Gateway в другом терминале с тем же `DATABASE_URL` из корня репозитория:

```sh
cd gateway
go run .
```

Оба процесса работают до `Ctrl+C`, завершают HTTP-запросы и освобождают ресурсы
через полученную функцию `closeFn`. Gateway передаёт инициализацию БД фабрике
Ledger; отдельный процесс Ledger и Redis для работы Gateway не нужны.

## Проверка Ledger через HTTP

Примеры рассчитаны на новую базу и выполняются последовательно. JSON-поля
бюджетов и транзакций совпадают с Gateway: `category`, `limit`, `amount`,
`description`, `date`, `id`. Для отдельного HTTP Ledger дата обязательна в формате
`YYYY-MM-DD`; суммы положительные, не более `999999999999.99`, с точностью
до двух десятичных знаков. Сначала вызывается общая доменная валидация,
затем сервис `NewStrict` проверяет прежние ограничения даты и денежных сумм.

Создайте бюджет — ожидается `201`:

```sh
curl -i -X POST http://localhost:8081/api/budgets \
  -H 'Content-Type: application/json' \
  -d '{"category":"еда","limit":500}'
```

Добавьте расход в пределах бюджета — ожидается `201` с назначенным БД `id`:

```sh
curl -i -X POST http://localhost:8081/api/transactions \
  -H 'Content-Type: application/json' \
  -d '{"amount":450,"category":"еда","description":"ланч","date":"2026-11-06"}'
```

Превысьте оставшийся лимит — ожидается `409` и `{"error":"budget exceeded"}`,
строка в `expenses` не добавляется:

```sh
curl -i -X POST http://localhost:8081/api/transactions \
  -H 'Content-Type: application/json' \
  -d '{"amount":100,"category":"еда","description":"кофе","date":"2026-11-06"}'
```

Прочитайте списки из PostgreSQL:

```sh
curl -i http://localhost:8081/api/budgets
curl -i http://localhost:8081/api/transactions
docker compose exec postgres psql -U postgres -d cashapp -c 'TABLE budgets; TABLE expenses;'
```

Бюджеты отсортированы по категории, расходы — по `date DESC, id DESC`.
Пустые списки представлены как `[]`. Повторный POST бюджета обновляет его лимит
без дубликата категории и без удаления расходов. Лимит учитывает всю историю
категории, равенство лимиту разрешено; категория без бюджета не ограничена.
Обновление лимита ниже уже потраченной суммы сохраняет историю и запрещает
новые расходы до увеличения лимита.

Остановите только Ledger (`Ctrl+C`), снова выполните `go run ./cmd/ledger`
с тем же DSN и повторите оба GET. Бюджет и расход должны остаться в ответах
и таблицах. Данные Gateway также сохраняются при его перезапуске с тем же DSN.

## Отчёт и Redis

Дважды подряд запросите один интервал, обе границы включаются:

```sh
curl -i 'http://localhost:8081/api/reports/summary?from=2026-11-01&to=2026-11-30'
curl -i 'http://localhost:8081/api/reports/summary?from=2026-11-01&to=2026-11-30'
docker compose exec redis redis-cli -n 0 GET 'report:summary:2026-11-01:2026-11-30'
docker compose exec redis redis-cli -n 0 TTL 'report:summary:2026-11-01:2026-11-30'
```

При отсутствии ключа первый запрос агрегирует `SUM(amount)` по категориям
в PostgreSQL, сохраняет JSON в Redis с TTL 30 секунд и пишет `cache miss` в лог.
Формат ответа:

```json
{"from":"2026-11-01","to":"2026-11-30","total":450,"categories":[{"category":"еда","amount":450}]}
```

Повторный запрос до истечения TTL возвращает тот же отчёт с `cache hit` в логе.
`TTL` показывает оставшееся время жизни; после истечения ключ исчезает.
Если ключ уже был заполнен предыдущим запросом, дождитесь истечения TTL.

Вставка расхода не сбрасывает кэш: отчёт может отставать от БД до 30 секунд.
При ошибке Redis во время работы Ledger логирует причину и считает отчёт
по PostgreSQL. PostgreSQL остаётся источником истины. Бюджеты не кэшируются.
При собственном `REDIS_DB` подставьте его вместо `-n 0`.

В отчёт входят расходы обоих приложений с корректной датой `YYYY-MM-DD`.
Записи Gateway без даты или с произвольной/некорректной датой исключаются
из отчёта по датам, но всегда учитываются при проверке бюджета.

## Проверка Gateway

Gateway сохраняет прежние пути и JSON-контракты:

```sh
curl -i http://localhost:8080/ping
curl -i -X POST http://localhost:8080/api/budgets \
  -H 'Content-Type: application/json' -d '{"category":"gateway-еда","limit":500}'
curl -i -X POST http://localhost:8080/api/transactions \
  -H 'Content-Type: application/json' \
  -d '{"amount":450,"category":"gateway-еда"}'
curl -i -X POST http://localhost:8080/api/transactions \
  -H 'Content-Type: application/json' \
  -d '{"amount":100,"category":"gateway-еда"}'
curl -i http://localhost:8080/api/budgets
curl -i http://localhost:8080/api/transactions
```

Ожидаются соответственно `200`, `201`, `201`, `409`, `200`, `200`.
Категория `gateway-еда` должна быть новой: данные сохраняются между запусками.
Пустые списки возвращаются как `[]`, бюджеты идут по категории, расходы —
в порядке добавления (`id ASC`). `description` и `date` остаются необязательными,
дата допускает произвольный текст, денежные значения не округляются до копеек.
ID назначает PostgreSQL; последовательность может иметь пропуски. Ошибки JSON
и валидации дают `400`, превышение — `409`, внутренние ошибки — `500`;
формат остаётся `{"error":"…"}`. После перезапуска Gateway оба GET сохраняют данные.
Отчёт на порту 8080 отсутствует; кэш проверяется через Ledger на 8081.

## Проверки разработки

Из корня репозитория:

```sh
gofmt -l .
(cd gateway && go test ./... && go vet ./...)
(cd ledger && go test ./... && go vet ./...)
(cd gateway && go test -race ./...)
(cd ledger && go test -race ./...)
```

`gofmt -l .` не должен выводить имена файлов. Интеграционные тесты требуют
отдельной доступной PostgreSQL и Redis; без переменных ниже они пропускаются:

```sh
export LEDGER_TEST_DATABASE_URL="$DATABASE_URL"
export LEDGER_TEST_REDIS_ADDR="$REDIS_ADDR"
(cd ledger && go test ./... -count=1)
```

Используйте тестовые сервисы: SQL-тесты изолируют данные отдельной схемой,
проверки Redis используют отдельные ключи в БД 0 без пароля (как в Compose).
HTTP-тесты Gateway используют настоящий сервис с отдельными тестовыми
репозиториями в памяти. Проверки PostgreSQL/Redis находятся в
`ledger/internal/integration/`, проверки фабрик — в `ledger/app/`.
Обычные модульные тесты внешние сервисы не требуют. Правила разработки находятся в [AGENTS.md](AGENTS.md)
и каталоге [rules](rules/).
