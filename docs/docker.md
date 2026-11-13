# Docker deployment

[English](docker.md) | [Русский](ru/docker.md)

The repository ships two Dockerfiles in [`deployment/docker`](../deployment/docker): `gateway.Dockerfile` and `connector.Dockerfile`, one per role. Each machine runs a single container — the Gateway on a server, the connector next to Ollama — so only the image for that role is built; the two images are independent and never need to run on the same host. Both binaries are static, connect only outbound, log JSON to stdout, and shut down gracefully on `SIGTERM`, so the images are built `FROM scratch`, run as an unprivileged user, and need nothing but a mounted config file.

The workflow is the same on Linux, macOS, and Windows: clone the repository, build the image, run it with your own configuration.

## Requirements

- Docker Engine or Docker Desktop with Compose v2. BuildKit (the default builder since Docker 23) is required: the Dockerfiles use `RUN --mount=type=cache` and the automatic `TARGETOS`/`TARGETARCH` arguments.
- On Windows use Linux containers — Docker Desktop does that by default through the WSL2 backend. Windows containers are not supported.
- `git`, and a config file of your own.

## Build

```sh
git clone https://github.com/shpak86/centaur.git
cd centaur
docker build -f deployment/docker/gateway.Dockerfile   -t centaur-gateway:local   .
docker build -f deployment/docker/connector.Dockerfile -t centaur-connector:local .
```

The build context is the repository root and `-f` points at the Dockerfile: `docker build deployment/docker/` fails with `failed to read dockerfile`, and building from inside `deployment/docker/` fails with `go.mod` not found.

Both images are small (`FROM scratch`: one binary and a CA bundle) and run as UID/GID 65532:

```sh
docker images | grep centaur
docker image inspect -f '{{.Config.User}} {{.Config.Entrypoint}}' centaur-gateway:local
```

The build stage is pinned to the Go version from `go.mod` (`golang:1.27.2`), so the toolchain is never downloaded inside the container. Dependencies are cached in BuildKit mounts, so a rebuild after a code change takes seconds. The images carry OCI labels; pass your own values with `--build-arg VERSION=... --build-arg REVISION=...`.

### Other platforms

`GOOS`/`GOARCH` come from BuildKit's `TARGETOS`/`TARGETARCH` and Go cross-compiles, so no emulation is involved:

```sh
# amd64 server image built on an Apple Silicon or ARM machine
docker buildx build --platform linux/amd64 -f deployment/docker/gateway.Dockerfile \
  -t centaur-gateway:local --load .

# both architectures: this needs a registry to push to
docker buildx build --platform linux/amd64,linux/arm64 -f deployment/docker/gateway.Dockerfile \
  -t registry.example.com/centaur-gateway:v0.1.5 --push .
```

A plain `docker build` produces an image for the host architecture: `arm64` on Apple Silicon, `amd64` on x86 hosts.

## Configuration

Both binaries take the config path with `-config`, defaulting to `/etc/centaur/gateway.json` and `/etc/centaur/connector.json`. The images contain no config on purpose: without a mounted file the process exits with `read config ...: no such file or directory` instead of quietly listening on loopback.

Start from the container templates in [`deployment/docker/config`](../deployment/docker/config):

```sh
mkdir -p config
cp deployment/docker/config/gateway.json   config/gateway.json
cp deployment/docker/config/connector.json config/connector.json
openssl rand -hex 16   # paste the same value into both files
```

Two things differ from the host templates in [`deployment/packaging/config`](../deployment/packaging/config):

- the Gateway **listens on `0.0.0.0`** (`proxy.address`, `pool.address`, `pool.apiAddress`, `metrics.address`). Addresses bound to `127.0.0.1` are unreachable through `-p`, because the published port forwards to the container's external interface;
- the connector **dials the Gateway at the address from its own config** (`gateway.example.com:18181` in the template — replace it with the real host) and **Ollama through `host.docker.internal`**, which works on Docker Desktop out of the box and needs `--add-host` on Linux.

The pool key must match on both sides: every value of the Gateway's `pool.keys` is accepted, the connector sends its `pool.key`. A mismatch leaves the connector retrying `register tunnel: unexpected status "403 Forbidden"` and the pool without tunnels, so clients receive `503`.

`config/gateway.json`:

```json
{
  "logger":  { "level": "info" },
  "proxy":   { "address": "0.0.0.0:18180" },
  "metrics": { "address": "0.0.0.0:18183" },
  "pool": {
    "address": "0.0.0.0:18181",
    "apiAddress": "0.0.0.0:18182",
    "keys": ["<pool-key>"]
  }
}
```

`config/connector.json`:

```json
{
  "logger": { "level": "info" },
  "pool": {
    "address": "gateway.example.com:18181",
    "apiAddress": "http://gateway.example.com:18182",
    "key": "<pool-key>",
    "tunnels": 4
  },
  "target": {
    "address": "http://host.docker.internal:11434",
    "models": ["llama3.2:1b"]
  }
}
```

