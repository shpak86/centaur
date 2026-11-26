# API

[English](api.md) | [Русский](ru/api.md)

## Client API (`proxy.address` on the Gateway)

The Gateway proxies **inference endpoints**—requests that specify a model. Backend info/admin endpoints (Ollama `/api/version`, `/api/ps`, `/api/show`, `/api/pull`, `/api/push`, `/api/create`, `/api/copy`, `/api/delete`, OpenAI `/v1/models/{model}`, and the like) are not proxied: the Gateway runs inference; it does not manage remote inference servers. Two paths are answered by the Gateway itself: the liveness check and the model list. The connector behind a tunnel can be Ollama or any OpenAI-compatible server (vLLM, llama.cpp, LM Studio, …).

| Request | Behavior |
| --- | --- |
| `POST /api/generate`, `POST /api/chat`, `POST /api/embed`, `POST /api/embeddings`, `POST /v1/chat/completions`, `POST /v1/completions`, `POST /v1/embeddings`, `POST /v1/responses` | Proxied to a connector selected by the model in the request body |
| `GET /` | Answered by the Gateway: `200 Centaur is running` |
| `GET /api/tags`, `GET /v1/models` | Answered by the Gateway from the tunnel registry: the models the connected connectors advertise (see [Model list](#model-list)) |
| Other paths (`/api/version`, `/api/ps`, `/api/show`, `/api/pull`, `/api/push`, `/api/create`, `/api/copy`, `/api/delete`, `/v1/models/{model}`, `/metrics`, ...) | `501 Not Implemented` |

> Prometheus metrics are served on a separate `metrics.address`, not on `proxy.address` — see [`metrics.md`](metrics.md).

Client API response codes:

| Code | Meaning |
| --- | --- |
| `200` | Request served (the upstream response may also be `4xx`/`5xx`) |
| `400` | Inference endpoint, but the body is not valid JSON |
| `501` | Path is not an inference endpoint |
| `502` | Connector could not proxy the request to the local inference backend |
| `503` | No free tunnel is available for the requested model |

The model name is read from the request body. The Gateway reads the whole body to parse it, so large requests are routed by model too — `/api/chat` with images can be tens of megabytes. The body occupies the Gateway's memory until it is written to the tunnel; there is no body size cap yet.

## Model list

`GET /api/tags` (the Ollama shape) and `GET /v1/models` (the OpenAI shape) are answered by the Gateway from the tunnel registry: they are not proxied and not forwarded to a connector. The list is the union of the models the connected connectors announce in `target.models` at registration, so it does not depend on which host holds which model, and such a request does not occupy a tunnel.

It is the list of models the pool **can route to**, not of what the backends have loaded:

- a model is listed while at least one tunnel of a connector that announced it is registered, including a tunnel whose handshake has not completed yet;
- a model of a connector that has just died stays in the list until the background sweep (every 5 seconds) drops its tunnels;
- models are de-duplicated, and the order is not defined;
- a model whose tunnels are all busy is still listed: the list describes routing, not free capacity.

```sh
curl -s http://127.0.0.1:18180/api/tags
```

```json
{"models":[{"name":"llama3.2:1b","model":"llama3.2:1b"}]}
```

```sh
curl -s http://127.0.0.1:18180/v1/models
```

```json
{"object":"list","data":[{"id":"llama3.2:1b","object":"model"}]}
```

With an empty pool (`centaur_gateway_tunnels{state="ready"}` is zero) both endpoints return an empty array, not an error.

## Streaming responses

The Gateway passes responses through without buffering: each chunk is flushed to the
client as soon as it arrives from the connector (`FlushInterval: -1` on both proxies),
and the tunnel response is read without a deadline. A long generation is therefore not
cut short, and NDJSON/SSE output arrives incrementally—exactly as with the backend directly.

- `stream` is `true` by default for `/api/generate` and `/api/chat` (NDJSON); set
  `"stream": false` to get a single JSON document.
- `/v1/chat/completions`, `/v1/completions`, and `/v1/responses` stream SSE when the
  request sets `"stream": true`.
- A stream occupies its one-shot tunnel until the response ends, so concurrent streams
  are limited by `pool.tunnels`; requests beyond that limit receive `503`.
- If the client disconnects, the Gateway closes the tunnel and frees the slot.
- Request bodies are still read in full before routing (to extract the model), so only
  responses are streamed.
- `centaur_gateway_request_duration_seconds` includes the whole stream duration.

`curl` buffers output, so use `-N` (`--no-buffer`) to see chunks as they are generated:

```sh
# NDJSON: one JSON object per line, the last one has "done": true
curl -sN -X POST http://127.0.0.1:18180/api/generate \
  -d '{"model":"llama3.2:1b","prompt":"hello"}'
```

```json
{"model":"llama3.2:1b","created_at":"...","response":"Hello","done":false}
{"model":"llama3.2:1b","created_at":"...","response":"!","done":false}
{"model":"llama3.2:1b","created_at":"...","response":"","done":true,"done_reason":"stop"}
```

```sh
# SSE: OpenAI-compatible chunks, terminated by data: [DONE]
curl -sN -X POST http://127.0.0.1:18180/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"llama3.2:1b","stream":true,"messages":[{"role":"user","content":"hello"}]}'
```

```text
data: {"id":"chatcmpl-...","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}

data: [DONE]
```

## Tunnel pool API (`pool.apiAddress` on the Gateway)

Requests are authenticated with the `ApiKey` header.

| Method and path | Headers | Body | Response |
| --- | --- | --- | --- |
| `POST /api/v1/tunnels/register` | `ApiKey` | `{"models":["..."]}` | `{"token":"..."}` |
| `GET /healthz` | `ApiKey` | — | `ok` |

A missing or invalid key returns `403 Forbidden`. An empty model list or invalid JSON returns `422 Unprocessable Entity`.

After registration, the connector connects to the pool's TCP address (`pool.address`) and writes the 16-byte `token` as a handshake. The Gateway uses it to bind the connection to the tunnel; see [`architecture.md`](architecture.md) for details.
