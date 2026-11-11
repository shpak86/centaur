# Метрики

[English](../metrics.md) | Русский

Gateway отдаёт метрики Prometheus на `GET /metrics` — на **отдельном** адресе
`metrics.address`, а не на `proxy.address`. Формат — стандартный экспозиционный
текст Prometheus.

| Метрика | Тип | Метки | Смысл |
| --- | --- | --- | --- |
| `centaur_gateway_requests_total` | counter | `endpoint`, `code` | клиентские запросы по эндпоинту и HTTP-статусу |
| `centaur_gateway_request_duration_seconds` | histogram | `endpoint` | длительность обработки запроса, включая стриминг |
| `centaur_gateway_tunnel_registrations_total` | counter | — | успешные регистрации туннелей |
| `centaur_gateway_tunnel_bind_errors_total` | counter | — | ошибки привязки соединения к туннелю (handshake) |
| `centaur_gateway_tunnel_acquire_misses_total` | counter | — | запросы, которым не досталось свободного туннеля (`503`) |
| `centaur_gateway_tunnels` | gauge | `state` (`pending` / `ready`) | сколько туннелей сейчас в реестре |
| `centaur_gateway_tunnel_stale_total` | counter | `state` (`pending` / `ready`) | туннели, вычищенные из реестра: соединение мертво (`ready`) или handshake не состоялся (`pending`) |

`endpoint` ограничен по кардинальности: инференс-ручки идут по своему пути, `/` —
health gateway, всё прочее схлопывается в `other`. Дополнительно экспортируются
стандартные метрики рантайма (`go_*`) и процесса (`process_*`).

```sh
# метрики на отдельном порту metrics.address
curl -s http://127.0.0.1:18183/metrics | grep '^centaur_gateway'
```

Что смотреть при разборе проблем:

- `centaur_gateway_tunnels{state="ready"}` держится на нуле — коннектор не
  подключается: проверьте логи коннектора и совпадение `pool.key` / `pool.keys`;
- `centaur_gateway_tunnel_acquire_misses_total` растёт — запросов больше, чем
  свободных туннелей: увеличьте `pool.tunnels`;
- `centaur_gateway_tunnel_stale_total` растёт — пул выбрасывает мёртвые туннели
  (см. [`architecture.md`](architecture.md)).
