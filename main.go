package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const (
	Version     = "0.2.0"
	articleLink = "https://jtprog.ru/interview-task-0003/"
)

var (
	debugMode     bool
	requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cubic_root_requests_total",
			Help: "Total number of requests to the cubic root endpoint",
		},
		[]string{"status"},
	)
	requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "cubic_root_request_duration_seconds",
			Help:    "Histogram of response time for cubic root requests",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"status"},
	)
	requestSize = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "cubic_root_request_size_bytes",
			Help:    "Histogram of request sizes in bytes.",
			Buckets: prometheus.ExponentialBuckets(10, 2, 10),
		},
	)
	responseSize = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "cubic_root_response_size_bytes",
			Help:    "Histogram of response sizes in bytes.",
			Buckets: prometheus.ExponentialBuckets(10, 2, 10),
		},
	)
	activeRequests = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "cubic_root_active_requests",
			Help: "Number of active requests being processed.",
		},
	)
)

func init() {
	var err error
	debugEnv := os.Getenv("DEBUG")
	debugMode, err = strconv.ParseBool(debugEnv)
	if err != nil {
		debugMode = false
	}

	level := slog.LevelInfo
	if debugMode {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})))

	prometheus.MustRegister(requestSize)
	prometheus.MustRegister(responseSize)
	prometheus.MustRegister(activeRequests)
	prometheus.MustRegister(requestsTotal)
	prometheus.MustRegister(requestDuration)
}

// traceAttrs extracts trace_id and span_id from ctx for structured logging.
func traceAttrs(ctx context.Context) []any {
	sc := oteltrace.SpanFromContext(ctx).SpanContext()
	if !sc.IsValid() {
		return nil
	}
	return []any{
		"trace_id", sc.TraceID().String(),
		"span_id", sc.SpanID().String(),
	}
}

func initTracer(ctx context.Context) (func(), error) {
	exp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create OTLP exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String("cubic-root"),
			semconv.ServiceVersionKey.String(Version),
		)),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			slog.Error("tracer provider shutdown", "err", err)
		}
	}, nil
}

func main() {
	ctx := context.Background()

	port, err := portFromEnv("PORT", 8080)
	if err != nil {
		slog.Error("invalid PORT", "err", err)
		os.Exit(1)
	}
	metricsPort, err := portFromEnv("METRICS_PORT", 9090)
	if err != nil {
		slog.Error("invalid METRICS_PORT", "err", err)
		os.Exit(1)
	}

	shutdownTracer, err := initTracer(ctx)
	if err != nil {
		slog.Warn("tracing disabled", "err", err)
		shutdownTracer = func() {}
	}

	// Публичный listener: бизнес-API и health-проба.
	publicMux := http.NewServeMux()
	publicMux.Handle("GET /health", otelhttp.NewHandler(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
		"/health",
	))
	publicMux.Handle("GET /cubic-root", otelhttp.NewHandler(
		http.HandlerFunc(cubicRootHandler),
		"/cubic-root",
	))

	// Internal listener: метрики Prometheus вынесены на отдельный порт, чтобы
	// их нельзя было достать через публичный Ingress/сервис.
	metricsMux := http.NewServeMux()
	metricsMux.Handle("GET /metrics", promhttp.Handler())

	slog.Info("server starting",
		"version", Version,
		"port", port,
		"metricsPort", metricsPort,
		"debug", debugMode,
		"article", articleLink,
	)

	server := newHTTPServer(port, publicMux)
	metricsServer := newHTTPServer(metricsPort, metricsMux)

	serveErr := make(chan error, 2)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("public server: %w", err)
		}
	}()
	go func() {
		if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("metrics server: %w", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-quit:
		slog.Info("shutting down servers")
	case err := <-serveErr:
		slog.Error("server error", "err", err)
		os.Exit(1)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	shutdownTracer()

	if err := metricsServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("metrics server forced to shutdown", "err", err)
	}
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("server forced to shutdown", "err", err)
		os.Exit(1)
	}
	slog.Info("server stopped gracefully")
}

