# Build and development

[English](development.md) | [Русский](ru/development.md)

## Requirements

- **Go 1.27+** — the exact version is in [`go.mod`](../go.mod) (`go 1.27.2`).
- **Ollama** is needed only for manually testing inference; builds and tests do not depend on it.
- **No CGO required**: the binary can be built statically.
- The repository has no build generators or task runners (`Makefile`/`Taskfile`) and no linter configuration. Use the `go` tool directly.

The only direct dependency is `github.com/prometheus/client_golang` (`v1.23.2`) for metrics; the other modules in `go.sum` are transitive dependencies. Everything else uses the standard library: HTTP proxying, TCP tunnels, config parsing, and logging (`log/slog`).

## Build

```sh
go build ./...                         # verify that all packages build
go build -o bin/gateway   ./cmd/gateway
go build -o bin/connector ./cmd/connector
```

For deployment to another machine, build a static binary without build paths or debug information:

```sh
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/gateway ./cmd/gateway
```

## Run from source

```sh
go run ./cmd/gateway   -config deployment/packaging/config/gateway.json
go run ./cmd/connector -config deployment/packaging/config/connector.json
```

`-config` specifies the JSON config path. Defaults are `/etc/centaur/gateway.json` and `/etc/centaur/connector.json`. See [`configuration.md`](configuration.md) for all fields and [`docker.md`](docker.md) for container deployment.

## Tests

```sh
go test ./...
go test -race ./...
go test -cover ./...
```

Tests are self-contained: they listen on `127.0.0.1:0` (letting the OS choose a port) and communicate over loopback. They need no Ollama, external network access, or free ports `18180`–`18183` from the default configs.

Run `-race` regularly: the tunnel registry, tunnel acquisition, and background stale-tunnel cleanup run in multiple goroutines.

## Formatting and checks

```sh
gofmt -l .     # should print nothing
go vet ./...
```

## Package map

See [`architecture.md`](architecture.md) for details. At a glance:

- `cmd/gateway`, `cmd/connector` — entry points;
- `internal/tunnel` — tunnels, registry, registration, and model-based routing;
- `internal/api` — HTTP servers: client proxy and metrics exposition;
- `internal/metrics` — Prometheus registry and Gateway metrics.
