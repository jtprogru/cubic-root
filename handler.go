package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type CubicRootResponse struct {
	Result  float64 `json:"result"`
	Message string  `json:"message"`
}

// api — обработчики публичного API и их зависимости.
type api struct {
	metrics *metrics
	tracer  oteltrace.Tracer
}

func newAPI(m *metrics) *api {
	return &api{
		metrics: m,
		tracer:  otel.Tracer("cubic-root"),
	}
}

func (a *api) cubicRoot(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	d, err := parseD(r.URL.Query())
	if err != nil {
		var verr *validationError
		if errors.As(err, &verr) {
			a.metrics.validationErrors.WithLabelValues(verr.reason).Inc()
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.metrics.observeInput(d)

	_, calcSpan := a.tracer.Start(ctx, "cubeRoot.calculate",
		oteltrace.WithAttributes(attribute.Float64("input", d)),
	)
	result, stats := cubeRoot(d)
	calcSpan.SetAttributes(
		attribute.Float64("result", result),
		attribute.Int("cuberoot.iterations", stats.Iterations),
		attribute.String("cuberoot.outcome", string(stats.Outcome)),
	)
	calcSpan.End()
	a.metrics.observeCalculation(d, result, stats)

	logArgs := append(traceAttrs(ctx),
		"d", d, "result", result,
		"iterations", stats.Iterations, "outcome", stats.Outcome,
	)
	if stats.Outcome.failed() {
		slog.WarnContext(ctx, "cubic root did not converge", logArgs...)
	} else {
		slog.DebugContext(ctx, "calculated cubic root", logArgs...)
	}

	w.Header().Set("Content-Type", "application/json")
	responseBytes, err := json.Marshal(CubicRootResponse{Result: result, Message: "Done"})
	if err != nil {
		errArgs := append(traceAttrs(ctx), "err", err)
		slog.ErrorContext(ctx, "encode response", errArgs...)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	_, _ = w.Write(responseBytes)
}

// Причины отказа во входных данных: значения label reason метрики
// cubic_root_validation_errors_total.
const (
	reasonMissing    = "missing"
	reasonNotANumber = "not_a_number"
	reasonNonFinite  = "non_finite"
)

var validationReasons = []string{reasonMissing, reasonNotANumber, reasonNonFinite}

// validationError — ошибка во входных данных запроса.
type validationError struct {
	reason string
	msg    string
}

func (e *validationError) Error() string { return e.msg }

// parseD читает и проверяет параметр d.
func parseD(values url.Values) (float64, error) {
	raw := values.Get("d")
	if raw == "" {
		return 0, &validationError{reasonMissing, "missing parameter 'd'"}
	}

	d, err := strconv.ParseFloat(raw, 64)
	switch {
	case errors.Is(err, strconv.ErrRange):
		return 0, &validationError{reasonNonFinite, "parameter 'd' is out of float64 range"}
	case err != nil:
		return 0, &validationError{reasonNotANumber, fmt.Sprintf("invalid parameter 'd': %v", err)}
	case math.IsNaN(d) || math.IsInf(d, 0):
		return 0, &validationError{reasonNonFinite, "parameter 'd' must be a finite number"}
	}
	return d, nil
}