// portFromEnv читает порт из переменной окружения name, возвращая def, если
// переменная не задана.
func portFromEnv(name string, def int) (int, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	return strconv.Atoi(v)
}

// newHTTPServer создаёт http.Server с консервативными тайм-аутами и лимитом
// размера заголовков (защита от Slowloris и раздутых заголовков).
func newHTTPServer(port int, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           handler,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 16, // 64 KiB
	}
}

func cubicRootHandler(w http.ResponseWriter, r *http.Request) {
	activeRequests.Inc()
	defer activeRequests.Dec()

	startTime := time.Now()
	var status string

	defer func() {
		duration := time.Since(startTime).Seconds()
		requestDuration.WithLabelValues(status).Observe(duration)
	}()

	if r.ContentLength >= 0 {
		requestSize.Observe(float64(r.ContentLength))
	}

	var req CubicRootRequest
	if err := parseQueryParamsToStruct(r.URL.Query(), &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		status = "400"
		requestsTotal.WithLabelValues(status).Inc()
		return
	}

	ctx := r.Context()
	tracer := otel.Tracer("cubic-root")
	_, calcSpan := tracer.Start(ctx, "cubeRoot.calculate",
		oteltrace.WithAttributes(attribute.Float64("input", req.D)),
	)

	result := calculateCubicRoot(req.D)

	calcSpan.SetAttributes(attribute.Float64("result", result.Result))
	calcSpan.End()

	logArgs := append(traceAttrs(ctx), "d", req.D, "result", result.Result)
	slog.DebugContext(ctx, "calculated cubic root", logArgs...)

	w.Header().Set("Content-Type", "application/json")
	responseBytes, err := json.Marshal(result)
	if err != nil {
		errArgs := append(traceAttrs(ctx), "err", err)
		slog.ErrorContext(ctx, "encode response", errArgs...)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		status = "500"
		requestsTotal.WithLabelValues(status).Inc()
		return
	}

	responseSize.Observe(float64(len(responseBytes)))
	_, _ = w.Write(responseBytes)
	status = "200"
	requestsTotal.WithLabelValues(status).Inc()
}

type CubicRootRequest struct {
	D float64 `json:"d"`
}

type CubicRootResponse struct {
	Result  float64 `json:"result"`
	Message string  `json:"message"`
}

func calculateCubicRoot(d float64) CubicRootResponse {
	return CubicRootResponse{
		Result:  cubeRoot(d),
		Message: "Done",
	}
}

func parseQueryParamsToStruct(values url.Values, target any) error {
	req, ok := target.(*CubicRootRequest)
	if !ok {
		return fmt.Errorf("target must be of type *CubicRootRequest")
	}

	dParam := values.Get("d")
	if dParam == "" {
		return fmt.Errorf("missing parameter 'd'")
	}

	d, err := strconv.ParseFloat(dParam, 64)
	if err != nil {
		return fmt.Errorf("invalid parameter 'd': %v", err)
	}
	if math.IsNaN(d) || math.IsInf(d, 0) {
		return fmt.Errorf("parameter 'd' must be a finite number")
	}

	req.D = d
	return nil
}

func cubeRoot(x float64) float64 {
	if x == 0 {
		return 0
	}

	const (
		precision = 1e-10
		maxIter   = 100
	)

	z := x / 3
	for i := 0; i < maxIter; i++ {
		// z может занулиться или уйти в Inf на экстремально малых/больших
		// входах — в этом случае x/(z*z) даёт Inf/NaN и итерация перестаёт
		// сходиться. Прерываемся, чтобы цикл всегда завершался.
		if z == 0 || math.IsInf(z, 0) {
			break
		}
		nextZ := (2*z + x/(z*z)) / 3
		if math.Abs(nextZ-z) < precision {
			break
		}
		z = nextZ
	}

	return z
}
