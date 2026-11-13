# Запуск в Docker

[English](../docker.md) | Русский

В репозитории два Dockerfile в [`deployment/docker`](../../deployment/docker): `gateway.Dockerfile` и `connector.Dockerfile`. На каждой машине запускается один контейнер — Gateway на сервере, коннектор рядом с Ollama, — поэтому собирается только нужный образ; образы независимы и не обязаны работать на одном хосте. Оба бинарника статические, ходят только исходящими соединениями, пишут JSON в stdout и корректно завершаются по `SIGTERM`, поэтому образы собираются `FROM scratch`, работают от непривилегированного пользователя и требуют только смонтированный конфиг.

Сценарий одинаков на Linux, macOS и Windows: клонируем репозиторий, собираем образ, запускаем со своим конфигом.

## Требования

- Docker Engine или Docker Desktop с Compose v2. Нужен BuildKit (сборщик по умолчанию начиная с Docker 23): в Dockerfile используются `RUN --mount=type=cache` и автоматические аргументы `TARGETOS`/`TARGETARCH`.
- На Windows — Linux-контейнеры: Docker Desktop использует их по умолчанию через WSL2. Windows-контейнеры не поддерживаются.
- `git` и собственный файл конфигурации.

## Сборка

```sh
git clone https://github.com/shpak86/centaur.git
cd centaur
docker build -f deployment/docker/gateway.Dockerfile   -t centaur-gateway:local   .
docker build -f deployment/docker/connector.Dockerfile -t centaur-connector:local .
```

Контекст сборки — корень репозитория, а `-f` указывает на Dockerfile: `docker build deployment/docker/` упадёт с `failed to read dockerfile`, а сборка из каталога `deployment/docker/` — с ошибкой про `go.mod`.

Оба образа маленькие (`FROM scratch`: бинарник и набор корневых сертификатов) и работают от UID/GID 65532:

```sh
docker images | grep centaur
docker image inspect -f '{{.Config.User}} {{.Config.Entrypoint}}' centaur-gateway:local
```

Стадия сборки прибита к версии Go из `go.mod` (`golang:1.27.2`), поэтому тулчейн не докачивается внутри контейнера. Зависимости кэшируются в mount'ах BuildKit, так что пересборка после правки кода занимает секунды. В образах есть OCI-метки; свои значения передаются через `--build-arg VERSION=... --build-arg REVISION=...`.

### Другие платформы

`GOOS`/`GOARCH` берутся из `TARGETOS`/`TARGETARCH` BuildKit, Go кросс-компилирует сам — эмуляция не нужна:

```sh
# образ для amd64-сервера, собранный на Apple Silicon или ARM
docker buildx build --platform linux/amd64 -f deployment/docker/gateway.Dockerfile \
  -t centaur-gateway:local --load .

# обе архитектуры: нужен registry, куда пушить
docker buildx build --platform linux/amd64,linux/arm64 -f deployment/docker/gateway.Dockerfile \
  -t registry/centaur-gateway:v0.1.5 --push .
```

Обычный `docker build` собирает образ под архитектуру хоста: `arm64` на Apple Silicon, `amd64` на x86.

## Конфигурация

Оба бинарника принимают путь к конфигу флагом `-config`, по умолчанию `/etc/centaur/gateway.json` и `/etc/centaur/connector.json`. В образах конфига намеренно нет: без смонтированного файла процесс завершается с `read config ...: no such file or directory`, а не начинает молча слушать loopback.

Отправная точка — контейнерные шаблоны в [`deployment/docker/config`](../../deployment/docker/config):

```sh
mkdir -p config
cp deployment/docker/config/gateway.json   config/gateway.json
cp deployment/docker/config/connector.json config/connector.json
openssl rand -hex 16   # одно и то же значение в оба файла
```

Отличия от хостовых шаблонов в [`deployment/packaging/config`](../../deployment/packaging/config) два:

