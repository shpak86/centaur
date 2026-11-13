# Centaur

[English](README.md) | Русский

Centaur — система для построения распределённой сети инференса с единым API. Она позволяет объединять в один пул обычные вычислительные машины — макбуки, рабочие станции, локальные серверы и GPU-хосты — без необходимости разворачивать отдельную production-инфраструктуру.


Система ориентирована на community-first сценарий: участники могут подключать свои машины к сети и делиться вычислительными ресурсами без публичных IP, проброса портов и сложной сетевой настройки. Каждый узел устанавливает исходящий туннель к центральному серверу. `Gateway` маршрутизирует запросы по имени модели и проксирует инференс на участников сети.

Чтобы inference-хост начал делиться ресурсами, достаточно запустить на нём Connector и указать параметры подключения к Gateway — после этого запросы пользователей пойдут на этот хост.

Чтобы пользоваться общими ресурсами, достаточно отправлять запросы на Gateway: никаких дополнительных настроек не требуется.

```
                      туннель (TCP)
client ──▶ gateway ─────────────────▶ connector ──▶ Ollama
```

## Ключевые идеи

- **Обычные машины как узлы инференса.** В сеть можно подключать доступное железо: ноутбуки, рабочие станции, локальные серверы и GPU-машины.
- **Исходящие туннели.** Узел сам подключается к Gateway, поэтому не нужен входящий доступ, публичный IP или проброс портов.
- **Маршрутизация по модели.** Gateway выбирает узел по имени модели, указанному в запросе.
- **Пул туннелей.** Каждый Connector поддерживает несколько туннелей одновременно и может обслуживать несколько запросов параллельно.
- **Состав пула может меняться.** Узлы можно добавлять и удалять без перенастройки всей системы.

## Быстрый старт

### Установка из релиза (Linux, systemd)

В каждом Linux-архиве лежат бинарники, конфиги по умолчанию и systemd-юниты:

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

1. Выберите архив под свой процессор: `uname -m` печатает `x86_64` для архивов `amd64` и `aarch64` (или `arm64`) для `arm64`.

