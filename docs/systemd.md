# Running Centaur with systemd

[English](systemd.md) | [Русский](ru/systemd.md)

The Linux archives ship both services as systemd system units. Each service runs
as the unprivileged system user `centaur`, logs to the journal, and needs no
writable paths on disk.

## Installation

An unpacked archive has this layout:

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

Install the files into the standard paths the units expect:

```sh
# service user
sudo groupadd --system centaur
sudo useradd --system --gid centaur --no-create-home \
  --home-dir /nonexistent --shell /usr/sbin/nologin centaur

# files
sudo install -d -m 0750 -o root -g centaur /etc/centaur
sudo install -m 0755 centaur-linux-amd64/bin/* /usr/local/bin/
sudo install -m 0640 -o root -g centaur centaur-linux-amd64/config/*.json /etc/centaur/
sudo install -m 0644 centaur-linux-amd64/systemd/*.service /etc/systemd/system/
sudo ln -sfn /usr/local/bin/centaur-gateway   /usr/local/bin/cntg
sudo ln -sfn /usr/local/bin/centaur-connector /usr/local/bin/cntc

# configuration, then start
sudoedit /etc/centaur/gateway.json     # or connector.json
sudo systemctl daemon-reload
sudo systemctl enable --now centaur-gateway
```

The download-and-verify sequence, including the checksum check, is in the
[README](../README.md#install-from-the-release-linux-systemd).

## Files and permissions

| Path | Mode | Owner |
| --- | --- | --- |
| `/usr/local/bin/centaur-gateway` | `0755` | `root:root` |
| `/usr/local/bin/centaur-connector` | `0755` | `root:root` |
| `/usr/local/bin/cntg` | symlink to `centaur-gateway` | `root:root` |
| `/usr/local/bin/cntc` | symlink to `centaur-connector` | `root:root` |
| `/etc/centaur` | `0750` | `root:centaur` |
| `/etc/centaur/gateway.json` | `0640` | `root:centaur` |
| `/etc/centaur/connector.json` | `0640` | `root:centaur` |
| `/etc/systemd/system/centaur-gateway.service` | `0644` | `root:root` |
| `/etc/systemd/system/centaur-connector.service` | `0644` | `root:root` |

Configurations contain pool keys, hence `0640 root:centaur`: only the service
and root can read them. The units themselves are tracked in the repository under
[`deployment/packaging/systemd`](../deployment/packaging/systemd) and are bundled into the Linux
archives.

## Services

### centaur-gateway.service

- `ExecStart=/usr/local/bin/centaur-gateway -config /etc/centaur/gateway.json`
- starts after `network-online.target`;
- `Restart=on-failure`, `RestartSec=2s`;
- listens on the addresses from the config: `proxy.address`, `pool.address`,
  `pool.apiAddress`, `metrics.address` (loopback by default);
- stops with `SIGTERM`; the Gateway gets up to 10s to finish in-flight
  requests, `TimeoutStopSec=15s`.

### centaur-connector.service

- `ExecStart=/usr/local/bin/centaur-connector -config /etc/centaur/connector.json`
- starts after `network-online.target`;
- `Restart=always`, `RestartSec=5s`: `Serve()` never returns on its own, so any
  exit is worth a restart; invalid configuration still trips the systemd start
  limit and prevents a restart loop;
- only opens outbound connections: TCP tunnels to `pool.address` and HTTP
  requests to `pool.apiAddress` and `target.address`. No inbound ports are
  needed.

Both units are hardened for a process that only speaks the network and writes
to the journal:

```
NoNewPrivileges=yes
CapabilityBoundingSet=
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
RestrictAddressFamilies=AF_INET AF_INET6        # + AF_UNIX for the Gateway
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM
UMask=0027
LimitNOFILE=65536
```

Because of `ProtectHome=yes` and `ProtectSystem=strict`, a binary installed
outside `/usr`, `/etc` or `/boot` (for example under `/home` or `/root`) is not
visible inside the unit. That is why the steps above use `/usr/local/bin`. If the
binaries live elsewhere, point `ExecStart` at that path and check that the
directory is visible to the service.

## Configuration

The Gateway needs at least `pool.keys`; the connector has to use one of those
keys and know where the Gateway and Ollama live:

```json
{
  "pool": {
    "address": "gateway.lan:18181",
    "apiAddress": "http://gateway.lan:18182",
    "key": "connector-key",
    "tunnels": 2
  },
  "target": {
    "address": "http://127.0.0.1:11434",
    "models": ["llama3.2:1b"]
  }
}
```

The placeholder key `connector-key` works only if the Gateway has the same key
in `pool.keys`. Otherwise the connector logs an authorization error and the pool
stays empty. See [`configuration.md`](configuration.md) for every field.

## Day-to-day operations

```sh
systemctl status centaur-gateway
journalctl -u centaur-gateway -f
sudo systemctl restart centaur-gateway
sudo systemctl stop centaur-gateway

# the aliases read /etc/centaur/*.json by default
cntg -h
cntc -h
```

Logs are JSON lines; `journalctl -u <unit> -o cat` shows them without the
journald prefix.

## Updating

Unpack the new archive and replace the binaries and the units; the configuration
in `/etc/centaur` is kept:

```sh
sudo systemctl stop centaur-gateway centaur-connector
sudo install -m 0755 centaur-linux-amd64/bin/* /usr/local/bin/
sudo install -m 0644 centaur-linux-amd64/systemd/*.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl start centaur-gateway centaur-connector
```

Compare `config/*.json` from the archive with the installed files and merge new
fields by hand: the installed configuration holds the pool keys, so it is never
replaced automatically.

## Uninstalling

```sh
sudo systemctl disable --now centaur-gateway centaur-connector
sudo rm -f /etc/systemd/system/centaur-gateway.service \
           /etc/systemd/system/centaur-connector.service
sudo systemctl daemon-reload
sudo rm -f /usr/local/bin/centaur-gateway /usr/local/bin/centaur-connector \
           /usr/local/bin/cntg /usr/local/bin/cntc
sudo rm -rf /etc/centaur
sudo userdel centaur && sudo groupdel centaur
```

## Troubleshooting

| Symptom | Cause |
| --- | --- |
| `status=203/EXEC` | `ExecStart` points to a missing binary; binaries outside `/usr` require editing the unit |
| `status=226/NAMESPACE` | the binary lives in `/home` or `/root`, which `ProtectHome=yes` hides |
| `status=1` right after start | configuration is missing or invalid, check `journalctl -u <unit>` |
| connector logs `403` | `pool.key` is not listed in the Gateway `pool.keys` |
| requests answer `503` | no tunnel in the pool: connector is down or `pool.tunnels` is too small |
| requests answer `502` | the tunnel died mid-request (network failure without FIN/RST) |