- Gateway **слушает `0.0.0.0`** (`proxy.address`, `pool.address`, `pool.apiAddress`, `metrics.address`). Адрес `127.0.0.1` внутри контейнера недостижим через `-p`, потому что опубликованный порт ведёт на внешний интерфейс контейнера;
- коннектор **ходит к Gateway по адресу из своего конфига** (`gateway:18181` в шаблоне — замените на реальный хост), а к Ollama — через `host.docker.internal`. На Docker Desktop это работает из коробки, на Linux нужен `--add-host`.

Ключ пула должен совпадать: Gateway принимает любое значение из `pool.keys`, коннектор отправляет свой `pool.key`. При расхождении коннектор повторяет `register tunnel: unexpected status "403 Forbidden"`, пул остаётся без туннелей, а клиенты получают `503`.

`config/gateway.json`:

```json
{
  "logger":  { "level": "info" },
  "proxy":   { "address": "0.0.0.0:18180" },
  "metrics": { "address": "0.0.0.0:18183" },
  "pool": {
    "address": "0.0.0.0:18181",
    "apiAddress": "0.0.0.0:18182",
    "keys": ["<ключ-пула>"]
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
    "key": "<ключ-пула>",
    "tunnels": 4
  },
  "target": {
    "address": "http://host.docker.internal:11434",
    "models": ["llama3.2:1b"]
  }
}
```

В шаблоне `gateway` — заглушка: `pool.address` — это `host:port` Gateway, а `pool.apiAddress` — URL того же хоста, поэтому оба значения нужно заменить на адрес, по которому Gateway доступен с машины коннектора.

В `target.models` перечисляем только те модели, которые реально скачаны в Ollama: Gateway маршрутизирует по имени модели в теле запроса и отвечает `503`, если ни один туннель такую модель не объявил.

Конфиг читается один раз при старте, поэтому монтируем его только для чтения и после правок пересоздаём контейнер. Каталог `config/` в `.gitignore` — в нём лежит ключ пула.

Если конфиг должен лежать в другом месте, монтируем его по произвольному пути и передаём `-config`:

```sh
docker run --rm \
  -v "$PWD/my/gateway.json:/cfg/gateway.json:ro" \
  centaur-gateway:local -config /cfg/gateway.json
```

На Linux с SELinux добавьте `:z` к тому (`...:/cfg/gateway.json:ro,z`).

## Запуск Gateway

```sh
docker run -d --name centaur-gateway \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  -p 18180:18180 -p 18181:18181 -p 18182:18182 -p 18183:18183 \
  -v "$PWD/config:/etc/centaur:ro" \
  centaur-gateway:local
```

Образ уже работает от UID 65532, а бинарник по умолчанию читает `/etc/centaur/gateway.json`, поэтому аргументы не нужны. В Windows (PowerShell) том записывается как `-v "${PWD}/config:/etc/centaur:ro"`.

| Порт | Поле конфига | Назначение |
| --- | --- | --- |
| 18180 | `proxy.address` | клиентский API: запросы инференса и `GET /` для проверки живости |
| 18181 | `pool.address` | туннели коннекторов |
| 18182 | `pool.apiAddress` | регистрация туннелей от коннекторов |
| 18183 | `metrics.address` | метрики Prometheus, для трафика не нужен |

`18181` и `18182` должны быть доступны с каждого хоста, где запущен коннектор; единственная аутентификация — ключ пула. `18180` при публичном Gateway ставим за reverse proxy с TLS, `18183` публикуем только там, где его скрейпит Prometheus.

Состояния у Gateway нет: перезапуск или пересоздание контейнера теряет только живые туннели, коннекторы регистрируются заново сами.

## Запуск коннектора

Коннектор открывает только исходящие соединения — к пулу Gateway и к `target.address` — поэтому портов не публикует и входящий доступ ему не нужен. Набор действий зависит от того, где находятся Ollama и Gateway.

### Ollama и Gateway на одном хосте (Linux)

