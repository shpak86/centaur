# Конфигурация

[English](../configuration.md) | Русский

Оба бинарника принимают путь к JSON-конфигу флагом `-config`:

- gateway — по умолчанию `/etc/centaur/gateway.json`;
- коннектор — по умолчанию `/etc/centaur/connector.json`.

Конфиги по умолчанию лежат в репозитории:
[`deployment/packaging/config/gateway.json`](../../deployment/packaging/config/gateway.json) и
[`deployment/packaging/config/connector.json`](../../deployment/packaging/config/connector.json);
в релизном архиве они называются `config/gateway.json` и `config/connector.json`
и устанавливаются в `/etc/centaur`. Варианты для контейнеров — в
[`deployment/docker/config`](../../deployment/docker/config), см. [`docker.md`](docker.md).

## Сводка

Одна таблица на оба бинарника — чтобы увидеть картину целиком, не переключаясь
между сторонами:

| Сторона | Поле | Смысл |
| --- | --- | --- |
| gateway | `proxy.address` | listen-адрес клиентского HTTP-прокси |
| gateway | `metrics.address` | listen-адрес HTTP-сервера метрик (`GET /metrics`) |
| gateway | `pool.address` | listen-адрес TCP-сервера пула (сюда приходят туннели) |
| gateway | `pool.apiAddress` | listen-адрес HTTP API пула; **без схемы** |
| gateway | `pool.keys` | API-ключи коннекторов, которым разрешена регистрация |
| connector | `pool.address` | адрес TCP-сервера пула gateway |
| connector | `pool.apiAddress` | URL HTTP API пула; **со схемой** (`http://...`) |
| connector | `pool.key` | API-ключ коннектора — должен быть в `pool.keys` gateway |
| connector | `pool.tunnels` | сколько туннелей держать одновременно (`> 0`) |
| connector | `target.address` | URL локальной Ollama |
| connector | `target.models` | модели, которые коннектор объявляет gateway |

Дальше — те же поля с типами и деталями по каждой стороне отдельно.

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

| Поле | Тип | Смысл |
| --- | --- | --- |
| `logger.level` | string | уровень логов: `debug`, `info`, `warn`, `error` |
| `proxy.address` | `host:port` | listen-адрес клиентского HTTP-прокси |
| `metrics.address` | `host:port` | listen-адрес HTTP-сервера метрик; **без схемы**, отдельный от `proxy.address` |
| `pool.address` | `host:port` | listen-адрес TCP-сервера пула (сюда приходят туннели) |
| `pool.apiAddress` | `host:port` | listen-адрес HTTP API пула (сюда приходят регистрации); **без схемы** |
| `pool.keys` | `[]string` | допустимые API-ключи коннекторов |

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

| Поле | Тип | Смысл |
| --- | --- | --- |
| `logger.level` | string | уровень логов: `debug`, `info`, `warn`, `error` |
| `pool.address` | `host:port` | адрес TCP-сервера пула gateway |
| `pool.apiAddress` | URL | URL HTTP API пула; **со схемой** (`http://...`) |
| `pool.key` | string | API-ключ коннектора; должен входить в `pool.keys` gateway |
| `pool.tunnels` | int | сколько туннелей держать одновременно; должно быть `> 0` |
| `target.address` | URL | URL локальной Ollama |
| `target.models` | `[]string` | модели, которые коннектор объявляет и по которым маршрутизируется |

## Общие замечания

- `pool.*` на двух сторонах означает разное: gateway **слушает** на
  `pool.address`/`pool.apiAddress`, коннектор **ходит** по ним.
- `pool.apiAddress` у коннектора требует схему, у gateway — наоборот, listen-адрес
  без схемы. Оба бинарника проверяют это при старте и падают с внятной ошибкой,
  поэтому молча скопировать конфиг с одной стороны на другую не получится.
- `pool.key` коннектора должен точно совпадать с одним из `pool.keys` gateway,
  иначе регистрация вернёт `403 Forbidden`, а коннектор будет переподключаться
  каждую секунду (туннелей будет 0).
- `target.models` должны соответствовать моделям, реально доступным в Ollama:
  маршрутизация идёт по имени модели из тела запроса.
