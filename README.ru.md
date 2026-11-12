# Centaur

[English](README.md) | Русский

Centaur — система для построения распределённой сети инференса с единым API. Она позволяет объединять в один пул обычные вычислительные машины — макбуки, рабочие станции, локальные серверы и GPU-хосты — без необходимости разворачивать отдельную production-инфраструктуру.


Система ориентирована на community-first сценарий: участники могут подключать свои машины к сети и делиться вычислительными ресурсами без публичных IP, проброса портов и сложной сетевой настройки. Каждый узел устанавливает исходящий туннель к центральному серверу. `Gateway` маршрутизирует запросы по имени модели и проксирует инференс на участников сети.

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

### Скачайте и распакуйте архив

1. Скачайте подходящий архив для вашей ОС из [релизов](https://github.com/shpak86/centaur/releases)
1. Распакуйте архив:
    ```bash
    tar -xzf centaur-linux-amd64.tar.gz
    ```
1. Сделайте бинарники исполняемыми:
    ```bash
    chmod +x centaur-linux-amd64/bin/*
    ```

#### Centaur Gateway

1. Переместите бинарник centaur-gateway в /usr/local/bin:
    ```bash
    sudo cp centaur-linux-amd64/bin/centaur-gateway /usr/local/bin/centaur-gateway
    ```
1. Переместите конфиг gateway.json в /etc/centaur:
    ```bash
    sudo cp centaur-linux-amd64/config/gateway.json /etc/centaur/
    ```
1. Укажите параметры в gateway.json и запустите Gateway:
    ```sh
    centaur-gateway
    ```

#### Centaur Connector

1. Переместите бинарник centaur-connector в ~/.centaur/bin:
    ```bash
    cp centaur-linux-amd64/bin/centaur-connector ~/.centaur/bin/centaur-connector
    ```
1. Переместите конфиг connector.json в ~/.centaur/config:
    ```bash
    cp centaur-linux-amd64/config/connector.json ~/.centaur/config/
    ```
1. Укажите в connector.json адрес Gateway и Ollama, корректный ключ и запустите Connector:
    ```sh
    ~/.centaur/bin/centaur-connector -config ~/.centaur/config/connector.json
    ```

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

## Лицензия

MIT — см. [`LICENSE`](LICENSE).