The template ships `gateway.example.com` as a placeholder: `pool.address` is a bare `host:port` of the Gateway and `pool.apiAddress` is a URL of the same host, so both have to be replaced with the address where the Gateway is reachable from the connector machine.

`target.models` must list only the models actually pulled in Ollama: the Gateway routes by the model name in the request body and answers `503` when no tunnel advertises it.

The config is read once at startup, so mount it read-only and restart the container after editing. The directory `config/` is git-ignored — it holds the pool key.

To keep the config anywhere else, mount it at a custom path and pass `-config`:

```sh
docker run --rm \
  -v "$PWD/my/gateway.json:/cfg/gateway.json:ro" \
  centaur-gateway:local -config /cfg/gateway.json
```

On Linux with SELinux add `:z` to the volume (`...:/cfg/gateway.json:ro,z`).

## Run the Gateway

```sh
docker run -d --name centaur-gateway \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  -p 18180:18180 -p 18181:18181 -p 18182:18182 -p 18183:18183 \
  -v "$PWD/config:/etc/centaur:ro" \
  centaur-gateway:local
```

The image already runs as UID 65532 and the binary defaults to `/etc/centaur/gateway.json`, so no command arguments are needed. On Windows (PowerShell) the volume becomes `-v "${PWD}/config:/etc/centaur:ro"`.

| Port | Config field | Purpose |
| --- | --- | --- |
| 18180 | `proxy.address` | client API: inference requests and `GET /` health |
| 18181 | `pool.address` | connector tunnels |
| 18182 | `pool.apiAddress` | tunnel registration from connectors |
| 18183 | `metrics.address` | Prometheus metrics, not needed for traffic |

`18181` and `18182` must be reachable from every host that runs a connector; the pool key is the only authentication. Put `18180` behind a reverse proxy with TLS if the Gateway is public, and publish `18183` only where Prometheus can scrape it.

The Gateway keeps no state: restarting or recreating the container loses nothing but live tunnels, and connectors re-register on their own.

## Run the connector

The connector opens outbound connections only — to the Gateway pool and to `target.address` — so it publishes no ports and needs no inbound access. What you need depends on where Ollama and the Gateway are.

### Ollama and the Gateway on the same host (Linux)

```sh
docker run -d --name centaur-connector \
  --network=host \
  -v "$PWD/config:/etc/centaur:ro" \
  centaur-connector:local
```

With `--network=host` the container shares the host network namespace, so the ordinary host-level addresses work: `pool.address: "127.0.0.1:18181"`, `pool.apiAddress: "http://127.0.0.1:18182"`, `target.address: "http://127.0.0.1:11434"`. This is the simplest option on a Linux workstation and requires no changes to Ollama. `--network=host` is Linux-only.

### Ollama on the host, bridge network

```sh
docker run -d --name centaur-connector \
  --add-host=host.docker.internal:host-gateway \
  -v "$PWD/config:/etc/centaur:ro" \
  centaur-connector:local
```

- macOS and Windows: `host.docker.internal` already resolves to the host and reaches services bound to the host's loopback, so `target.address: "http://host.docker.internal:11434"` works as is.
- Linux: `host.docker.internal` resolves to the `docker0` address, so Ollama must listen on all interfaces — run it with `OLLAMA_HOST=0.0.0.0:11434` (or `OLLAMA_HOST=0.0.0.0` in the launchd/systemd unit). If it stays on `127.0.0.1:11434`, the connector logs `Ollama proxy error ... connection refused` and clients receive `502`, so use `--network=host` instead.

The connector dials the Gateway the same way: `pool.address: "host.docker.internal:18181"`, `pool.apiAddress: "http://host.docker.internal:18182"` when the Gateway runs on the host itself.

### Remote Gateway

Nothing special: put the reachable address into the config, no flags or extra hosts.

```json
"pool": {
  "address": "gateway.example.com:18181",
  "apiAddress": "http://gateway.example.com:18182"
}
```

### Ollama in Docker

The connector can also reach Ollama over a user-defined network instead of the host: create it with `docker network create centaur`, run the Ollama container on it, and point `target.address` at the container name, for example `http://ollama:11434`. No `host.docker.internal` involved.

| Gateway | Ollama | `pool.address` / `pool.apiAddress` | `target.address` |
| --- | --- | --- | --- |
| host | host | `host.docker.internal:18181` / `http://host.docker.internal:18182` | `http://host.docker.internal:11434` |
| remote host | host | `gateway.example.com:18181` / `http://gateway.example.com:18182` | `http://host.docker.internal:11434` |
| any | container | unchanged | `http://ollama:11434` |

`--add-host=host.docker.internal:host-gateway` is required on Linux for every `host.docker.internal` entry in the table and is harmless on Docker Desktop.

## Both roles on one host

The normal layout is one role per machine, but for a quick end-to-end check both containers can run on a single host — still two independent `docker run` containers, nothing else to install:

```sh
mkdir -p config
cp deployment/docker/config/gateway.json   config/gateway.json
cp deployment/docker/config/connector.json config/connector.json
# put the same random key into pool.keys (gateway.json) and pool.key (connector.json);
# in connector.json replace gateway.example.com with host.docker.internal, and
# keep in target.models only the models pulled in Ollama

docker build -f deployment/docker/gateway.Dockerfile   -t centaur-gateway:local   .
docker build -f deployment/docker/connector.Dockerfile -t centaur-connector:local .

docker run -d --name centaur-gateway \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  -p 18180:18180 -p 18181:18181 -p 18182:18182 -p 18183:18183 \
  -v "$PWD/config:/etc/centaur:ro" centaur-gateway:local

docker run -d --name centaur-connector \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  --add-host=host.docker.internal:host-gateway \
  -v "$PWD/config:/etc/centaur:ro" centaur-connector:local

docker logs -f centaur-connector
```

Here the connector reaches the Gateway through its published ports, so its config uses `host.docker.internal` for both `pool.*` and `target.address`; `--add-host` provides that name on Linux. Removing the containers does not touch the config:

```sh
docker rm -f centaur-gateway centaur-connector
```

## Verify

```sh
# health: answered by the Gateway itself, not through a tunnel
curl -s http://127.0.0.1:18180/

# inference: the tunnel is selected by the model in the request body
curl -s -X POST http://127.0.0.1:18180/api/generate \
  -d '{"model":"llama3.2:1b","prompt":"hello","stream":false}'

# Ollama info/admin endpoints are not proxied by the Gateway -> 501
curl -si http://127.0.0.1:18180/api/tags

# Prometheus metrics
curl -s http://127.0.0.1:18183/metrics | grep '^centaur_gateway'

# logs are JSON lines on stdout
docker logs centaur-gateway
docker logs centaur-connector
```

The connector prints `connector started` with `poolApiAddress`, `poolAddress` and `targetAddress` at startup, which is the quickest way to check that the mounted config is the one you meant.

## Update

```sh
git pull
# on the Gateway host
docker build --pull -f deployment/docker/gateway.Dockerfile -t centaur-gateway:local .
# on every connector host
docker build --pull -f deployment/docker/connector.Dockerfile -t centaur-connector:local .
```

Then recreate the container of that role with the `docker run` command from above (`docker rm -f centaur-gateway` or `docker rm -f centaur-connector` first). The mounted config is kept: compare it with `deployment/docker/config/*.json` after an update and merge new fields by hand.

## What is inside the images

- `FROM scratch`: one static binary and `/etc/ssl/certs/ca-certificates.crt`, no shell, no package manager. There is nothing to `docker exec ... sh` into; debug through `docker logs`, metrics, and by running the same binary on the host.
- Unprivileged `USER 65532:65532`, no writable paths, no capabilities, no setuid binaries — so `--read-only`, `--cap-drop ALL`, and `no-new-privileges` (already used in the commands above) break nothing.
- Logs go to stdout/stderr, so `docker logs` and journald both work; the process handles `SIGTERM` and stops within 10 seconds, which is inside Docker's default grace period.
- The config is read once at startup and is mounted read-only: recreate the container to apply changes.

## Troubleshooting

| Symptom | Cause |
| --- | --- |
| `failed to solve: ... go.mod: not found` or `failed to read dockerfile` | the build context is wrong: build from the repository root with `-f deployment/docker/<name>.Dockerfile .` |
| `read config "/etc/centaur/gateway.json": no such file or directory` | the config is not mounted, or only `connector.json` is present: mount the directory or the right file |
| connector logs `register tunnel: unexpected status "403 Forbidden"` | `pool.key` is not listed in the Gateway's `pool.keys` |
| clients receive `503` | the pool has no tunnel: the connector is down, the key is wrong, or `target.models` does not contain the requested model |
| connector logs `Ollama proxy error ... connection refused` for `172.17.0.1:11434` and clients get `502 upstream error` | Ollama listens on `127.0.0.1` only — start it with `OLLAMA_HOST=0.0.0.0:11434`, or run the connector with `--network=host` and `127.0.0.1` in the config |
| `curl 127.0.0.1:18180` connects but the pool never fills | the Gateway's addresses are still `127.0.0.1` in the config: inside the container they must be `0.0.0.0` |
| `permission denied` while mounting the config on Fedora/RHEL | SELinux: add `:z` to the volume (`./config:/etc/centaur:ro,z`) |
| connector logs `dial tcp: lookup host.docker.internal ... no such host` | on Linux the name does not exist by default: add `--add-host=host.docker.internal:host-gateway` or use `--network=host` with `127.0.0.1` in the config |
| `exec format error` right after start | the image was built for another architecture: rebuild with `--platform` matching the host |

Images are not published to a registry; build them locally from the clone. For deployments without Docker see [`systemd.md`](systemd.md) and the release archives in [releases](https://github.com/shpak86/centaur/releases).
