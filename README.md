# Centaur

[English](README.md) | [Русский](README.ru.md)

Centaur builds a distributed inference network behind a single API. It combines everyday computing devices—MacBooks, workstations, local servers, and GPU hosts—into one pool, without requiring a separate production infrastructure.

The project is designed for community-first use: participants can connect their machines and share compute without public IP addresses, port forwarding, or complex network setup. Each node establishes an outbound tunnel to a central server. The `Gateway` routes requests by model name and proxies inference to network participants.

An inference host starts sharing capacity as soon as the Connector runs there with the Gateway connection settings — requests for its models are then routed to that host.

To use the shared capacity, clients only send requests to the Gateway; no additional setup is needed.

```
                      tunnel (TCP)
client ──▶ gateway ─────────────────▶ connector ──▶ Ollama
```

## Key ideas

- **Everyday machines as inference nodes.** Use available hardware: laptops, workstations, local servers, and GPU machines.
- **Outbound tunnels.** Nodes connect to the Gateway, so no inbound access, public IP, or port forwarding is needed.
- **Model-based routing.** The Gateway selects a node using the model name in the request.
- **Tunnel pool.** Each Connector maintains multiple tunnels and can serve multiple requests in parallel.
- **A flexible pool.** Nodes can join or leave without reconfiguring the whole system.


## Quick start

### Install from the release (Linux, systemd)

Each Linux archive contains the binaries, the default configuration files, and the systemd units:

```
centaur-linux-amd64/
├── bin/centaur-gateway
├── bin/centaur-connector
├── config/gateway.json
├── config/connector.json
└── systemd/
    ├── centaur-gateway.service
    └── centaur-connector.service
```

1. Pick the archive for your CPU: `uname -m` prints `x86_64` for the `amd64` archives and `aarch64` (or `arm64`) for `arm64`.

