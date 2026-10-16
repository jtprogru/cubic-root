# cubic-root

[![Go](https://github.com/jtprogru/cubic-root/actions/workflows/build-go.yaml/badge.svg)](https://github.com/jtprogru/cubic-root/actions/workflows/build-go.yaml)
[![Publish Docker image](https://github.com/jtprogru/cubic-root/actions/workflows/build-docker.yaml/badge.svg)](https://github.com/jtprogru/cubic-root/actions/workflows/build-docker.yaml)

Приложение по мотивам данной задачи: https://jtprog.ru/interview-task-0003/

Приложение докеризовано с использованием multi stage build для Go приложений.

Имеется Helm Chart для установки приложения в Kubernetes.

## Usage

Приложение по умолчанию слушает на порту 8080.

### Основные команды для разработки

Все команды можно посмотреть запустив `make` (или `make help`) и получив такой вывод:

```log
  help             Show available targets
  deps-install     Download Go module dependencies
  tidy             Sync go.mod and go.sum with the code
  deps-update      Update all Go dependencies to the latest minor/patch versions
  build-bin        Build binary
  build-docker     Build docker image
  push-docker      Push docker image
  tests            Run all tests
  test-unit        Run unit tests
  test-bench       Run benchmarks
  run-bin          Run binary
  test-load        Run load test using k6 (stages: ramp-up 30s → load 240s → ramp-down 30s)
  test-load-smoke  Smoke test: 10 VU, 10s — быстрая проверка что сервис отвечает
  run-tempo        Start Tempo for local tracing (OTLP HTTP :4318, API :3200)
  run-traced       Run binary with tracing enabled (requires Tempo: make run-tempo)
  obs-up           Start full observability stack (Tempo + Loki + Promtail + Prometheus + Grafana)
  obs-down         Stop observability stack
```

#### build-bin

Собирает локально готовый бинарный файл и складывает его по пути `bin/cubic-root`:

```shell
make build-bin

go build -o bin/cubic-root main.go
```

## Testing

Для тестирования приложения воспользуйтесь командой `make tests`, она запустит сначала `make test-unit` для юниттестов, а потом `make test-bench` для запуска бенчмарков. Пример вывода:

```shell
Running unit tests...
go test -v -timeout 30s ./...
=== RUN   TestCubicRootHandler_ValidRequest
--- PASS: TestCubicRootHandler_ValidRequest (0.00s)
=== RUN   TestCubicRootHandler_InvalidParameter
--- PASS: TestCubicRootHandler_InvalidParameter (0.00s)
=== RUN   TestCubicRootHandler_Zero
--- PASS: TestCubicRootHandler_Zero (0.00s)
=== RUN   TestCubicRootHandler_NegativeNumber
--- PASS: TestCubicRootHandler_NegativeNumber (0.00s)
=== RUN   TestParseQueryParamsToStruct
--- PASS: TestParseQueryParamsToStruct (0.00s)
PASS
ok  	github.com/jtprogru/cubic-root	(cached)
Running benchmarks...
go test -bench=. -run='^$' -v
goos: darwin
goarch: arm64
pkg: github.com/jtprogru/cubic-root
cpu: Apple M1 Max
BenchmarkCubicRoot
BenchmarkCubicRoot-10           	 3147228	       413.0 ns/op
BenchmarkCubicRootHandler
BenchmarkCubicRootHandler-10    	  491019	      2303 ns/op
PASS
ok  	github.com/jtprogru/cubic-root	4.088s
```

Для тестирования производительности можно использовать утилиту `wrk`. Пример запуска:

```shell
wrk -c500 -d300s -t16 'http://127.0.0.1:8080/cubic-root?d=7077888000'
```

Где:

- `-c500` - количество подключений
- `-d300s` - длительность теста в секундах
- `-t16` - количество потоков

Пример вывода:

```
Running 5m test @ http://127.0.0.1:8080/cubic-root?d=7077888000
  16 threads and 500 connections
  Thread Stats   Avg      Stdev     Max   +/- Stdev
    Latency     4.30ms    3.90ms  93.16ms   55.51%
    Req/Sec     3.49k     1.80k    9.17k    62.92%
  16646603 requests in 5.00m, 2.19GB read
  Socket errors: connect 260, read 104, write 0, timeout 0
Requests/sec:  55470.35
Transfer/sec:      7.46MB
```

## Лицензия

[WTFPL](LICENSE)
