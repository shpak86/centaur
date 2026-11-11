# Metrics

[English](metrics.md) | [Русский](ru/metrics.md)

The Gateway exposes Prometheus metrics at `GET /metrics` on the **separate** `metrics.address`, not on `proxy.address`. The format is Prometheus's standard text exposition format.

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `centaur_gateway_requests_total` | counter | `endpoint`, `code` | Client requests by endpoint and HTTP status |
| `centaur_gateway_request_duration_seconds` | histogram | `endpoint` | Request handling duration, including streaming |
| `centaur_gateway_tunnel_registrations_total` | counter | — | Successful tunnel registrations |
| `centaur_gateway_tunnel_bind_errors_total` | counter | — | Errors binding a connection to a tunnel (handshake) |
| `centaur_gateway_tunnel_acquire_misses_total` | counter | — | Requests that could not acquire a free tunnel (`503`) |
| `centaur_gateway_tunnels` | gauge | `state` (`pending` / `ready`) | Current number of tunnels in the registry |
| `centaur_gateway_tunnel_stale_total` | counter | `state` (`pending` / `ready`) | Tunnels removed from the registry: dead connection (`ready`) or incomplete handshake (`pending`) |

`endpoint` cardinality is bounded: inference endpoints use their own paths, `/` is Gateway health, and all other paths are grouped under `other`. Standard Go runtime (`go_*`) and process (`process_*`) metrics are also exported.

```sh
# Metrics on the separate metrics.address port
curl -s http://127.0.0.1:18183/metrics | grep '^centaur_gateway'
```

For troubleshooting:

- `centaur_gateway_tunnels{state="ready"}` stays at zero — the connector is not connecting: check connector logs and that `pool.key` matches one of the Gateway's `pool.keys`;
- `centaur_gateway_tunnel_acquire_misses_total` is increasing — demand exceeds the number of free tunnels: increase `pool.tunnels`;
- `centaur_gateway_tunnel_stale_total` is increasing — the pool is removing dead tunnels (see [`architecture.md`](architecture.md)).