```sh
docker run -d --name centaur-connector \
  --network=host \
  -v "$PWD/config:/etc/centaur:ro" \
  centaur-connector:local
```

С `--network=host` контейнер использует сетевой namespace хоста, поэтому работают обычные адреса: `pool.address: "127.0.0.1:18181"`, `pool.apiAddress: "http://127.0.0.1:18182"`, `target.address: "http://127.0.0.1:11434"`. Для Linux-машины это самый простой вариант, менять настройки Ollama не нужно. `--network=host` есть только на Linux.

### Ollama на хосте, bridge-сеть

```sh
docker run -d --name centaur-connector \
  --add-host=host.docker.internal:host-gateway \
  -v "$PWD/config:/etc/centaur:ro" \
  centaur-connector:local
```

- macOS и Windows: `host.docker.internal` уже указывает на хост и достаёт сервисы, слушающие loopback, так что `target.address: "http://host.docker.internal:11434"` работает как есть.
- Linux: `host.docker.internal` — это адрес `docker0`, поэтому Ollama должен слушать все интерфейсы: запускайте с `OLLAMA_HOST=0.0.0.0:11434` (или `OLLAMA_HOST=0.0.0.0` в launchd/systemd-юните). Если Ollama остаётся на `127.0.0.1:11434`, коннектор пишет `Ollama proxy error ... connection refused`, а клиенты получают `502` — используйте `--network=host`.

К Gateway коннектор ходит так же: `pool.address: "host.docker.internal:18181"`, `pool.apiAddress: "http://host.docker.internal:18182"`, если Gateway запущен на самом хосте.

### Удалённый Gateway

Ничего особенного: в конфиг пишем доступный адрес, флаги и extra hosts не нужны.

```json
"pool": {
  "address": "gateway:18181",
  "apiAddress": "http://gateway:18182"
}
```

### Ollama в Docker

Коннектор может ходить к Ollama не через хост, а по пользовательской сети: создайте её через `docker network create centaur`, запустите в ней контейнер Ollama и укажите в `target.address` имя контейнера, например `http://ollama:11434`. `host.docker.internal` тут не нужен.

| Gateway | Ollama | `pool.address` / `pool.apiAddress` | `target.address` |
| --- | --- | --- | --- |
| хост | хост | `host.docker.internal:18181` / `http://host.docker.internal:18182` | `http://host.docker.internal:11434` |
| удалённый хост | хост | `gateway:18181` / `http://gateway:18182` | `http://host.docker.internal:11434` |
| любой | контейнер | без изменений | `http://ollama:11434` |

`--add-host=host.docker.internal:host-gateway` обязателен на Linux для каждого `host.docker.internal` из таблицы и безвреден на Docker Desktop.

## Обе роли на одной машине

Обычная схема — один сервис на машину, но для быстрой проверки всей цепочки оба контейнера можно поднять на одном хосте: это по-прежнему два независимых `docker run`, ничего дополнительно устанавливать не нужно:

```sh
mkdir -p config
cp deployment/docker/config/gateway.json   config/gateway.json
cp deployment/docker/config/connector.json config/connector.json
# один и тот же случайный ключ в pool.keys (gateway.json) и pool.key (connector.json);
# в connector.json замените gateway на host.docker.internal, а
# в target.models оставьте только модели, скачанные в Ollama

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

Здесь коннектор видит Gateway через опубликованные порты, поэтому в его конфиге `host.docker.internal` используется и для `pool.*`, и для `target.address`; имя на Linux даёт `--add-host`. Удаление контейнеров конфиг не трогает:

```sh
docker rm -f centaur-gateway centaur-connector
```

## Проверка

```sh
# живость: отвечает сам Gateway, не через туннель
curl -s http://127.0.0.1:18180/

# инференс: туннель выбирается по модели в теле запроса
curl -s -X POST http://127.0.0.1:18180/api/generate \
  -d '{"model":"llama3.2:1b","prompt":"hello","stream":false}'

