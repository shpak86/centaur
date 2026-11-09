# Запуск в Docker

[English](../docker.md) | Русский

Готовых образов в репозитории нет — собираются они тривиально. На выходе два
статических бинаря (`cmd/gateway` и `cmd/connector`), поэтому хватает одного
многоступенчатого `Dockerfile` с параметром сборки.

## Dockerfile

В корне репозитория создайте `Dockerfile`:

```dockerfile
# --- сборка ---
FROM golang:1.27 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# TARGET = gateway | connector
ARG TARGET=gateway
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
      -o /out/centaur ./cmd/${TARGET}

# --- рантайм ---
FROM debian:bookworm-slim

COPY --from=build /out/centaur /usr/bin/centaur

# конфиг монтируется снаружи: /etc/centaur/gateway.json или /etc/centaur/connector.json
ENTRYPOINT ["/usr/bin/centaur"]
```

Сборка двух образов:

```sh
docker build --build-arg TARGET=gateway   -t centaur-gateway:latest   .
docker build --build-arg TARGET=connector -t centaur-connector:latest .
```

Тег образа сборки (`golang:1.27`) должен соответствовать версии из `go.mod`.
Рантайм-образ можно взять меньше — например `scratch` или `distroless/static`:
бинарь статический, рядом с ним нужен только файл конфига.

## docker-compose.yml

```yaml
services:
  gateway:
    image: centaur-gateway:latest
    command: ["-config", "/etc/centaur/gateway.json"]
    volumes:
      - ./config/gateway.json:/etc/centaur/gateway.json:ro
    ports:
      - "18180:18180"   # proxy.address   — клиенты
      - "18181:18181"   # pool.address    — туннели коннекторов
      - "18182:18182"   # pool.apiAddress — регистрация туннелей
      - "18183:18183"   # metrics.address — Prometheus
    restart: unless-stopped

  connector:
    image: centaur-connector:latest
    command: ["-config", "/etc/centaur/connector.json"]
    volumes:
      - ./config/connector.json:/etc/centaur/connector.json:ro
    extra_hosts:
      - "host.docker.internal:host-gateway"
    depends_on:
      - gateway
    restart: unless-stopped
```

## Конфиги для контейнеров

Внутри контейнеров gateway должен **слушать** на `0.0.0.0`, а коннектор —
**ходить** на gateway по имени сервиса.

`config/gateway.json`:

```json
{
  "logger":  { "level": "info" },
  "proxy":   { "address": "0.0.0.0:18180" },
  "metrics": { "address": "0.0.0.0:18183" },
  "pool": {
    "address": "0.0.0.0:18181",
    "apiAddress": "0.0.0.0:18182",
    "keys": ["<api-key>"]
  }
}
```

`config/connector.json`:

```json
{
  "logger": { "level": "info" },
  "pool": {
    "address": "gateway:18181",
    "apiAddress": "http://gateway:18182",
    "key": "<api-key>",
    "tunnels": 4
  },
  "target": {
    "address": "http://host.docker.internal:11434",
    "models": ["llama3.2:1b"]
  }
}
```

Запуск и проверка:

```sh
docker compose up -d
docker compose ps

curl -s http://127.0.0.1:18180/
curl -s -X POST http://127.0.0.1:18180/api/generate \
  -d '{"model":"llama3.2:1b","prompt":"hello","stream":false}'
curl -s http://127.0.0.1:18183/metrics | grep '^centaur_gateway'
```

## Замечания

- `pool.key` коннектора должен совпадать с одним из `pool.keys` gateway, иначе
  коннектор получит `403 Forbidden` и будет переподключаться каждую секунду
  (туннелей будет 0).
- Ollama на хосте достаётся из контейнера по `host.docker.internal:11434` — в Linux
  для этого нужен `extra_hosts: ["host.docker.internal:host-gateway"]`. Если Ollama
  тоже в Docker, положите её в ту же сеть и укажите в `target.address` имя сервиса.
- Порт `metrics.address` публикуйте только там, где его скрейпит Prometheus: на
  клиентском трафике он не нужен.
- Конфиг монтируется только для чтения (`:ro`) — бинарники читают его один раз при
  старте.
