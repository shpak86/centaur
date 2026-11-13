# Configuration

[English](configuration.md) | [Русский](ru/configuration.md)

Both binaries accept a JSON config path via the `-config` flag:

- Gateway: defaults to `/etc/centaur/gateway.json`.
- Connector: defaults to `/etc/centaur/connector.json`.

Default configuration files live in [`deployment/packaging/config/gateway.json`](../deployment/packaging/config/gateway.json) and [`deployment/packaging/config/connector.json`](../deployment/packaging/config/connector.json); the release archive ships them as `config/gateway.json` and `config/connector.json`, and they are installed into `/etc/centaur`. Container-oriented variants are in [`deployment/docker/config`](../deployment/docker/config) — see [`docker.md`](docker.md).

## Summary

This combined table gives an overview of both binaries; detailed field types and descriptions follow below.

| Side | Field | Meaning |
| --- | --- | --- |
| gateway | `proxy.address` | Client HTTP proxy listen address |
| gateway | `metrics.address` | Metrics HTTP server listen address (`GET /metrics`) |
| gateway | `pool.address` | Pool TCP server listen address (connectors establish tunnels here) |
| gateway | `pool.apiAddress` | Pool HTTP API listen address; **no scheme** |
| gateway | `pool.keys` | Connector API keys allowed to register |
| connector | `pool.address` | Gateway pool TCP server address |
| connector | `pool.apiAddress` | Pool HTTP API URL; **includes a scheme** (`http://...`) |
| connector | `pool.key` | Connector API key; must be listed in the Gateway's `pool.keys` |
| connector | `pool.tunnels` | Number of concurrent tunnels to maintain (`> 0`) |
| connector | `target.address` | Local Ollama URL |
| connector | `target.models` | Models the connector advertises to the Gateway |

## Gateway

```json
{
  "logger": { "level": "info" },
  "proxy":   { "address": "127.0.0.1:18180" },
  "metrics": { "address": "127.0.0.1:18183" },
  "pool": {
    "address": "127.0.0.1:18181",
    "apiAddress": "127.0.0.1:18182",
    "keys": ["<api-key-1>", "<api-key-2>"]
  }
}
```

| Field | Type | Meaning |
| --- | --- | --- |
| `logger.level` | string | Log level: `debug`, `info`, `warn`, or `error` |
| `proxy.address` | `host:port` | Client HTTP proxy listen address |
| `metrics.address` | `host:port` | Metrics HTTP server listen address; **no scheme**, separate from `proxy.address` |
| `pool.address` | `host:port` | Pool TCP server listen address (connectors establish tunnels here) |
| `pool.apiAddress` | `host:port` | Pool HTTP API listen address (connectors register here); **no scheme** |
| `pool.keys` | `[]string` | Allowed connector API keys |

## Connector

```json
{
  "logger": { "level": "info" },
  "pool": {
    "address": "127.0.0.1:18181",
    "apiAddress": "http://127.0.0.1:18182",
    "key": "<api-key>",
    "tunnels": 4
  },
  "target": {
    "address": "http://127.0.0.1:11434",
    "models": ["llama3:8b", "qwen2.5-coder:7b"]
  }
}
```

| Field | Type | Meaning |
| --- | --- | --- |
| `logger.level` | string | Log level: `debug`, `info`, `warn`, or `error` |
| `pool.address` | `host:port` | Gateway pool TCP server address |
| `pool.apiAddress` | URL | Pool HTTP API URL; **includes a scheme** (`http://...`) |
| `pool.key` | string | Connector API key; must be listed in the Gateway's `pool.keys` |
| `pool.tunnels` | int | Number of tunnels to maintain; must be `> 0` |
| `target.address` | URL | Local Ollama URL |
| `target.models` | `[]string` | Models the connector advertises and can serve |

## Notes

- `pool.*` means different things on each side: the Gateway **listens** on `pool.address`/`pool.apiAddress`, while the connector **dials** them.
- The connector's `pool.apiAddress` requires a URL scheme; the Gateway's is a listen address without a scheme. Both binaries validate this at startup and report an error rather than silently accepting the other side's format.
- The connector's `pool.key` must exactly match one of the Gateway's `pool.keys`. Otherwise, registration returns `403 Forbidden` and the connector retries every second (leaving the pool with zero tunnels).
- `target.models` must match models actually available in Ollama; routing uses the model name in the request body.
