# API

[English](../api.md) | Русский

## Клиентский API (gateway, `proxy.address`)

Gateway проксирует **только инференс-ручки** — те, в которых клиент указывает
модель. Инфо/админ-ручки Ollama он не проксирует: gateway выполняет инференс, а не
управляет удалённой Ollama.

| Запрос | Поведение |
| --- | --- |
| `POST /api/generate`, `POST /api/chat`, `POST /api/embed`, `POST /api/embeddings`, `POST /v1/chat/completions`, `POST /v1/completions`, `POST /v1/embeddings` | проксируется по модели из тела запроса |
| `GET /` | отвечает сам gateway: `200 Centaur is running` |
| остальные пути (`/api/version`, `/api/tags`, `/api/ps`, `/api/show`, `/api/pull`, `/api/push`, `/api/create`, `/api/copy`, `/api/delete`, `/v1/models`, `/metrics`, ...) | `501 Not Implemented` |

> Метрики Prometheus отдаются не на `proxy.address`, а на отдельном
> `metrics.address` — см. [`metrics.md`](metrics.md).

Коды ответов клиентского API:

| Код | Когда |
| --- | --- |
| `200` | запрос обслужен (возможен также `4xx/5xx` самой Ollama) |
| `400` | инференс-ручка, но тело не разбирается как JSON |
| `501` | путь не является инференс-ручкой |
| `502` | коннектор не смог проксировать запрос в Ollama |
| `503` | нет свободного туннеля под запрошенную модель |

Имя модели берётся из тела запроса. Gateway читает тело целиком, чтобы его разобрать,
поэтому по модели маршрутизируются и крупные запросы — `/api/chat` с картинками
бывает в десятки мегабайт. Тело занимает память gateway, пока не уйдёт в туннель;
верхней границы размера тела пока нет.

## API пула туннелей (gateway, `pool.apiAddress`)

Защищён заголовком `ApiKey`.

| Метод и путь | Заголовки | Тело | Ответ |
| --- | --- | --- | --- |
| `POST /api/v1/tunnels/register` | `ApiKey` | `{"models":["..."]}` | `{"token":"..."}` |
| `GET /healthz` | `ApiKey` | — | `ok` |

Неверный или отсутствующий ключ — `403 Forbidden`. Пустой список моделей или
битый JSON — `422 Unprocessable Entity`.

После регистрации коннектор подключается к TCP-адресу пула (`pool.address`) и
пишет туда 16-байтовый `token` — это handshake, по которому gateway привязывает
соединение к туннелю; подробности — в [`architecture.md`](architecture.md).
