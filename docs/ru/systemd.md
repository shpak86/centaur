# Centaur под systemd

[English](../systemd.md) | Русский

Linux-архивы содержат оба сервиса как системные юниты systemd. Каждый сервис
работает от непривилегированного системного пользователя `centaur`, пишет логи
в journal и не требует записываемых каталогов на диске.

## Установка

Распакованный архив выглядит так:

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

Раскладываем файлы по стандартным путям, которых ждут юниты:

```sh
# системный пользователь
sudo groupadd --system centaur
sudo useradd --system --gid centaur --no-create-home \
  --home-dir /nonexistent --shell /usr/sbin/nologin centaur

# файлы
sudo install -d -m 0750 -o root -g centaur /etc/centaur
sudo install -m 0755 centaur-linux-amd64/bin/* /usr/local/bin/
sudo install -m 0640 -o root -g centaur centaur-linux-amd64/config/*.json /etc/centaur/
sudo install -m 0644 centaur-linux-amd64/systemd/*.service /etc/systemd/system/
sudo ln -sfn /usr/local/bin/centaur-gateway   /usr/local/bin/cntg
sudo ln -sfn /usr/local/bin/centaur-connector /usr/local/bin/cntc

# конфигурация, затем запуск
sudoedit /etc/centaur/gateway.json     # или connector.json
sudo systemctl daemon-reload
sudo systemctl enable --now centaur-gateway
```

Последовательность со скачиванием и проверкой контрольной суммы — в
[README](../../README.ru.md#установка-из-релиза-linux-systemd).

## Файлы и права

| Путь | Права | Владелец |
| --- | --- | --- |
| `/usr/local/bin/centaur-gateway` | `0755` | `root:root` |
| `/usr/local/bin/centaur-connector` | `0755` | `root:root` |
| `/usr/local/bin/cntg` | симлинк на `centaur-gateway` | `root:root` |
| `/usr/local/bin/cntc` | симлинк на `centaur-connector` | `root:root` |
| `/etc/centaur` | `0750` | `root:centaur` |
| `/etc/centaur/gateway.json` | `0640` | `root:centaur` |
| `/etc/centaur/connector.json` | `0640` | `root:centaur` |
| `/etc/systemd/system/centaur-gateway.service` | `0644` | `root:root` |
| `/etc/systemd/system/centaur-connector.service` | `0644` | `root:root` |

В конфигах лежат ключи пула, поэтому `0640 root:centaur`: читать их могут
только сервис и root. Сами юниты лежат в репозитории в
[`deployment/packaging/systemd`](../../deployment/packaging/systemd) и попадают в linux-архивы.

## Сервисы

### centaur-gateway.service

- `ExecStart=/usr/local/bin/centaur-gateway -config /etc/centaur/gateway.json`;
- стартует после `network-online.target`;
- `Restart=on-failure`, `RestartSec=2s`;
- слушает адреса из конфига: `proxy.address`, `pool.address`,
  `pool.apiAddress`, `metrics.address` (по умолчанию — loopback);
- останавливается по `SIGTERM`; на завершение текущих запросов даётся до 10s,
  `TimeoutStopSec=15s`.

### centaur-connector.service

- `ExecStart=/usr/local/bin/centaur-connector -config /etc/centaur/connector.json`;
- стартует после `network-online.target`;
- `Restart=always`, `RestartSec=5s`: `Serve()` сам не завершается, поэтому любой
  выход стоит перезапустить; при битом конфиге срабатывает лимит запусков
  systemd и цикла перезапусков не будет;
- открывает только исходящие соединения: TCP-туннели к `pool.address` и
  HTTP-запросы к `pool.apiAddress` и `target.address`. Входящие порты не нужны.

Оба юнита захарднены под процесс, которому нужны только сеть и journal:

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
RestrictAddressFamilies=AF_INET AF_INET6        # gateway — ещё AF_UNIX
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

Из-за `ProtectHome=yes` и `ProtectSystem=strict` бинарник, лежащий вне `/usr`,
`/etc` и `/boot` (например, в `/home` или `/root`), внутри юнита не виден.
Именно поэтому в шагах выше используется `/usr/local/bin`. Если бинарники лежат
в другом каталоге, поправьте `ExecStart` и проверьте, что каталог виден сервису.

## Конфигурация

Gateway нужен как минимум `pool.keys`; коннектор должен использовать один из
этих ключей и знать адреса gateway и Ollama:

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

Ключ-заглушка `connector-key` работает, только если такой же ключ есть в
`pool.keys` на gateway. Иначе коннектор пишет в лог ошибку авторизации, и пул
остаётся пустым. Все поля описаны в [`configuration.md`](configuration.md).

## Эксплуатация

```sh
systemctl status centaur-gateway
journalctl -u centaur-gateway -f
sudo systemctl restart centaur-gateway
sudo systemctl stop centaur-gateway

# алиасы по умолчанию читают /etc/centaur/*.json
cntg -h
cntc -h
```

Логи — JSON-строки; `journalctl -u <unit> -o cat` показывает их без префикса
journald.

## Обновление

Распакуйте новый архив и замените бинарники и юниты; конфигурация в
`/etc/centaur` сохраняется:

```sh
sudo systemctl stop centaur-gateway centaur-connector
sudo install -m 0755 centaur-linux-amd64/bin/* /usr/local/bin/
sudo install -m 0644 centaur-linux-amd64/systemd/*.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl start centaur-gateway centaur-connector
```

Сравните `config/*.json` из архива с установленными файлами и перенесите новые
поля вручную: в установленной конфигурации лежат ключи пула, поэтому
автоматически она не перезаписывается.

## Удаление

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

## Диагностика

| Симптом | Причина |
| --- | --- |
| `status=203/EXEC` | в `ExecStart` путь к отсутствующему бинарнику; если бинарники лежат вне `/usr`, нужно править юнит |
| `status=226/NAMESPACE` | бинарник лежит в `/home` или `/root`, а `ProtectHome=yes` их скрывает |
| `status=1` сразу после старта | конфиг отсутствует или некорректен, смотрите `journalctl -u <unit>` |
| в логе коннектора `403` | `pool.key` отсутствует в `pool.keys` на gateway |
| ответы `503` | в пуле нет туннелей: коннектор лежит или мал `pool.tunnels` |
| ответы `502` | туннель умер посреди запроса (обрыв без FIN/RST) |