1. Download the archive and `checksums.txt` for the same tag from [releases](https://github.com/shpak86/centaur/releases), then verify the archive:
    ```sh
    VER=v0.1.5
    curl -fLO "https://github.com/shpak86/centaur/releases/download/${VER}/centaur-linux-amd64.tar.gz"
    curl -fLO "https://github.com/shpak86/centaur/releases/download/${VER}/checksums.txt"
    grep 'linux-amd64.tar.gz' checksums.txt > centaur.sha256
    sha256sum -c centaur.sha256
    ```
    `checksums.txt` and the `systemd/` directory are published since v0.1.5; for older tags skip the checksum step and take the units from [`deployment/packaging/systemd`](deployment/packaging/systemd).

1. Unpack the archive:
    ```sh
    tar -xzf centaur-linux-amd64.tar.gz
    ```

1. Create the unprivileged system user the units run as, and the configuration directory:
    ```sh
    sudo groupadd --system centaur
    sudo useradd --system --gid centaur --no-create-home \
      --home-dir /nonexistent --shell /usr/sbin/nologin centaur
    sudo install -d -m 0750 -o root -g centaur /etc/centaur
    ```
    On Alpine use `addgroup -S centaur` and `adduser -S -D -H -h /nonexistent -s /sbin/nologin -G centaur centaur` instead.

1. Install the binaries, the configuration, and the units:
    ```sh
    sudo install -m 0755 centaur-linux-amd64/bin/* /usr/local/bin/
    sudo install -m 0640 -o root -g centaur centaur-linux-amd64/config/*.json /etc/centaur/
    sudo install -m 0644 centaur-linux-amd64/systemd/*.service /etc/systemd/system/
    ```

1. Optionally add the short aliases:
    ```sh
    sudo ln -sfn /usr/local/bin/centaur-gateway   /usr/local/bin/cntg
    sudo ln -sfn /usr/local/bin/centaur-connector /usr/local/bin/cntc
    ```

1. Configure the services. Both sides must share one key: it goes into `pool.keys` on the Gateway and into `pool.key` on the connector, otherwise registration returns `403` and the pool stays empty.

    Gateway (`sudoedit /etc/centaur/gateway.json`):
    ```json
    {
      "logger":  { "level": "info" },
      "proxy":   { "address": "0.0.0.0:18180" },
      "metrics": { "address": "0.0.0.0:18183" },
      "pool": {
        "address": "0.0.0.0:18181",
        "apiAddress": "0.0.0.0:18182",
        "keys": ["CHANGE_ME"]
      }
    }
    ```

    Connector (`sudoedit /etc/centaur/connector.json`) — the Gateway address in your network and the local Ollama:
    ```json
    {
      "logger": { "level": "info" },
      "pool": {
        "address": "gateway.lan:18181",
        "apiAddress": "http://gateway.lan:18182",
        "key": "CHANGE_ME",
        "tunnels": 4
      },
      "target": {
        "address": "http://127.0.0.1:11434",
        "models": ["llama3.2:1b"]
      }
    }
    ```

    Replace `CHANGE_ME` with the same random value in both files (`openssl rand -hex 16`) and list in `models` only the models that are actually pulled in Ollama. The files contain the pool key, hence `0640 root:centaur` and `0750` on the directory. The packaged defaults listen on loopback, so switch `proxy.address`, `pool.address`, and `pool.apiAddress` to `0.0.0.0` (as above) or to the LAN address when clients and connectors come from other hosts.

1. Enable and start the services:
    ```sh
    sudo systemctl daemon-reload
    sudo systemctl enable --now centaur-gateway      # host that serves the API
    sudo systemctl enable --now centaur-connector    # host with Ollama
    ```

The Gateway and the connector run as the unprivileged system user `centaur`; logs go to the journal (`journalctl -u centaur-gateway -f`). The Gateway only accepts requests on `proxy.address`; the connector only dials the Gateway and the local Ollama. See [`docs/systemd.md`](docs/systemd.md) for the unit reference, permissions, and operations.

To update, replace the binaries and restart the services; the configuration files are kept:

```sh
sudo systemctl stop centaur-gateway centaur-connector
sudo install -m 0755 centaur-linux-amd64/bin/* /usr/local/bin/
sudo systemctl start centaur-gateway centaur-connector
```

### Run with Docker (Linux, macOS, Windows)

The repository ships two Dockerfiles, one per role. Each machine runs exactly one container: the Gateway on the server, the Connector on the machine with Ollama, so only the image needed for that role is built. Both images are `FROM scratch` and run as a non-root user:

```sh
git clone https://github.com/shpak86/centaur.git
cd centaur

# On the Gateway machine
mkdir -p config && cp deployment/docker/config/gateway.json config/
# pool.keys here and pool.key on the Connector side must hold the same random value
docker build -f deployment/docker/gateway.Dockerfile -t centaur-gateway:local .
docker run -d --name centaur-gateway \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  -p 18180:18180 -p 18181:18181 -p 18182:18182 -p 18183:18183 \
  -v "$PWD/config:/etc/centaur:ro" \
  centaur-gateway:local

# On the machine with Ollama
cp deployment/docker/config/connector.json config/
# set the Gateway address in pool.address/pool.apiAddress,
# list in target.models only the models pulled in Ollama
docker build -f deployment/docker/connector.Dockerfile -t centaur-connector:local .
docker run -d --name centaur-connector \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  --add-host=host.docker.internal:host-gateway \
  -v "$PWD/config:/etc/centaur:ro" \
  centaur-connector:local
```

Docker Desktop on macOS and Windows runs Linux containers by default, so the same commands work there. The Gateway publishes 18180 (clients), 18181/18182 (connectors) and 18183 (metrics); the Connector publishes nothing and reaches Ollama at `host.docker.internal:11434`. Configs are mounted read-only from `./config`, so the pool key never ends up in an image.

Remote Gateway, Linux networking (`host.docker.internal`, Ollama on `0.0.0.0` instead of `--network=host`), running both roles on one host and troubleshooting: [`docs/docker.md`](docs/docker.md).

### Try it:

```sh
# Gateway health: answered by the Gateway itself, not through a tunnel
curl -s http://gateway.lan:18180/

# Ollama info/admin endpoints are not proxied by the Gateway → 501
curl -si http://gateway.lan:18180/api/tags

# Inference: the tunnel is selected by the model in the request body
curl -s -X POST http://gateway.lan:18180/api/generate \
  -d '{"model":"llama3.2:1b","prompt":"hello","stream":false}'

# Prometheus metrics (separate metrics.address port)
curl -s http://gateway.lan:18183/metrics | grep '^centaur_gateway'
```

## Configuration

See [`docs/configuration.md`](docs/configuration.md) for the full field reference, including the different meanings of `pool.*` on the Gateway and connector sides.

## Limitations

- Ollama info/admin endpoints are not available through the Gateway (`501`): the Gateway runs inference; it does not manage remote Ollama instances.
- Concurrency is limited by `pool.tunnels`: requests with no available tunnel receive `503` while the connector replenishes the pool.
- The pool detects dead tunnels when a connection closes (FIN/RST), on acquisition and during background sweeps. A silent network failure is detected only by TCP keepalive, so a request may end with `502`.

See [`docs/architecture.md`](docs/architecture.md) for details.

## Documentation

- [`docs/architecture.md`](docs/architecture.md) — system design, one-shot tunnels, handshake, tunnel pool, stale tunnel cleanup, and package map.
- [`docs/configuration.md`](docs/configuration.md) — full Gateway and connector configuration reference.
- [`docs/api.md`](docs/api.md) — model routing, response codes, and tunnel pool API.
- [`docs/metrics.md`](docs/metrics.md) — Prometheus metrics.
- [`docs/development.md`](docs/development.md) — build, run, test, and development requirements.
- [`docs/docker.md`](docs/docker.md) — build images and run the Gateway and connector in Docker.
- [`docs/systemd.md`](docs/systemd.md) — install from the release and run the services under systemd.

## License

MIT — see [`LICENSE`](LICENSE).
