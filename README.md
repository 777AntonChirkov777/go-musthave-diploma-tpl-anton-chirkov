## Требования

- Go 1.26 или новее (см. `go.mod`).
- PostgreSQL 13 или новее. Сервис проверялся на PostgreSQL 16.
- Система расчёта начислений (accrual).
- Docker нужен только для быстрого запуска PostgreSQL. Он не обязателен.

## Сборка

```bash
go build -o cmd/gophermart/gophermart ./cmd/gophermart
```

На Windows укажите выходной файл `cmd/gophermart/gophermart.exe`. Запуск без отдельной сборки:

```bash
go run ./cmd/gophermart -d "postgres://postgres:postgres@localhost:5432/gophermart?sslmode=disable" -r http://localhost:8081
```

Справка по флагам: `go run ./cmd/gophermart -h`.

## Конфигурация

| Параметр | Флаг | Переменная окружения | Ключ YAML | По умолчанию | Описание |
|---|---|---|---|---|---|
| Адрес HTTP-сервера | `-a` | `RUN_ADDRESS` | `run_address` | `localhost:8080` | Адрес в формате `host:port`; порт от 0 до 65535 |
| PostgreSQL | `-d` | `DATABASE_URI` | `database_uri` | — | Обязательный URI подключения, например `postgres://user:pass@host:5432/db?sslmode=disable` |
| Система начислений | `-r` | `ACCRUAL_SYSTEM_ADDRESS` | `accrual_system_address` | — | Обязательный адрес accrual: `http://host:port` или `host:port` (тогда используется `http://`) |
| Файл конфигурации | `-c` | — | — | `config.yaml` | Путь к YAML-файлу |

Каждый параметр определяется независимо. Источники перечислены от высшего приоритета к низшему:

1. явно переданный флаг командной строки, даже с пустым значением (`-r=`);
2. непустая переменная окружения (пустая игнорируется);
3. YAML-файл;
4. значение по умолчанию.

YAML-файл по умолчанию (`config.yaml` в рабочем каталоге) необязателен: если его нет, он просто пропускается. Если путь задан явно через `-c`, файл обязан существовать. Неизвестные ключи, несколько YAML-документов в файле и некорректный YAML считаются ошибкой запуска. Пример лежит в [`config.example.yaml`](config.example.yaml):

```bash
cp config.example.yaml config.yaml
```

Если не задан `DATABASE_URI` или адрес accrual, сервис завершается с ошибкой конфигурации до подключения к БД и открытия порта.

## База данных

Быстрый способ поднять PostgreSQL:

```bash
docker run -d --name gophermart-db -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=gophermart -p 5432:5432 postgres:16
```
При старте сервис применяет встроенные миграции goose из `internal/infrastructure/postgres/migrations`. Их применение защищено advisory-блокировкой, поэтому несколько экземпляров могут стартовать одновременно. Миграции создают:

- таблицы `users`, `user_sessions`, `orders`, `withdrawals`;
- ENUM-типы `order_status` (`NEW`, `PROCESSING`, `INVALID`, `PROCESSED`) и `order_check_backoff` (`short`, `long`);
- служебную таблицу goose `goose_db_version`.

## Система расчёта начислений (accrual)

Выберите бинарный файл под свою ОС: `accrual_linux_amd64`, `accrual_darwin_amd64`, `accrual_darwin_arm64` или `accrual_windows_amd64`. Флаги такие же, как в CI: `-a` задаёт адрес, `-d` — URI PostgreSQL.

```bash
./cmd/accrual/accrual_linux_amd64 -a localhost:8081 -d "postgres://postgres:postgres@localhost:5432/gophermart?sslmode=disable"
```

Чтобы проверить начисления вручную, зарегистрируйте в accrual механику вознаграждения и заказ:

```bash
curl -X POST http://localhost:8081/api/goods -H "Content-Type: application/json" -d '{"match":"Bork","reward":10,"reward_type":"%"}'
```

```bash
curl -X POST http://localhost:8081/api/orders -H "Content-Type: application/json" -d '{"order":"12345678903","goods":[{"description":"Чайник Bork","price":7000}]}'
```

После этого загрузите тот же номер в гофермарт через `POST /api/user/orders`.

## Взаимодействие сервисов

```
клиент ──HTTP API──▶ gophermart ──SQL──▶ PostgreSQL
                         │
                         └──GET /api/orders/{number}──▶ accrual
```

- Клиент регистрируется или входит (`/api/user/register`, `/api/user/login`) и получает токен сессии двумя способами: в заголовке `Authorization: Bearer <token>` и в cookie `session`. Остальные ручки `/api/user/*` принимают любой из них; если валидный заголовок есть, используется он. `GET /health` работает без аутентификации.
- Номер заказа должен проходить проверку Луна и состоять не более чем из 255 цифр, иначе ответ `422`.
- Загруженный заказ (`POST /api/user/orders`) сохраняется со статусом `NEW` и сразу ставится в очередь опроса accrual. Фоновый воркер (4 потока) раз в секунду забирает заказы, у которых подошёл срок проверки, и запрашивает `GET /api/orders/{number}`.
- Статусы accrual `REGISTERED` и `PROCESSING` переводят заказ в `PROCESSING` с частым переопросом (от 1 с до 10 с). Статусы `INVALID` и `PROCESSED` окончательные; для `PROCESSED` сохраняется начисление. Ответ `204` (заказ не зарегистрирован) и ошибки accrual дают редкий переопрос с экспоненциальной задержкой до 5 мин.
- Ответ `429` приостанавливает все запросы к accrual на время из `Retry-After` (по умолчанию 60 с).
- Незавершённые заказы продолжают опрашиваться после перезапуска сервиса: расписание хранится в БД.
- Баланс (`GET /api/user/balance`) равен сумме начислений заказов в статусе `PROCESSED` минус сумма списаний.
- Списание (`POST /api/user/balance/withdraw`) регистрируется только в гофермарте, во внешние системы оно не уходит. К номеру применяются те же правила, что и при загрузке (Луна, не более 255 цифр). На каждый номер допускается ровно одно списание среди всех пользователей. Повторный запрос с тем же номером, даже с той же суммой, получает `422`.
- Сервис принимает тела запросов, сжатые gzip (`Content-Encoding: gzip`), и сжимает ответы, если клиент прислал `Accept-Encoding: gzip`.

## Тесты

Модульные тесты:

```bash
go test ./...
```

Интеграционные тесты с PostgreSQL запускаются, когда задана переменная `TEST_DATABASE_URI`. Без неё они пропускаются. Каждый тест работает в собственной временной схеме и удаляет её по завершении, поэтому существующие таблицы базы не затрагиваются.

```bash
TEST_DATABASE_URI="postgres://postgres:postgres@localhost:5432/gophermart?sslmode=disable" go test ./...
```