# info/admin-эндпоинты Ollama через Gateway не проксируются -> 501
curl -si http://127.0.0.1:18180/api/tags

# метрики Prometheus
curl -s http://127.0.0.1:18183/metrics | grep '^centaur_gateway'

# логи — JSON-строки в stdout
docker logs centaur-gateway
docker logs centaur-connector
```

При старте коннектор печатает `connector started` с `poolApiAddress`, `poolAddress` и `targetAddress` — это самый быстрый способ убедиться, что смонтирован нужный конфиг.

## Обновление

```sh
git pull
# на хосте с Gateway
docker build --pull -f deployment/docker/gateway.Dockerfile -t centaur-gateway:local .
# на каждой машине с коннектором
docker build --pull -f deployment/docker/connector.Dockerfile -t centaur-connector:local .
```

Затем пересоздайте контейнер нужной роли командой `docker run` из примеров выше (сначала `docker rm -f centaur-gateway` или `docker rm -f centaur-connector`). Конфигурация в `./config` сохраняется: после обновления сравните её с `deployment/docker/config/*.json` и перенесите новые поля вручную.

## Что внутри образов

- `FROM scratch`: один статический бинарник и `/etc/ssl/certs/ca-certificates.crt`, без shell и пакетного менеджера. Зайти в `docker exec ... sh` не получится — диагностика через `docker logs`, метрики и запуск того же бинарника на хосте.
- Непривилегированный `USER 65532:65532`, нет записываемых путей, capabilities и setuid-бинарей — поэтому `--read-only`, `--cap-drop ALL` и `no-new-privileges` (уже используются в командах выше) ничего не ломают.
- Логи идут в stdout/stderr, так что работают и `docker logs`, и journald; процесс обрабатывает `SIGTERM` и останавливается за 10 секунд — это меньше стандартного grace-периода Docker.
- Конфиг читается один раз при старте и монтируется только для чтения: чтобы применить правки, контейнер пересоздают.

## Диагностика

| Симптом | Причина |
| --- | --- |
| `failed to solve: ... go.mod: not found` или `failed to read dockerfile` | неверный контекст сборки: собирайте из корня репозитория с `-f deployment/docker/<name>.Dockerfile .` |
| `read config "/etc/centaur/gateway.json": no such file or directory` | конфиг не смонтирован или в каталоге только `connector.json`: монтируйте каталог целиком или нужный файл |
| коннектор пишет `register tunnel: unexpected status "403 Forbidden"` | `pool.key` отсутствует в `pool.keys` на Gateway |
| клиенты получают `503` | в пуле нет туннеля: коннектор не запущен, ключ неверный или запрошенной модели нет в `target.models` |
| на Linux коннектор пишет `Ollama proxy error ... connection refused` для `172.17.0.1:11434`, а клиенты получают `502 upstream error` | Ollama слушает только `127.0.0.1` — запустите с `OLLAMA_HOST=0.0.0.0:11434` или используйте `--network=host` и `127.0.0.1` в конфиге |
| `curl 127.0.0.1:18180` отвечает, но пул не наполняется | в конфиге Gateway остались адреса `127.0.0.1`: внутри контейнера нужны `0.0.0.0` |
| `permission denied` при монтировании конфига на Fedora/RHEL | SELinux: добавьте `:z` к тому (`./config:/etc/centaur:ro,z`) |
| коннектор пишет `dial tcp: lookup host.docker.internal ... no such host` | на Linux этого имени по умолчанию нет: добавьте `--add-host=host.docker.internal:host-gateway` или используйте `--network=host` и `127.0.0.1` в конфиге |
| `exec format error` сразу после старта | образ собран под другую архитектуру: пересоберите с `--platform`, совпадающим с хостом |

Образы в registry не публикуются — собирайте их локально из клона. Развёртывание без Docker — в [`systemd.md`](systemd.md) и релизных архивах в [releases](https://github.com/shpak86/centaur/releases).
