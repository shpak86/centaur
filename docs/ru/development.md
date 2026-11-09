# Сборка и разработка

[English](../development.md) | Русский

## Требования

- **Go 1.27+** — точная версия указана в [`go.mod`](../../go.mod) (`go 1.27.2`).
- **Ollama** — только для ручной проверки инференса; сборка и тесты от неё не
  зависят.
- **CGO не нужен**: бинарь получается статическим.
- Сборщиков и генераторов в репозитории нет — ни `Makefile`, ни `Taskfile`, ни
  конфига линтера. Все шаги делаются `go`-командами.

Единственная прямая зависимость — `github.com/prometheus/client_golang`
(`v1.23.2`) для метрик; остальное в `go.sum` — её транзитивные зависимости. На
всё прочее хватает стандартной библиотеки: HTTP-прокси, TCP-туннели, разбор
конфига, логирование (`log/slog`).

## Сборка

```sh
go build ./...                         # проверить, что всё собирается
go build -o bin/gateway   ./cmd/gateway
go build -o bin/connector ./cmd/connector
```

Для переноса на другую машину удобна статическая сборка без путей сборки и
отладочной информации:

```sh
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/gateway ./cmd/gateway
```

## Запуск из исходников

```sh
go run ./cmd/gateway   -config examples/gateway.json
go run ./cmd/connector -config examples/connector.json
```

`-config` — путь к JSON-конфигу; по умолчанию `/etc/centaur/gateway.json` и
`/etc/centaur/connector.json`. Все поля — в
[`configuration.md`](configuration.md), запуск в контейнерах — в
[`docker.md`](docker.md).

## Тесты

```sh
go test ./...
go test -race ./...
go test -cover ./...
```

Тесты самодостаточны: поднимают слушатели на `127.0.0.1:0` (порт выбирает ОС) и
ходят друг к другу по loopback, поэтому им не нужны ни Ollama, ни внешняя сеть,
ни свободные порты `18180`–`18183` из примеров конфигов.

`-race` стоит гонять регулярно: реестр туннелей, выдача туннелей и фоновая
вычистка мёртвых работают из нескольких горутин.

## Формат и проверки

```sh
gofmt -l .     # вывода быть не должно
go vet ./...
```

## Где что лежит

Подробный разбор — в [`architecture.md`](architecture.md), кратко:

- `cmd/gateway`, `cmd/connector` — точки входа;
- `internal/tunnel` — туннели, реестр, регистрация, маршрутизация по модели;
- `internal/api` — HTTP-серверы: клиентский прокси и экспозиция метрик;
- `internal/metrics` — реестр Prometheus и метрики gateway.
