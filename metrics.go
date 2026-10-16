package main

import (
	"math"
	"regexp"
	"runtime/debug"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// httpDurationBuckets — бакеты латентности HTTP от 50µs до 1s. Хендлер отвечает
// за микросекунды, поэтому со стандартными prometheus.DefBuckets (5ms…10s) все
// запросы попадали в первый бакет, и histogram_quantile возвращал интерполяцию
// внутри [0, 5ms] вместо реальной латентности.
var httpDurationBuckets = []float64{
	0.00005, 0.0001, 0.00025, 0.0005,
	0.001, 0.0025, 0.005, 0.01, 0.025, 0.05,
	0.1, 0.25, 0.5, 1,
}

// metrics — метрики приложения. Создаётся один раз в main и передаётся
// зависимостям явно.
//
// Метрики HTTP-слоя названы по OTel semantic conventions (http.server.*) в
// нотации Prometheus, чтобы дашборды и правила переносились между сервисами.
//
// Доменные метрики (cubic_root_*) описывают работу сервиса: сколько вычислений
// выполнено, сколько шагов они стоили и насколько точны результаты.
type metrics struct {
	requestDuration *prometheus.HistogramVec
	activeRequests  *prometheus.GaugeVec
	responseSize    *prometheus.HistogramVec

	calculations     *prometheus.CounterVec
	iterations       prometheus.Histogram
	relativeError    prometheus.Histogram
	inputMagnitude   prometheus.Histogram
	validationErrors *prometheus.CounterVec
}

func newMetrics(reg prometheus.Registerer) *metrics {
	m := &metrics{
		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_server_request_duration_seconds",
			Help:    "Duration of HTTP server requests.",
			Buckets: httpDurationBuckets,
		}, []string{"http_request_method", "http_route", "http_response_status_code"}),
		activeRequests: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "http_server_active_requests",
			Help: "Number of HTTP server requests currently in flight.",
		}, []string{"http_request_method"}),
		responseSize: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_server_response_body_size_bytes",
			Help:    "Size of HTTP server response bodies.",
			Buckets: prometheus.ExponentialBuckets(16, 2, 8), // 16 B … 2 KiB
		}, []string{"http_route"}),
	}
	m.calculations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "cubic_root_calculations_total",
		Help: "Cube root computations actually performed, by outcome.",
	}, []string{"outcome"})
	m.iterations = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "cubic_root_newton_iterations",
		Help:    "Newton steps spent on one computation.",
		Buckets: []float64{1, 2, 3, 5, 8, 13, 21, 34, 55, 89, 100},
	})
	m.relativeError = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "cubic_root_result_relative_error",
		Help: "Relative error |z^3-d|/|d| of computed results; errors of 100% and more are recorded as 1.",
		// 1e-12 — граница correctness SLI.
		Buckets: []float64{1e-16, 1e-15, 1e-14, 1e-13, 1e-12, 1e-11, 1e-10, 1e-9, 1e-8, 1e-6, 1e-4, 1e-2},
	})
	m.inputMagnitude = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "cubic_root_input_magnitude_log10",
		Help:    "Order of magnitude log10(|d|) of valid non-zero inputs.",
		Buckets: []float64{-300, -100, -20, -12, -6, -3, 0, 3, 6, 9, 12, 20, 27, 50, 100, 308},
	})
	m.validationErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "cubic_root_validation_errors_total",
		Help: "Requests rejected because of invalid input, by reason.",
	}, []string{"reason"})

	// Серии с нулём создаются заранее: иначе доля ошибок до первой ошибки
	// превращается в «no data» вместо 0.
	for _, o := range outcomes {
		m.calculations.WithLabelValues(string(o))
	}
	for _, r := range validationReasons {
		m.validationErrors.WithLabelValues(r)
	}

	reg.MustRegister(
		m.requestDuration, m.activeRequests, m.responseSize,
		m.calculations, m.iterations, m.relativeError, m.inputMagnitude, m.validationErrors,
	)
	return m
}

// observeInput записывает порядок величины входа: профиль нагрузки.
func (m *metrics) observeInput(d float64) {
	if d == 0 {
		return
	}
	m.inputMagnitude.Observe(math.Log10(math.Abs(d)))
}

// observeCalculation записывает выполненное вычисление: исход, число шагов и
// точность результата.
func (m *metrics) observeCalculation(d, result float64, st calcStats) {
	m.calculations.WithLabelValues(string(st.Outcome)).Inc()
	if st.Outcome == outcomeTrivial {
		return
	}
	m.iterations.Observe(float64(st.Iterations))
	m.relativeError.Observe(relativeError(d, result))
}

// newRegistry создаёт registry с метриками рантайма Go, процесса и сборки.
func newRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(collectors.WithGoCollectorRuntimeMetrics(
			// go_sched_latencies_seconds: сколько горутина ждёт в очереди
			// планировщика. Лучший сигнал CPU saturation для Go-сервиса.
			collectors.MetricsScheduler,
			// go_sync_mutex_wait_total_seconds_total: цена contention на мьютексах.
			collectors.GoRuntimeMetricsRule{Matcher: regexp.MustCompile(`^/sync/mutex/wait/total:seconds$`)},
			// go_cpu_classes_*: разбивка CPU по классам, в том числе доля GC.
			collectors.GoRuntimeMetricsRule{Matcher: regexp.MustCompile(`^/cpu/classes/.*`)},
		)),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewBuildInfoCollector(),
		newBuildInfo(),
	)
	return reg
}

// newBuildInfo возвращает cubic_root_build_info{version, revision} 1: по смене
// значения видны деплои, а labels можно приджойнить к другим метрикам.
func newBuildInfo() prometheus.Collector {
	g := prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "cubic_root_build_info",
		Help:        "Build information of cubic-root, always 1.",
		ConstLabels: prometheus.Labels{"version": Version, "revision": vcsRevision()},
	})
	g.Set(1)
	return g
}

// vcsRevision возвращает коммит, из которого собран бинарник. Go записывает
// его, только если сборка шла внутри git-репозитория.
func vcsRevision() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				return s.Value
			}
		}
	}
	return "unknown"
}
