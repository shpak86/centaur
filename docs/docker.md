# Docker deployment

[English](docker.md) | [Русский](ru/docker.md)

The repository does not include prebuilt images, but building them is straightforward. Both programs (`cmd/gateway` and `cmd/connector`) produce static binaries, so one multi-stage `Dockerfile` with a build argument is sufficient.

## Dockerfile

Create a `Dockerfile` in the repository root:

```dockerfile
# --- build ---
FROM golang:1.27 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# TARGET = gateway | connector
ARG TARGET=gateway
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
      -o /out/centaur ./cmd/${TARGET}

# --- runtime ---
FROM debian:bookworm-slim

COPY --from=build /out/centaur /usr/bin/centaur

# Mount the config from the host: /etc/centaur/gateway.json or /etc/centaur/connector.json
ENTRYPOINT ["/usr/bin/centaur"]
```

Build both images:

```sh
docker build --build-arg TARGET=gateway   -t centaur-gateway:latest   .
docker build --build-arg TARGET=connector -t centaur-connector:latest .
```

The build image tag (`golang:1.27`) must match the version in `go.mod`. The runtime image can be smaller—for example, `scratch` or `distroless/static`—because the binary is static and only needs its config file alongside it.

## docker-compose.yml

```yaml
services:
  gateway:
    image: centaur-gateway:latest
    command: ["-config", "/etc/centaur/gateway.json"]
    volumes:
      - ./config/gateway.json:/etc/centaur/gateway.json:ro
    ports:
      - "18180:18180"   # proxy.address — clients
      - "18181:18181"   # pool.address — connector tunnels
      - "18182:18182"   # pool.apiAddress — tunnel registration
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

## Container configuration

Inside the containers, the Gateway must **listen** on `0.0.0.0`, while the connector **dials** the Gateway using its service name.

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

Start and verify:

```sh
docker compose up -d
docker compose ps

curl -s http://127.0.0.1:18180/
curl -s -X POST http://127.0.0.1:18180/api/generate \
  -d '{"model":"llama3.2:1b","prompt":"hello","stream":false}'
curl -s http://127.0.0.1:18183/metrics | grep '^centaur_gateway'
```

## Notes

- The connector's `pool.key` must match one of the Gateway's `pool.keys`. Otherwise, registration returns `403 Forbidden` and the connector retries every second, leaving the pool with no tunnels.
- Ollama running on the host is reachable from a container at `host.docker.internal:11434`. On Linux, add `extra_hosts: ["host.docker.internal:host-gateway"]`. If Ollama also runs in Docker, put it on the same network and use its service name in `target.address`.
- Publish the `metrics.address` port only where Prometheus can scrape it; it is not needed for client traffic.
- The config is mounted read-only (`:ro`); binaries read it once at startup.
