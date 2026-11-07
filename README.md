# Centaur

Centaur — это gateway-прокси для [Ollama](https://ollama.com): коннекторы,
запущенные рядом с локальными Ollama, поднимают исходящие TCP-туннели к gateway,
а клиенты обращаются к единой точке входа. Gateway принимает клиентские
запросы, выбирает подходящий туннель по имени модели в запросе и проксирует
запрос в локальную Ollama за этим туннелем.

```
                      туннель (TCP)
client ──▶ gateway ─────────────────▶ connector ──▶ Ollama (локально)
             proxy.address          pool.address     target.address
```

## Идея

- **Gateway** (`cmd/gateway`) — единая точка входа. Принимает клиентские
  HTTP-запросы и держит пул зарегистрированных туннелей.
- **Connector** (`cmd/connector`) — запускается рядом с Ollama. Регистрируется
  в пуле gateway и поднимает исходящее TCP-соединение (туннель). Запросы,
  пришедшие по туннелю, проксирует в локальную Ollama.
- **Ollama-mock** (`cmd/ollama-mock`) — тестовый сервер, отвечающий в формате
  Ollama API. Нужен, чтобы прогонять всю связку без реальной модели.

Ключевой момент: **туннели одноразовые**. На один клиентский запрос выдаётся
один туннель; после ответа соединение закрывается, и коннектор поднимает новый
туннель. Так переподключение не зависит от состояния клиентского соединения.

## Как это работает

1. Коннектор отправляет `POST /api/v1/tunnels/register` на HTTP API пула
   (заголовок `ApiKey`, тело `{"models": [...]}`) и получает `token`.
2. Коннектор подключается к TCP-адресу пула и первым делом пишет 16-байтовый
   `token` — это handshake. Gateway по токену привязывает соединение к
   зарегистрированному туннелю.
3. Клиентский запрос приходит на `proxy.address`. Gateway разбирает имя модели
   из тела запроса, находит свободный туннель, поддерживающий эту модель, и
   проксирует запрос по туннелю.
4. Коннектор принимает запрос из туннеля и проксирует его в локальную Ollama.
5. После ответа туннель закрывается, коннектор поднимает новый.

## Маршрутизация по модели

Gateway проксирует **только инференс-ручки** — те, в которых клиент указывает
модель. Инфо/админ-ручки Ollama он не проксирует: gateway выполняет инференс,
а не управляет удалённой Ollama.

| Запрос | Поведение |
| --- | --- |
| `POST /api/generate`, `POST /api/chat`, `POST /api/embed`, `POST /api/embeddings`, `POST /v1/chat/completions`, `POST /v1/completions`, `POST /v1/embeddings` | Проксируется по модели из тела запроса |
| `GET /` | Отвечает сам gateway: `200 Centaur is running` |
| Остальные пути (`/api/version`, `/api/tags`, `/api/ps`, `/api/show`, `/api/pull`, `/api/push`, `/api/create`, `/api/copy`, `/api/delete`, `/v1/models`, ...) | `501 Not Implemented` |

Коды ответов proxy:

| Код | Когда |
| --- | --- |
| `200` | Запрос обслужен (возможен также `4xx/5xx` самой Ollama) |
| `400` | Инференс-ручка, но тело не разбирается как JSON |
| `501` | Путь не является инференс-ручкой |
| `502` | Коннектор не смог проксировать запрос в Ollama |
| `503` | Нет свободного туннеля под запрошенную модель |

## Компоненты

| Путь | Назначение |
| --- | --- |
| `cmd/gateway` | Бинарь gateway: конфиг, запуск/остановка серверов |
| `cmd/connector` | Бинарь коннектора: конфиг, запуск/остановка клиента |
| `cmd/ollama-mock` | Мок Ollama API для тестов |
| `internal/api` | `ProxyServer` — клиентский HTTP-сервер и `tunnel.Service` |
| `internal/tunnel/client.go` | Клиент коннектора: регистрация, туннель, прокси в Ollama |
| `internal/tunnel/service.go` | Gateway-сторона: обратный прокси, разбор модели, маршрутизация |
| `internal/tunnel/registry.go` | Реестр туннелей (`Register`/`Bind`/`Acquire`) |
| `internal/tunnel/tunnel.go` | Структура `Tunnel` |
| `internal/tunnel/api_server.go` | HTTP API пула (регистрация туннелей) + `AuthMiddleware` |
| `internal/tunnel/tunnel_server.go` | TCP-сервер пула (handshake) |
| `internal/tunnel/model.go` | Список инференс-ручек и разбор имени модели из тела |
| `internal/tunnel/auth.go` | Проверка API-ключа |
| `internal/tunnel/dto.go` | DTO регистрации туннеля |

## Требования

- Go 1.27+ (см. `go.mod`).
- Внешних зависимостей нет — только стандартная библиотека.

## Сборка и тесты

```sh
go build ./...
go test ./...
go test -race ./...
go vet ./...
```

## Быстрый старт (локальное тестовое развёртывание)

Сценарий из трёх процессов: `ollama-mock` → `connector` → `gateway`. Готовые
примеры конфигов лежат в `deployment/test/` (каталог локальный, в `.gitignore`);
их содержимое приведено в разделе «Конфигурация» ниже.

```sh
# Терминал 1 — мок Ollama
go run ./cmd/ollama-mock -listen=127.0.0.1:11435

# Терминал 2 — gateway
go run ./cmd/gateway -config deployment/test/gateway.json

# Терминал 3 — connector
go run ./cmd/connector -config deployment/test/connector-mock.json
```

Проверка через gateway (`proxy.address` = `127.0.0.1:18180`):

```sh
# health gateway (отвечает сам gateway, не через туннель)
curl -s http://127.0.0.1:18180/

# инфо/админ-ручки Ollama gateway не проксирует — 501
curl -si http://127.0.0.1:18180/api/tags

# инференс: маршрутизируется по модели в теле запроса
curl -s -X POST http://127.0.0.1:18180/api/generate \
  -d '{"model":"mock:7b","prompt":"hello","stream":false}'
```

## Конфигурация

### Gateway (`-config`, по умолчанию `/etc/centaur/server.json`)

```json
{
  "logger": { "level": "info" },
  "proxy":  { "address": "127.0.0.1:18180" },
  "pool": {
    "address": "127.0.0.1:18181",
    "apiAddress": "127.0.0.1:18182",
    "keys": ["<api-key-1>", "<api-key-2>"]
  }
}
```

| Поле | Смысл |
| --- | --- |
| `logger.level` | Уровень логов: `debug`, `info`, `warn`, `error` |
| `proxy.address` | Адрес клиентского HTTP-прокси |
| `pool.address` | Listen-адрес TCP-сервера пула (сюда приходят туннели) |
| `pool.apiAddress` | Listen-адрес HTTP API пула (сюда приходят регистрации); **без схемы** |
| `pool.keys` | Список допустимых API-ключей коннекторов |

### Connector (`-config`, по умолчанию `/etc/centaur/connector.json`)

```json
{
  "logger": { "level": "info" },
  "pool": {
    "address": "127.0.0.1:18181",
    "apiAddress": "http://127.0.0.1:18182",
    "key": "<api-key>"
  },
  "target": {
    "address": "http://127.0.0.1:11434",
    "models": ["llama3:8b", "qwen2.5-coder:7b"]
  }
}
```

| Поле | Смысл |
| --- | --- |
| `logger.level` | Уровень логов: `debug`, `info`, `warn`, `error` |
| `pool.address` | `host:port` TCP-сервера пула (куда подключается коннектор) |
| `pool.apiAddress` | URL HTTP API пула регистрации; **со схемой** (`http://...`) |
| `pool.key` | API-ключ коннектора (должен быть в `pool.keys` gateway) |
| `target.address` | URL локальной Ollama |
| `target.models` | Модели, которые коннектор «объявляет» и по которым маршрутизируется |

> Имена `pool.*` на двух сторонах имеют разный смысл: gateway **слушает** на
> `pool.address`/`pool.apiAddress`, коннектор **ходит** по ним. Оба бинарника
> валидируют формат адресов при старте и падают с внятной ошибкой.

## API пула туннелей

Слушается на `pool.apiAddress` и защищён заголовком `ApiKey`.

| Метод и путь | Заголовки | Тело | Ответ |
| --- | --- | --- | --- |
| `POST /api/v1/tunnels/register` | `ApiKey` | `{"models":["..."]}` | `{"token":"..."}` |
| `GET /healthz` | `ApiKey` | — | `ok` |

Неверный или отсутствующий ключ — `403 Forbidden`. Пустой список моделей или
битый JSON — `422 Unprocessable Entity`.

## Ограничения

- Инфо/админ-ручки Ollama через gateway недоступны (`501`): управлять удалённой
  Ollama gateway не умеет и не должен.
- WebSocket/Upgrade (`101 Switching Protocols`) туннель не поддерживает;
  Ollama его не использует.
- Тело запроса больше 1 МиБ не разбирается ради имени модели (так устроен
  `/api/chat` с крупными `images`), поэтому такой запрос не маршрутизируется по
  модели.
- Туннели одноразовые: под нагрузку часть запросов может получить `503`, пока
  коннектор поднимает новые туннели.

