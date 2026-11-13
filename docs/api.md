# API

[English](api.md) | [Русский](ru/api.md)

## Client API (`proxy.address` on the Gateway)

The Gateway proxies **inference endpoints only**—requests that specify a model. It does not proxy Ollama info/admin endpoints: the Gateway runs inference; it does not manage remote Ollama instances.

| Request | Behavior |
| --- | --- |
| `POST /api/generate`, `POST /api/chat`, `POST /api/embed`, `POST /api/embeddings`, `POST /v1/chat/completions`, `POST /v1/completions`, `POST /v1/embeddings` | Proxied to a connector selected by the model in the request body |
| `GET /` | Answered by the Gateway: `200 Centaur is running` |
| Other paths (`/api/version`, `/api/tags`, `/api/ps`, `/api/show`, `/api/pull`, `/api/push`, `/api/create`, `/api/copy`, `/api/delete`, `/v1/models`, `/metrics`, ...) | `501 Not Implemented` |

> Prometheus metrics are served on a separate `metrics.address`, not on `proxy.address` — see [`metrics.md`](metrics.md).

Client API response codes:

| Code | Meaning |
| --- | --- |
| `200` | Request served (the Ollama response may also be `4xx`/`5xx`) |
| `400` | Inference endpoint, but the body is not valid JSON |
| `501` | Path is not an inference endpoint |
| `502` | Connector could not proxy the request to Ollama |
| `503` | No free tunnel is available for the requested model |

The model name is read from the request body. The Gateway reads the whole body to parse it, so large requests are routed by model too — `/api/chat` with images can be tens of megabytes. The body occupies the Gateway's memory until it is written to the tunnel; there is no body size cap yet.

## Tunnel pool API (`pool.apiAddress` on the Gateway)

Requests are authenticated with the `ApiKey` header.

| Method and path | Headers | Body | Response |
| --- | --- | --- | --- |
| `POST /api/v1/tunnels/register` | `ApiKey` | `{"models":["..."]}` | `{"token":"..."}` |
| `GET /healthz` | `ApiKey` | — | `ok` |

A missing or invalid key returns `403 Forbidden`. An empty model list or invalid JSON returns `422 Unprocessable Entity`.

After registration, the connector connects to the pool's TCP address (`pool.address`) and writes the 16-byte `token` as a handshake. The Gateway uses it to bind the connection to the tunnel; see [`architecture.md`](architecture.md) for details.