1. Скачайте архив и `checksums.txt` одного и того же тега из [релизов](https://github.com/shpak86/centaur/releases) и проверьте архив:
    ```sh
    VER=v0.1.5
    curl -fLO "https://github.com/shpak86/centaur/releases/download/${VER}/centaur-linux-amd64.tar.gz"
    curl -fLO "https://github.com/shpak86/centaur/releases/download/${VER}/checksums.txt"
    grep 'linux-amd64.tar.gz' checksums.txt > centaur.sha256
    sha256sum -c centaur.sha256
    ```
    `checksums.txt` и каталог `systemd/` публикуются начиная с v0.1.5; в старых тегах пропустите проверку суммы, а юниты возьмите из [`deployment/packaging/systemd`](deployment/packaging/systemd).

1. Распакуйте архив:
    ```sh
    tar -xzf centaur-linux-amd64.tar.gz
    ```

1. Создайте непривилегированного системного пользователя, от которого работают юниты, и каталог конфигурации:
    ```sh
    sudo groupadd --system centaur
    sudo useradd --system --gid centaur --no-create-home \
      --home-dir /nonexistent --shell /usr/sbin/nologin centaur
    sudo install -d -m 0750 -o root -g centaur /etc/centaur
    ```
    В Alpine вместо этого: `addgroup -S centaur` и `adduser -S -D -H -h /nonexistent -s /sbin/nologin -G centaur centaur`.

1. Установите бинарники, конфигурацию и юниты:
    ```sh
    sudo install -m 0755 centaur-linux-amd64/bin/* /usr/local/bin/
    sudo install -m 0640 -o root -g centaur centaur-linux-amd64/config/*.json /etc/centaur/
    sudo install -m 0644 centaur-linux-amd64/systemd/*.service /etc/systemd/system/
    ```

1. При желании добавьте короткие алиасы:
    ```sh
    sudo ln -sfn /usr/local/bin/centaur-gateway   /usr/local/bin/cntg
    sudo ln -sfn /usr/local/bin/centaur-connector /usr/local/bin/cntc
    ```

1. Настройте сервисы. Ключ должен совпадать с двух сторон: он указывается в `pool.keys` у Gateway и в `pool.key` у коннектора, иначе регистрация вернёт `403` и пул останется пустым.

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

    Коннектор (`sudoedit /etc/centaur/connector.json`) — адрес Gateway в вашей сети и локальный Ollama:
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

    Замените `CHANGE_ME` на одно и то же случайное значение в обоих файлах (`openssl rand -hex 16`) и перечислите в `models` только те модели, которые действительно загружены в Ollama. В файлах лежит ключ пула, поэтому права `0640 root:centaur`, а на каталоге — `0750`. Конфиги из архива слушают loopback, поэтому замените `proxy.address`, `pool.address` и `pool.apiAddress` на `0.0.0.0` (как выше) или на LAN-адрес, если клиенты и коннекторы приходят с других хостов.

1. Включите и запустите сервисы:
    ```sh
    sudo systemctl daemon-reload
    sudo systemctl enable --now centaur-gateway      # хост, который отдаёт API
    sudo systemctl enable --now centaur-connector    # хост с Ollama
    ```

Gateway и коннектор работают от непривилегированного системного пользователя `centaur`; логи пишутся в journal (`journalctl -u centaur-gateway -f`). Gateway принимает запросы только на `proxy.address`, коннектор обращается только к Gateway и локальному Ollama. Юниты, права и операции описаны в [`docs/ru/systemd.md`](docs/ru/systemd.md).

Обновление — заменить бинарники и перезапустить сервисы, конфигурация при этом сохраняется:

```sh
sudo systemctl stop centaur-gateway centaur-connector
sudo install -m 0755 centaur-linux-amd64/bin/* /usr/local/bin/
sudo systemctl start centaur-gateway centaur-connector
```

### Запуск в Docker (Linux, macOS, Windows)

В репозитории два Dockerfile: gateway.Dockerfile и connector.Dockerfile. Каждая машина запускает один контейнер: Gateway на сервере, Connector на машине с Ollama, поэтому собирается только образ для нужной роли. Оба образа собираются `FROM scratch` и работают от непривилегированного пользователя:

```sh
git clone https://github.com/shpak86/centaur.git
cd centaur

# на машине с Gateway
mkdir -p config && cp deployment/docker/config/gateway.json config/
# pool.keys здесь и pool.key на стороне Connector должны содержать одно и то же случайное значение
docker build -f deployment/docker/gateway.Dockerfile -t centaur-gateway:local .
docker run -d --name centaur-gateway \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  -p 18180:18180 -p 18181:18181 -p 18182:18182 -p 18183:18183 \
  -v "$PWD/config:/etc/centaur:ro" \
  centaur-gateway:local

# на машине с Ollama
cp deployment/docker/config/connector.json config/
# в pool.address/pool.apiAddress — адрес Gateway,
# в target.models — только модели, скачанные в Ollama
docker build -f deployment/docker/connector.Dockerfile -t centaur-connector:local .
docker run -d --name centaur-connector \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  --add-host=host.docker.internal:host-gateway \
  -v "$PWD/config:/etc/centaur:ro" \
  centaur-connector:local
```

Docker Desktop на macOS и Windows по умолчанию запускает Linux-контейнеры, поэтому те же команды работают и там. Gateway публикует 18180 (клиенты), 18181/18182 (коннекторы) и 18183 (метрики); коннектор не публикует ничего и ходит к Ollama через `host.docker.internal:11434`. Конфиги монтируются только для чтения из `./config`, так что ключ пула не попадает в образ.

Удалённый Gateway, особенности сети в Linux (`host.docker.internal`, Ollama на `0.0.0.0` вместо `--network=host`), запуск обеих ролей на одной машине и диагностика: [`docs/ru/docker.md`](docs/ru/docker.md).

### Проверка:

```sh
# health gateway: отвечает сам gateway, не через туннель
curl -s http://gateway.lan:18180/

# инфо/админ-ручки Ollama gateway не проксирует → 501
curl -si http://gateway.lan:18180/api/tags

# инференс: туннель выбирается по модели из тела запроса
curl -s -X POST http://gateway.lan:18180/api/generate \
  -d '{"model":"llama3.2:1b","prompt":"hello","stream":false}'

# метрики Prometheus (отдельный порт metrics.address)
curl -s http://gateway.lan:18183/metrics | grep '^centaur_gateway'
```

## Конфигурация

Полный справочник полей — [`docs/ru/configuration.md`](docs/ru/configuration.md): там же
подробно описано, чем `pool.*` на стороне gateway отличается от `pool.*` у
коннектора.

## Ограничения

- Инфо/админ-ручки Ollama через gateway недоступны (`501`): gateway выполняет
  инференс, а не управляет удалённой Ollama.
- Число туннелей ограничено `pool.tunnels`: запросы сверх числа свободных туннелей
  получают `503`, пока коннектор поднимает новые.
- Мёртвые туннели пул находит по закрытию соединения (FIN/RST) — при выдаче
  туннеля и фоновым обходом. Обрыв без FIN/RST виден только по TCP-keepalive,
  поэтому такой запрос может закончиться `502`.

Подробности в [`docs/ru/architecture.md`](docs/ru/architecture.md).

## Документация

- [`docs/ru/architecture.md`](docs/ru/architecture.md) — как всё устроено: одноразовые
  туннели, handshake, пул, вычистка мёртвых туннелей, карта пакетов.
- [`docs/ru/configuration.md`](docs/ru/configuration.md) — полный справочник конфигов
  gateway и коннектора.
- [`docs/ru/api.md`](docs/ru/api.md) — маршрутизация по модели, коды ответов, API пула
  туннелей.
- [`docs/ru/metrics.md`](docs/ru/metrics.md) — метрики Prometheus.
- [`docs/ru/development.md`](docs/ru/development.md) — сборка, запуск, тесты и
  требования.
- [`docs/ru/docker.md`](docs/ru/docker.md) — сборка образов и запуск gateway/коннектора
  в Docker.
- [`docs/ru/systemd.md`](docs/ru/systemd.md) — установка из релиза и работа
  сервисов под systemd.

## Лицензия

MIT — см. [`LICENSE`](LICENSE).
