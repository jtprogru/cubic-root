package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const (
	Version     = "0.3.0"
	articleLink = "https://jtprog.ru/interview-task-0003/"
)

// setupLogging настраивает JSON-логгер slog, в debug-режиме с уровнем DEBUG.
func setupLogging(debug bool) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})))
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
	// Невалидное или пустое значение DEBUG означает выключенный debug-режим.
	debugMode, _ := strconv.ParseBool(os.Getenv("DEBUG"))
	setupLogging(debugMode)

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

	reg := newRegistry()
	m := newMetrics(reg)

	slog.Info("server starting",
		"version", Version,
		"port", port,
		"metricsPort", metricsPort,
		"debug", debugMode,
		"article", articleLink,
	)

	server := newHTTPServer(port, newPublicHandler(m))
	metricsServer := newHTTPServer(metricsPort, newMetricsHandler(reg))

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

// newPublicHandler собирает публичный listener: бизнес-API и health-пробу,
// обёрнутые HTTP-метриками.
func newPublicHandler(m *metrics) http.Handler {
	a := newAPI(m)
	routes := map[string]http.Handler{
		"GET /health":     otelhttp.NewHandler(http.HandlerFunc(healthHandler), "/health"),
		"GET /cubic-root": otelhttp.NewHandler(http.HandlerFunc(a.cubicRoot), "/cubic-root"),
	}
	mux := http.NewServeMux()
	for pattern, h := range routes {
		mux.Handle(pattern, h)
	}
	return instrumentHTTP(mux, m, slices.Collect(maps.Keys(routes))...)
}

// newMetricsHandler собирает internal listener: метрики вынесены на отдельный
// порт, чтобы их нельзя было достать через публичный Ingress/сервис.
func newMetricsHandler(reg *prometheus.Registry) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.InstrumentMetricHandler(reg,
		promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg}),
	))
	return mux
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
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
