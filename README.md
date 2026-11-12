# Centaur

[English](README.md) | [Русский](README.ru.md)

Centaur builds a distributed inference network behind a single API. It combines everyday computing devices—MacBooks, workstations, local servers, and GPU hosts—into one pool, without requiring a separate production infrastructure.

The project is designed for community-first use: participants can connect their machines and share compute without public IP addresses, port forwarding, or complex network setup. Each node establishes an outbound tunnel to a central server. The `Gateway` routes requests by model name and proxies inference to network participants.

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

### Download and unpack the archive

1. Download the archive for your OS from [releases](https://github.com/shpak86/centaur/releases)
1. Unpack the archive:
    ```bash
    tar -xzf centaur-linux-amd64.tar.gz
    ```
1. Make the binaries executable:
    ```bash
    chmod +x centaur-linux-amd64/bin/*
    ```

#### Centaur Gateway

1. Move the centaur-gateway binary to /usr/local/bin:
    ```bash
    sudo cp centaur-linux-amd64/bin/centaur-gateway /usr/local/bin/centaur-gateway
    ```
1. Move the gateway.json config to /etc/centaur:
    ```bash
    sudo cp centaur-linux-amd64/config/gateway.json /etc/centaur/
    ```
1. Set the parameters in gateway.json and start the Gateway:
    ```sh
    centaur-gateway
    ```

#### Centaur Connector

1. Move the centaur-connector binary to ~/.centaur/bin:
    ```bash
    cp centaur-linux-amd64/bin/centaur-connector ~/.centaur/bin/centaur-connector
    ```
1. Move the connector.json config to ~/.centaur/config:
    ```bash
    cp centaur-linux-amd64/config/connector.json ~/.centaur/config/
    ```
1. Set the Gateway and Ollama addresses and a valid key in connector.json, then start the Connector:
    ```sh
    ~/.centaur/bin/centaur-connector -config ~/.centaur/config/connector.json
    ```

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

## License

MIT — see [`LICENSE`](LICENSE).
