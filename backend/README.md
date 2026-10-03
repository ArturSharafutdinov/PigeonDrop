# Backend (Go)

Сервер PigeonDrop на Go: принимает записи с часов, хранит их, конвертирует и (позже) запускает цепочки. Заменяет Node-прототип из `server/`. Выбор стека: [`STACK_COMPARISON.md`](../STACK_COMPARISON.md), набор B.

**Состояние:** каркас (конфиг, PostgreSQL, миграции, `/healthz`, Docker, CI). Маршруты текущего контракта (`API_SPEC.md`) переносятся следующими шагами.

## Быстрый старт

Нужны Go 1.26+ и серверные программы PostgreSQL (`brew install postgresql@16`).

```bash
cd backend
scripts/dev-db.sh up        # своя временная PostgreSQL на 127.0.0.1:55432, данные в .dev/ (чужие базы не трогает)
make test                   # тесты; базу берут из TEST_DATABASE_URL, по умолчанию из dev-db.sh
make run                    # сервер на :3000, миграции применяются при старте
curl localhost:3000/healthz
scripts/dev-db.sh down      # остановить
```

Тесты сами создают отдельную базу со случайным именем и удаляют её. Если базы нет, тесты пропускаются; с `REQUIRE_DB=1` (так в CI) они падают.

## Структура

| Путь | Что там |
|---|---|
| `api/openapi.yaml` | **Контракт HTTP API: источник правды.** Код под него генерируется |
| `internal/api` | Сгенерированные типы и маршруты (`make generate`); руками не править |
| `internal/httpapi` | Обработчики и общие middleware (логи, перехват паники, JSON-ошибки) |
| `internal/config` | Настройки из переменных окружения |
| `internal/db` | Пул соединений и применение миграций |
| `internal/testdb` | Отдельная база для каждого теста |
| `migrations/` | SQL-миграции (goose); встроены в бинарник |
| `cmd/pigeondrop` | Точка входа: `serve`, `migrate`, `healthcheck`, `version` |
| `tools/` | Отдельный модуль с генераторами (oapi-codegen, sqlc, goose), чтобы не раздувать зависимости сервера |

## Команды

```bash
make generate   # после правки api/openapi.yaml (и запросов sqlc, когда появятся)
make check      # gofmt + go vet + тесты (то же, что CI)
make build      # bin/pigeondrop
```

Команды самой программы:

```bash
pigeondrop serve            # HTTP-сервер
pigeondrop migrate up       # применить миграции
pigeondrop migrate status   # состояние миграций
pigeondrop healthcheck      # код 0, если локальный сервер отвечает (для HEALTHCHECK в Docker)
```

## Настройки (переменные окружения)

| Переменная | По умолчанию | Назначение |
|---|---|---|
| `DATABASE_URL` | — (обязательна) | Строка подключения к PostgreSQL |
| `PORT` | `3000` | Порт HTTP-сервера |
| `STORAGE_DIR` | `./data` (в образе `/data`) | Где лежат записи; в Docker это том |
| `DEFAULT_SERVER_URL` | `http://localhost:$PORT` | Публичный адрес сервера, по нему часы отправляют записи |
| `MIGRATE_ON_START` | `false` | Применять миграции при старте (для одного экземпляра); иначе запускайте `migrate up` отдельным шагом |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `MAX_DB_CONNS` | `10` | Размер пула: PostgreSQL на VPS общий с другими проектами |

Если в базе есть неприменённые миграции, `serve` не стартует и говорит об этом.

## Правила работы

- Код и комментарии в нём — на английском, документы — на русском.
- Контракт меняем **сначала в `api/openapi.yaml`**, затем `make generate`, затем код. CI проверяет, что сгенерированное совпадает с закоммиченным.
- Миграции только вперёд и только новыми файлами; уже применённые не правим.
- Изменения идут через ветки и pull request-ы.

## Docker

```bash
docker build -t pigeondrop-backend backend
```

Образ собирается в один статический бинарник, работает не от root; состояние (`STORAGE_DIR=/data`) нужно хранить на томе.
