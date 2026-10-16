# cubic-root

[![Go](https://github.com/jtprogru/cubic-root/actions/workflows/build-go.yaml/badge.svg)](https://github.com/jtprogru/cubic-root/actions/workflows/build-go.yaml)
[![Publish Docker image](https://github.com/jtprogru/cubic-root/actions/workflows/build-docker.yaml/badge.svg)](https://github.com/jtprogru/cubic-root/actions/workflows/build-docker.yaml)

Приложение по мотивам данной задачи: https://jtprog.ru/interview-task-0003/

Приложение докеризовано с использованием multi stage build для Go приложений.

Имеется Helm Chart для установки приложения в Kubernetes.

## Usage

Приложение по умолчанию слушает на порту 8080.

### Конфигурация

Все настройки задаются переменными окружения. Невалидное значение не даёт процессу стартовать, ошибки по всем переменным выводятся разом.

| переменная | по умолчанию | что задаёт |
|---|---|---|
| `PORT` | `8080` | порт публичного API |
| `METRICS_PORT` | `9090` | внутренний порт `/metrics` |
| `DEBUG` | `false` | debug-логи, в том числе по каждому запросу |
| `CACHE_SIZE` | `10000` | размер кэша результатов; `0` выключает кэш |
| `CACHE_TTL` | `5m` | время жизни записи кэша; `0` — без устаревания |
| `CALC_DELAY` | `0` | искусственная задержка вычисления на промахе кэша, для экспериментов |
| `CALC_DELAY_JITTER` | `0` | равномерный разброс `±` вокруг `CALC_DELAY` |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` и другие `OTEL_*` | — | экспорт трейсов, стандартные переменные OpenTelemetry SDK |

### Кэш

Результаты хранятся в in-memory LRU-кэше с TTL (`internal/cache`). Ключ — значение `d` после парсинга, поэтому `27`, `27.0` и `2.7e1` попадают в одну запись. Одновременные промахи по одному ключу ждут одно вычисление (singleflight). Результаты, которым нельзя доверять (`max_iter`, `degenerate`), в кэш не кладутся. Откуда взят ответ, видно по заголовку `X-Cache: HIT|MISS`.

Само вычисление стоит около 230ns, поэтому без `CALC_DELAY` кэш почти не влияет на латентность, зато заметно сокращает работу. Прогон `make test-load SCENARIO=hot-keys` (50 VU, 60s, ~2000 RPS) на Apple M4 Max:

| конфигурация | hit ratio | вычислений/с | p50 сервера | p99 сервера |
|---|---|---|---|---|
| кэш, без задержки | 79.1% | 407 | 27µs | 165µs |
| без кэша, без задержки | — | 1 930 | 27µs | 229µs |
| кэш, `CALC_DELAY=2ms ±1ms` | 79.1% | 385 | 34µs | 4.8ms |
| без кэша, `CALC_DELAY=2ms ±1ms` | — | 1 740 | 2.65ms | 5.0ms |

С задержкой кэш снимает медиану с 2.65ms до 34µs, но p99 почти не меняется: в хвост попадают промахи. Сравнить это на графиках можно в ряду Cache дашборда `cubic-root`.

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
  test-load        Run k6 load test, SCENARIO=baseline|wide-range|bad-requests|hot-keys|spike (see k6/load.js)
  test-load-smoke  Smoke test: 10 VU, 10s — быстрая проверка что сервис отвечает
  run-traces       Start VictoriaTraces for local tracing (OTLP HTTP and UI on :10428)
  run-traced       Run binary with tracing enabled (requires VictoriaTraces: make run-traces)
  obs-up           Start full observability stack (VictoriaMetrics + vmagent + vmalert + VictoriaLogs + Vector + VictoriaTraces + Grafana)
  obs-down         Stop observability stack
```

#### build-bin

Собирает локально готовый бинарный файл и складывает его по пути `bin/cubic-root`:

```shell
make build-bin

go build -o bin/cubic-root main.go
```

## Метрики

Приложение отдаёт метрики на внутреннем порту `METRICS_PORT` (по умолчанию `9090`) по пути `/metrics`. Публичный порт их не отдаёт.

HTTP-слой (golden signals) снимает middleware со всех запросов, включая отклонённые роутером. Имена по OTel semantic conventions:

| метрика | тип | labels | что показывает |
|---|---|---|---|
| `http_server_request_duration_seconds` | histogram | `http_request_method`, `http_route`, `http_response_status_code` | латентность; `_count` даёт трафик и ошибки |
| `http_server_active_requests` | gauge | `http_request_method` | запросы в обработке (saturation) |
| `http_server_response_body_size_bytes` | histogram | `http_route` | размер ответов |

`http_route` — паттерн маршрута без метода (`/cubic-root`, `/health`) или `unmatched` для 404 и 405. Нестандартные HTTP-методы записываются как `_OTHER`. Так ни путь, ни метод из запроса не могут породить новые серии.

Доменные метрики описывают работу сервиса, которую HTTP-слой не видит:

| метрика | тип | labels | что показывает |
|---|---|---|---|
| `cubic_root_calculations_total` | counter | `outcome` = `converged` / `max_iter` / `degenerate` / `trivial` | выполненные вычисления и чем они закончились; `max_iter` и `degenerate` — ответы, которым нельзя доверять, хотя HTTP отдаёт 200 |
| `cubic_root_newton_iterations` | histogram | — | число шагов метода Ньютона на одно вычисление: стоимость работы |
| `cubic_root_result_relative_error` | histogram | — | относительная ошибка `\|z³−d\|/\|d\|`, correctness SLI по бакету `le="1e-12"`; ошибка от 100% записывается как 1 |
| `cubic_root_input_magnitude_log10` | histogram | — | порядок величины `log10\|d\|`: профиль нагрузки |
| `cubic_root_validation_errors_total` | counter | `reason` = `missing` / `not_a_number` / `non_finite` | из чего состоят 400-е ответы |

Метрики кэша:

| метрика | тип | labels | что показывает |
|---|---|---|---|
| `cubic_root_cache_requests_total` | counter | `result` = `hit` / `miss` | hit ratio |
| `cubic_root_cache_evictions_total` | counter | `reason` = `capacity` / `expired` | `capacity` — кэш мал, `expired` — TTL короткий |
| `cubic_root_cache_entries` | gauge | — | текущий размер, считывается при scrape |
| `cubic_root_cache_capacity_entries` | gauge | — | лимит размера, `0` при выключенном кэше |
| `cubic_root_cache_hit_age_seconds` | histogram | — | возраст записи в момент попадания, для подбора `CACHE_TTL` |
| `cubic_root_cache_coalesced_total` | counter | — | промахи, дождавшиеся чужого вычисления |

Рантайм и сборка: стандартные `go_*` и `process_*`, а также `go_sched_latencies_seconds` (сколько горутина ждёт CPU), `go_sync_mutex_wait_total_seconds_total` (ожидание на мьютексах), `go_cpu_classes_*` (разбивка CPU, в том числе доля GC) и `cubic_root_build_info{version, revision}`.

## Observability

Локальный стек наблюдаемости собран на VictoriaMetrics и поднимается командой `make obs-up` (останавливается `make obs-down`):

| компонент | роль | адрес |
|---|---|---|
| VictoriaMetrics | хранение метрик | http://localhost:8428/vmui |
| vmagent | сбор метрик приложения и самого стека (`observability/vmagent/scrape.yml`) | http://localhost:8429/targets |
| vmalert | recording rules и алерты (`observability/vmalert/rules/`) | http://localhost:8880 |
| VictoriaLogs | хранение логов | http://localhost:9428/select/vmui |
| Vector | доставка логов контейнера `cubic-root` в VictoriaLogs (`observability/vector/vector.yaml`) | — |
| VictoriaTraces | хранение трейсов, приём OTLP | http://localhost:10428/select/vmui |
| Grafana | дашборды, переходы между логами и трейсами | http://localhost:3333 |

Приложение отдаёт метрики на внутреннем порту `9090`, пишет JSON-логи в stdout и отправляет трейсы по OTLP HTTP в VictoriaTraces. Доля сэмплируемых трейсов задаётся переменной `TRACES_SAMPLE_RATIO` (по умолчанию `1.0`), для нагрузочных прогонов её стоит снизить: `TRACES_SAMPLE_RATIO=0.01 make obs-up`.

Дашборд приложения лежит в `observability/grafana/dashboards/cubic-root.json`, официальные дашборды компонентов стека — в папке `victoria-stack` (`observability/grafana/dashboards/victoria-stack/`).

В Grafana трейсы доступны через два datasource: `VictoriaTraces` (Jaeger API, основной) и `VictoriaTraces (Tempo API)` (экспериментальный, для TraceQL и Traces Drilldown). Из лога в VictoriaLogs можно перейти в трейс по полю `trace_id`, из спана — в логи этого трейса.

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

Нагрузочные тесты на k6 запускаются через `make test-load`, профиль выбирается переменной `SCENARIO`:

| сценарий | нагрузка | что подсвечивает |
|---|---|---|
| `baseline` (по умолчанию) | разгон до 3500 VU, `d` равномерно в [1e6, 1e9] | latency и saturation под нагрузкой |
| `wide-range` | 50 VU, `\|d\|` log-uniform в [1e-20, 1e40] с обоими знаками | точность и сходимость алгоритма: k6 сверяет ответ с `Math.cbrt` |
| `bad-requests` | 50 VU, четверть запросов невалидные или POST | 4xx/405 в HTTP-метриках и причины в `cubic_root_validation_errors_total` |
| `hot-keys` | 50 VU, 80% запросов по 1000 «горячим» значениям | hit ratio кэша; k6 сверяет его по заголовку `X-Cache` |
| `spike` | скачок с 50 до 3000 VU и обратно | saturation и восстановление |

Например: `make test-load SCENARIO=wide-range`. Для длинных прогонов стоит снизить долю трейсов: `TRACES_SAMPLE_RATIO=0.01 make obs-up`.

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
