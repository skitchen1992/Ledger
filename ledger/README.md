# Ledger

Ledger — HTTP-сервис учёта расходов на Go. Бюджеты и расходы хранятся
в PostgreSQL, отчёт по интервалу кэшируется в Redis на 30 секунд.
При запуске демонстрационные данные и `budgets.json` не загружаются.

## Запуск

Из корня репозитория установите Goose, создайте БД и примените миграции:

```sh
go install github.com/pressly/goose/v3/cmd/goose@v3.21.1
export PATH="$(go env GOPATH)/bin:$PATH"
docker compose up -d --wait
export DATABASE_URL='postgres://postgres:postgres@localhost:5432/cashapp?sslmode=disable'
export REDIS_ADDR='localhost:6379'
export REDIS_DB='0'
export REDIS_PASSWORD=''
goose -dir ./ledger/migrations postgres "$DATABASE_URL" up
cd ledger
go run ./cmd/ledger
```

Compose создаёт `cashapp` при первом запуске PostgreSQL с пустым томом.
Успешные подключения к PostgreSQL и Redis выводятся в лог. Если `Ping` любого
сервиса не прошёл, Ledger завершает запуск с ошибкой. По умолчанию HTTP слушает
`:8081`; адрес меняется через `LEDGER_ADDR`.

`DATABASE_URL` имеет приоритет. Без него DSN собирается из `DB_HOST=localhost`,
`DB_PORT=5432`, `DB_USER=postgres`, `DB_PASS=postgres`, `DB_NAME=cashapp`
с `sslmode=disable`. Значения Redis по умолчанию: `REDIS_ADDR=localhost:6379`,
`REDIS_DB=0`, `REDIS_PASSWORD` пустой. Локальный Compose не задаёт пароль Redis;
переменная клиента применяется при подключении к серверу с авторизацией.
Пул PostgreSQL: 10 открытых, 5 простаивающих соединений, время жизни 5 минут.

В миграциях `00001_budgets.sql` и `00002_expenses.sql` секции `Up`/`Down`
находятся в одном файле, как требует Goose. Откат последней миграции из корня:

```sh
goose -dir ./ledger/migrations postgres "$DATABASE_URL" down
```

`down-to 0` откатывает обе миграции. Откат удаляет таблицы и данные;
перед следующим запуском повторите `up`.

## HTTP-проверка

Для новой базы выполните по порядку:

```sh
curl -i -X POST http://localhost:8081/api/budgets \
  -H 'Content-Type: application/json' -d '{"category":"еда","limit":500}'
curl -i -X POST http://localhost:8081/api/transactions \
  -H 'Content-Type: application/json' \
  -d '{"amount":450,"category":"еда","description":"ланч","date":"2026-11-06"}'
curl -i -X POST http://localhost:8081/api/transactions \
  -H 'Content-Type: application/json' \
  -d '{"amount":100,"category":"еда","date":"2026-11-06"}'
curl -i http://localhost:8081/api/budgets
curl -i http://localhost:8081/api/transactions
curl -i 'http://localhost:8081/api/reports/summary?from=2026-11-01&to=2026-11-30'
curl -i 'http://localhost:8081/api/reports/summary?from=2026-11-01&to=2026-11-30'
```

Первые два POST дают `201`; превышение — `409` без вставки. GET читают данные
из БД. При перезапуске Ledger с тем же DSN данные сохраняются. Бюджет обновляется
через UPSERT по категории; при отсутствии бюджета расходы разрешены.
Дата расхода обязательна (`YYYY-MM-DD`), суммы положительные, до двух десятичных
знаков и не более `999999999999.99`. Транзакции возвращаются по `date DESC, id DESC`,
бюджеты — по категории.

Первый запрос отчёта при пустом кэше пишет `cache miss` в лог, следующий
в течение 30 секунд — `cache hit`. Ключ:
`report:summary:2026-11-01:2026-11-30`, значение — JSON. После добавления расхода
отчёт может отставать на время оставшегося TTL. Ошибка Redis во время работы
логируется; отчёт вычисляется через PostgreSQL. Списки бюджетов не кэшируются.

**Gateway не подключён к этому сервису:** по требованию его код сохранён.
Он использует прежний тип `ledger.Ledger` в памяти; запросы к Gateway на 8080
не проверяют PostgreSQL/Redis. Маршрута отчёта в Gateway нет.

[Общая инструкция](../README.md) содержит проверку SQL, TTL, конфигурацию портов
и интеграционных тестов. В каталоге `ledger/` обычные проверки запускаются так:

```sh
go test ./...
go vet ./...
```
