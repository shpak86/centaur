# Architecture

[English](architecture.md) | [Русский](ru/architecture.md)

Centaur is a gateway proxy in front of [Ollama](https://ollama.com) or any OpenAI-compatible inference server. Connectors run next to the local inference backend and open outbound TCP connections—tunnels—to the Gateway. Clients use the Gateway like a regular inference API, including the model list: `GET /api/tags` and `GET /v1/models` are answered by the Gateway from the tunnel registry, not proxied.

```
                      tunnel (TCP)
client ──▶ gateway ─────────────────▶ connector ──▶ Ollama / OpenAI-compatible
             proxy.address          pool.address     target.address

prometheus ──▶ gateway (metrics.address, GET /metrics)
```

The inference backend does not need to be reachable from outside: the Gateway does not open ports on the backend side. The connector always initiates the connection, so it only needs outbound access to the Gateway.

## One-shot tunnels

Each tunnel serves exactly one client request. After the response, the connection closes and the connector opens a new one. Reconnection is independent of the client connection state: if a connection drops, the next request uses a new tunnel instead of a stale one.

Each connector maintains a pool of `pool.tunnels` tunnels. They are ready to accept requests while idle and serve up to `pool.tunnels` requests concurrently under load. Thus, `pool.tunnels` sets both the parallelism and the upper limit: requests beyond the number of free tunnels receive `503`.

## Registration and handshake

1. The connector sends `POST /api/v1/tunnels/register` to the pool HTTP API (with an `ApiKey` header and `{"models": [...]}` body) and receives a `token`.
2. The connector connects to the pool's TCP address and writes the 16-byte `token` first; this is the handshake.
3. The Gateway uses the token to bind the connection to the registered tunnel. Before the handshake the tunnel is `pending`; afterwards it is `ready`.
4. A client request arrives at `proxy.address`. The Gateway reads the model name from the request body, finds a free tunnel that supports the model, and proxies the request through it.
5. The connector receives the request from the tunnel and proxies it to its local inference backend (Ollama or an OpenAI-compatible server).
6. After the response, the tunnel closes and the connector opens another one. `Serve` starts `pool.tunnels` such workers to keep the pool replenished.

## Pool and stale tunnel cleanup

The pool avoids handing out dead tunnels:

- **Before acquisition**, the Gateway peeks at the incoming connection without consuming data (`MSG_PEEK` on Unix sockets). If the connector has closed or dropped the connection, the tunnel is discarded.
- **A background registry sweep** runs every `sweepInterval` (5 seconds) and removes tunnels with no activity, including `pending` tunnels whose handshake never completed.

Without these checks, tunnels belonging to a crashed connector would remain marked as ready, be handed to clients, and fail with `502` when writing to the closed connection. The number removed is exposed as `centaur_gateway_tunnel_stale_total`.

One case cannot be detected this way: a silent network failure with no FIN/RST (a “black hole”). It is detected only by TCP keepalive on the tunnel server (30-second interval, about 5 minutes to detect), so a request sent through such a tunnel may end with `502`.

## Model list

The Gateway answers the model-list endpoints itself, from the tunnel registry, without occupying a tunnel:

- `GET /api/tags` — the Ollama shape: `{"models":[{"name":"...","model":"..."}]}`;
- `GET /v1/models` — the OpenAI shape: `{"object":"list","data":[{"id":"...","object":"model"}]}`.

The list is `Registry.Models()`: the union of the models the connectors announce in `target.models` at registration, de-duplicated, order undefined. It describes what the pool can route to, not what the backends have loaded: a model is listed while a tunnel that announced it is registered—even a `pending` one—and it disappears only when the background sweep drops the last such tunnel. No tunnel is consumed by these requests, so they work with an empty pool (returning an empty array).

Every other backend info/admin endpoint is not proxied and answers `501` (see [`api.md`](api.md)).

## Why metrics use a separate address

Metrics are served at `metrics.address` (`GET /metrics`), not at `proxy.address`:

- A dedicated port can listen on loopback and stay off the network perimeter.
- Prometheus scrapes the Gateway independently of client traffic.
- `proxy.address` remains an exact inference API surface, without extra endpoints.

## Package map

| Path | Purpose |
| --- | --- |
| `cmd/gateway` | Gateway binary: configuration and server lifecycle |
| `cmd/connector` | Connector binary: configuration and client lifecycle |
| `internal/api/proxy_server.go` | `ProxyServer`, the client-facing HTTP server |
| `internal/api/metrics_server.go` | `MetricsServer`, the metrics HTTP server |
| `internal/tunnel/service.go` | Gateway side: reverse proxy, model parsing, routing, and the model-list endpoints |
| `internal/tunnel/registry.go` | Tunnel registry: `Register`/`Bind`/`Acquire`, and the models it advertises (`Models`) |
| `internal/tunnel/tunnel.go` | `Tunnel` structure and `pending`/`ready` states |
| `internal/tunnel/tunnel_server.go` | Pool TCP server: handshake and tunnel acquisition |
| `internal/tunnel/api_server.go` | Pool HTTP API (registration) and `AuthMiddleware` |
| `internal/tunnel/client.go` | Connector client: registration, tunnel, and proxy to the local backend |
| `internal/tunnel/model.go` | Ollama and OpenAI-compatible inference endpoints, and model-name extraction from request bodies |
| `internal/tunnel/auth.go` | API key validation |
| `internal/tunnel/dto.go` | Tunnel registration and model-list DTOs |
| `internal/metrics` | Prometheus registry, Gateway metrics, and `/metrics` handler |

## Design notes

- The Gateway does not proxy backend info/admin endpoints: it runs inference; it does not manage remote inference servers. The model list is the exception: `GET /api/tags` and `GET /v1/models` are answered by the Gateway from the tunnel registry instead of being proxied.
- The request body of model endpoints is read in full to extract the model name (`internal/tunnel/model.go`): `model` may sit anywhere in the JSON, and `/api/chat` with large `images` is tens of megabytes. The body is then restored and written into the tunnel; until that moment it occupies Gateway memory.
- WebSocket/Upgrade (`101 Switching Protocols`) is not supported through tunnels; Ollama does not use it.
- `pool.*` means different things on each side: the Gateway **listens** on `pool.address`/`pool.apiAddress`, while the connector **dials** them. Both binaries validate address formats at startup and fail with a clear error if they are invalid.
