# Gateway

HTTP API на порту 8080. Gateway получает готовый
`service.ReportingService` от фабрики `ledger/app.New(ctx)` и передаёт его в
`api.NewHandler(service)`. Обработчики сохраняют прежние DTO и формат ошибок,
а валидацию, проверку бюджета и запись выполняет сервис Ledger.

`main.go` вызывает фабрику и освобождает её ресурсы через `defer closeFn()`.
Фабрика читает конфигурацию БД, подключается к PostgreSQL с переданным контекстом
и собирает репозитории и сервис. Ошибка запуска логируется, процесс завершается
с ненулевым кодом. При SIGINT/SIGTERM Gateway завершает HTTP-запросы и закрывает
ресурсы. Код Gateway импортирует только публичные пакеты `ledger/service`
и `ledger/app`; SQL, драйверы и репозитории остаются внутри Ledger.

Gateway вызывает Ledger внутри процесса через локальный импорт модуля.
Бюджеты и транзакции сохраняются в PostgreSQL между перезапусками. Отдельный
HTTP-сервис Ledger на порту 8081 использует ту же БД при одинаковом DSN
и предоставляет отчёты с Redis-кэшем. `GET /api/reports/summary` на порту 8080
возвращает суммы по категориям без обращения к Redis.

## Подготовка БД

Команды из корня репозитория:

```sh
go install github.com/pressly/goose/v3/cmd/goose@v3.21.1
export PATH="$(go env GOPATH)/bin:$PATH"
docker compose up -d --wait
export DATABASE_URL='postgres://postgres:postgres@localhost:5432/cashapp?sslmode=disable'
goose -dir ./ledger/migrations postgres "$DATABASE_URL" up
```

Compose создаёт БД `cashapp` при первом запуске. `DATABASE_URL` задаёт подключение
обоих приложений через фабрику Ledger; без неё используются `DB_HOST`, `DB_PORT`,
`DB_USER`, `DB_PASS`, `DB_NAME` с defaults `localhost`, `5432`, `postgres`,
`postgres`, `cashapp`. Gateway не читает настройки подключения самостоятельно
и не требует Redis. Миграции выполняются отдельно до запуска, фабрика их не запускает.

Откат последней миграции из корня — команда
`goose -dir ./ledger/migrations postgres "$DATABASE_URL" down`;
`down-to 0` удаляет обе таблицы. Откат миграции совместимости допустим, только
если данные соответствуют прежним ограничениям даты и денежных сумм; иначе
он завершается ошибкой без изменения данных. После отката перед запуском
снова выполните `up`. Подробности: [общий README](../README.md).

## Запуск и проверка Gateway

В терминале с заданным `DATABASE_URL` из корня репозитория:

```sh
cd gateway
go run .
```

Адрес по умолчанию — `:8080`; другой адрес задаётся через `GATEWAY_ADDR`.

Прежние эндпоинты и JSON-поля сохранены. Для пустой БД:

```sh
curl -i http://localhost:8080/ping
curl -i -X POST http://localhost:8080/api/budgets \
  -H 'Content-Type: application/json' -d '{"category":"еда","limit":500}'
curl -i -X POST http://localhost:8080/api/transactions \
  -H 'Content-Type: application/json' \
  -d '{"amount":450,"category":"еда","description":"ланч","date":"2026-11-06"}'
curl -i -X POST http://localhost:8080/api/transactions \
  -H 'Content-Type: application/json' \
  -d '{"amount":100,"category":"еда","date":"2026-11-06"}'
curl -i http://localhost:8080/api/budgets
curl -i http://localhost:8080/api/transactions
```

Ожидаются `200` (`"pong"`), `201`, `201`, `409`, `200`, `200`.
Превышение не меняет историю. Пустые списки — `[]`, бюджеты отсортированы
по категории, транзакции — в порядке добавления. `description` и `date`
в Gateway необязательны; формат даты не проверяется. Ошибки JSON
и валидации возвращают `400`, неизвестные пути — `404`, неподдерживаемые методы —
`405`. Ошибки имеют вид `{"error":"описание"}`.

## Отчёт и таймауты запросов

```sh
curl -i 'http://localhost:8080/api/reports/summary?from=2026-11-01&to=2026-11-30'
```

Ответ `200`, например `{"еда":450}`; пустой период — `{}`. Обе даты обязательны,
формат — `YYYY-MM-DD`, границы включаются, `from` не позже `to`. Ошибки дат дают
`400` и JSON с полем `error`. Расчёт в Ledger запускает отдельную горутину
на категорию, использует `sync.WaitGroup` и останавливает heartbeat перед возвратом.

Middleware ограничивает каждый запрос Gateway контекстом на **2 секунды**.
Изменение при запуске: `REQUEST_TIMEOUT=500ms go run .`. Значение — положительная
Go duration; неверное значение останавливает запуск. В коде для явной настройки
есть `api.NewHandlerWithTimeout(service, duration)`. При таймауте или отмене
контекста возвращается **504** и `{"error":"request timed out or canceled"}`.
Контекст передаётся во все SQL-операции; рабочие горутины и heartbeat завершаются
до ответа. Воспроизводимая ручная проверка с малым таймаутом описана
в [общем README](../README.md#таймауты-запросов).

## Проверка отчёта отдельного Ledger

Для отдельного HTTP-сервиса и отчётов запустите Ledger с тем же `DATABASE_URL`:

```sh
export REDIS_ADDR='localhost:6379'
export REDIS_DB='0'
export REDIS_PASSWORD=''
(cd ledger && go run ./cmd/ledger)
```

Ledger на порту **8081** сохраняет обязательную дату `YYYY-MM-DD` и ограничения
сумм прежнего SQL API. Дважды подряд запросите отчёт непосредственно у Ledger:

```sh
curl -i 'http://localhost:8081/api/reports/summary?from=2026-11-01&to=2026-11-30'
curl -i 'http://localhost:8081/api/reports/summary?from=2026-11-01&to=2026-11-30'
```

Первый запрос при отсутствии ключа агрегирует данные PostgreSQL и пишет
`cache miss` в лог Ledger; второй в пределах TTL 30 секунд — `cache hit`.
Расходы Gateway с корректной датой входят в общий отчёт; расходы без даты
или с произвольной строкой учитываются в бюджете, но не в отчёте за даты.
[README Ledger](../ledger/README.md) описывает слои, SQL-хранилище и кэш.

Проверки из каталога `gateway/`:

```sh
go test ./...
go vet ./...
go test -race ./...
```
