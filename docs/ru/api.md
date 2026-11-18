# API

[English](../api.md) | Русский

## Клиентский API (gateway, `proxy.address`)

Gateway проксирует **только инференс-ручки** — те, в которых клиент указывает
модель. Инфо/админ-ручки бэкенда (Ollama `/api/tags`, OpenAI `/v1/models` и т.п.)
он не проксирует: gateway выполняет инференс, а не управляет удалёнными
inference-серверами. За туннелем может стоять Ollama или любой
OpenAI-совместимый сервер (vLLM, llama.cpp, LM Studio, …).

| Запрос | Поведение |
| --- | --- |
| `POST /api/generate`, `POST /api/chat`, `POST /api/embed`, `POST /api/embeddings`, `POST /v1/chat/completions`, `POST /v1/completions`, `POST /v1/embeddings`, `POST /v1/responses` | проксируется по модели из тела запроса |
| `GET /` | отвечает сам gateway: `200 Centaur is running` |
| остальные пути (`/api/version`, `/api/tags`, `/api/ps`, `/api/show`, `/api/pull`, `/api/push`, `/api/create`, `/api/copy`, `/api/delete`, `/v1/models`, `/metrics`, ...) | `501 Not Implemented` |

> Метрики Prometheus отдаются не на `proxy.address`, а на отдельном
> `metrics.address` — см. [`metrics.md`](metrics.md).

Коды ответов клиентского API:

| Код | Когда |
| --- | --- |
| `200` | запрос обслужен (возможен также `4xx/5xx` upstream-бэкенда) |
| `400` | инференс-ручка, но тело не разбирается как JSON |
| `501` | путь не является инференс-ручкой |
| `502` | коннектор не смог проксировать запрос в локальный inference-бэкенд |
| `503` | нет свободного туннеля под запрошенную модель |

Имя модели берётся из тела запроса. Gateway читает тело целиком, чтобы его разобрать,
поэтому по модели маршрутизируются и крупные запросы — `/api/chat` с картинками
бывает в десятки мегабайт. Тело занимает память gateway, пока не уйдёт в туннель;
верхней границы размера тела пока нет.

## Потоковые ответы

Gateway отдаёт ответ без буферизации: каждый чанк уходит клиенту сразу, как приходит
из туннеля (`FlushInterval: -1` на обеих сторонах), а ответ из туннеля читается без
дедлайна. Поэтому долгая генерация не обрывается, а NDJSON/SSE приходит
инкрементально — так же, как при прямом обращении к бэкенду.

- у `/api/generate` и `/api/chat` `stream` по умолчанию `true` (NDJSON); чтобы
  получить один JSON-документ, укажите `"stream": false`;
- `/v1/chat/completions`, `/v1/completions` и `/v1/responses` отдают SSE, если в
  запросе `"stream": true`;
- поток занимает свой одноразовый туннель до конца ответа, поэтому число
  одновременных потоков ограничено `pool.tunnels`; запросы сверх лимита получают
  `503`;
- если клиент отключился, gateway закрывает туннель и освобождает слот;
- тело запроса по-прежнему читается целиком до маршрутизации (чтобы достать модель),
  поэтому потоковым является только ответ;
- `centaur_gateway_request_duration_seconds` учитывает всё время потока.

`curl` буферизует вывод, поэтому используйте `-N` (`--no-buffer`), чтобы видеть
чанки по мере генерации:

```sh
# NDJSON: по одному JSON-объекту в строке, в последнем "done": true
curl -sN -X POST http://127.0.0.1:18180/api/generate \
  -d '{"model":"llama3.2:1b","prompt":"hello"}'
```

```json
{"model":"llama3.2:1b","created_at":"...","response":"Hello","done":false}
{"model":"llama3.2:1b","created_at":"...","response":"!","done":false}
{"model":"llama3.2:1b","created_at":"...","response":"","done":true,"done_reason":"stop"}
```

```sh
# SSE: чанки в формате OpenAI, завершаются data: [DONE]
curl -sN -X POST http://127.0.0.1:18180/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"llama3.2:1b","stream":true,"messages":[{"role":"user","content":"hello"}]}'
```

```text
data: {"id":"chatcmpl-...","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}

data: [DONE]
```

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
