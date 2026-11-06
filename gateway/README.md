# Gateway

HTTP API домашнего задания 5 на порту 8080. Код Gateway оставлен без изменений:
он использует прежний тип `ledger.Ledger` внутри процесса через локальный импорт
модуля. Бюджеты и транзакции Gateway хранятся в памяти и теряются при перезапуске.

Отдельный сервис Ledger теперь хранит данные в PostgreSQL и кэширует отчёты
в Redis, однако исходный Gateway не пересылает ему HTTP-запросы. Поэтому
проверить базу и кэш **через Gateway нельзя**, а `GET /api/reports/summary`
на порту 8080 возвращает `404`. Запросы к Ledger выполняйте на порту 8081.

## Запуск PostgreSQL, Redis и Ledger

Команды из корня репозитория:

```sh
go install github.com/pressly/goose/v3/cmd/goose@v3.21.1
export PATH="$(go env GOPATH)/bin:$PATH"
docker compose up -d --wait
export DATABASE_URL='postgres://postgres:postgres@localhost:5432/cashapp?sslmode=disable'
export REDIS_ADDR='localhost:6379'
export REDIS_DB='0'
export REDIS_PASSWORD=''
goose -dir ./ledger/migrations postgres "$DATABASE_URL" up
(cd ledger && go run ./cmd/ledger)
```

Compose создаёт БД `cashapp` при первом запуске. `DATABASE_URL` задаёт подключение
Ledger; без неё используются `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASS`, `DB_NAME`
с defaults `localhost`, `5432`, `postgres`, `postgres`, `cashapp`. Redis по умолчанию
доступен на `localhost:6379`, БД 0, без пароля. `REDIS_PASSWORD` используется,
если пароль настроен на самом сервере Redis. Gateway эти переменные не читает.

Откат последней миграции из корня — команда
`goose -dir ./ledger/migrations postgres "$DATABASE_URL" down`;
`down-to 0` удаляет обе таблицы. Откат удаляет данные, после него перед запуском
Ledger снова выполните `up`. Подробности: [общий README](../README.md).

## Запуск и проверка Gateway

В другом терминале из корня репозитория:

```sh
cd gateway
go run .
```

Прежние эндпоинты и JSON-поля сохранены. Для пустого Gateway:

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
в прежнем Gateway необязательны; формат даты не проверяется. Ошибки JSON
и валидации возвращают `400`, неизвестные пути — `404`, неподдерживаемые методы —
`405`. Ошибки имеют вид `{"error":"описание"}`.

## Проверка отчёта отдельного Ledger

Для проверки PostgreSQL повторите POST/GET с портом **8081** и обязательной
датой расхода. Дважды подряд запросите отчёт непосредственно у Ledger:

```sh
curl -i 'http://localhost:8081/api/reports/summary?from=2026-11-01&to=2026-11-30'
curl -i 'http://localhost:8081/api/reports/summary?from=2026-11-01&to=2026-11-30'
```

Первый запрос при отсутствии ключа агрегирует данные PostgreSQL и пишет
`cache miss` в лог Ledger; второй в пределах TTL 30 секунд — `cache hit`.
Данные из примеров Gateway в этот отчёт не входят. Перезапуск отдельного Ledger
с тем же DSN сохраняет его бюджеты и расходы. [README Ledger](../ledger/README.md)
описывает SQL-хранилище и кэш.

Проверки из каталога `gateway/`:

```sh
go test ./...
go vet ./...
go test -race ./...
```
