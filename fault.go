package main

import (
	"log/slog"
	"math/rand/v2"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// statusClientClosedRequest — ответ на запрос, клиент которого ушёл, не
// дождавшись ответа (код из nginx). Так отменённые запросы не попадают в 5xx и
// не расходуют бюджет ошибок.
const statusClientClosedRequest = 499

// enabled сообщает, включена ли хоть одна инъекция.
func (f faultConfig) enabled() bool {
	return f.ErrorRate > 0 || (f.Latency > 0 && f.LatencyRate > 0)
}

// injectFaults добавляет к ответам next ошибки и задержки, чтобы проверять
// дашборды, SLO и алерты на живых сбоях. Включается только переменными
// окружения FAULT_*: через запрос включить сбои нельзя.
func injectFaults(next http.Handler, cfg faultConfig, m *metrics) http.Handler {
	if !cfg.enabled() {
		return next
	}
	latency := m.injectedFaults.WithLabelValues("latency")
	failure := m.injectedFaults.WithLabelValues("error")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		span := oteltrace.SpanFromContext(ctx)

		if cfg.Latency > 0 && chance(cfg.LatencyRate) {
			latency.Inc()
			span.SetAttributes(attribute.Bool("fault.latency", true))
			if err := sleep(ctx, cfg.Latency); err != nil {
				w.WriteHeader(statusClientClosedRequest)
				return
			}
		}
		if chance(cfg.ErrorRate) {
			failure.Inc()
			span.SetAttributes(attribute.Bool("fault.error", true))
			span.SetStatus(codes.Error, "injected fault")
			slog.WarnContext(ctx, "injected fault", append(traceAttrs(ctx), "fault", "error")...)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// chance возвращает true с вероятностью p.
func chance(p float64) bool {
	return p > 0 && rand.Float64() < p // #nosec G404 -- инъекция сбоев, не криптография
}
