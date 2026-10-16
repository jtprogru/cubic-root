package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/singleflight"

	"github.com/jtprogru/cubic-root/internal/cache"
)

type CubicRootResponse struct {
	Result  float64 `json:"result"`
	Message string  `json:"message"`
}

// cubicRootResult — результат вычисления; в таком виде он хранится в кэше.
type cubicRootResult struct {
	value float64
	stats calcStats
}

// cacheStatus — откуда взят ответ. Уходит в заголовок X-Cache, атрибут спана
// и лог.
type cacheStatus string

const (
	cacheHit      cacheStatus = "HIT"
	cacheMiss     cacheStatus = "MISS"
	cacheDisabled cacheStatus = "DISABLED"
)

// api — обработчики публичного API и их зависимости.
type api struct {
	cfg     apiConfig
	metrics *metrics
	tracer  oteltrace.Tracer
	// cache равен nil, если кэш выключен (CACHE_SIZE=0).
	cache  *cache.LRU[uint64, cubicRootResult]
	flight singleflight.Group
}

// newAPI создаёт обработчики и, если кэш включён, сам кэш. Размер кэша
// регистрируется в reg как метрики, которые считываются при scrape.
func newAPI(cfg apiConfig, m *metrics, reg prometheus.Registerer) *api {
	a := &api{
		cfg:     cfg,
		metrics: m,
		tracer:  otel.Tracer("cubic-root"),
	}
	if cfg.CacheSize > 0 {
		a.cache = cache.New[uint64, cubicRootResult](cfg.CacheSize, cfg.CacheTTL,
			cache.WithObserver(newCacheObserver(m)),
		)
	}
	m.registerCacheSize(reg, a.cacheLen, cfg.CacheSize)
	return a
}

func (a *api) cacheLen() int {
	if a.cache == nil {
		return 0
	}
	return a.cache.Len()
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

	res, status := a.result(ctx, d)
	oteltrace.SpanFromContext(ctx).SetAttributes(attribute.String("cache.status", string(status)))

	logArgs := append(traceAttrs(ctx),
		"d", d, "result", res.value,
		"iterations", res.stats.Iterations, "outcome", res.stats.Outcome,
		"cache", status,
	)
	slog.DebugContext(ctx, "calculated cubic root", logArgs...)

	w.Header().Set("Content-Type", "application/json")
	if status != cacheDisabled {
		w.Header().Set("X-Cache", string(status))
	}
	responseBytes, err := json.Marshal(CubicRootResponse{Result: res.value, Message: "Done"})
	if err != nil {
		errArgs := append(traceAttrs(ctx), "err", err)
		slog.ErrorContext(ctx, "encode response", errArgs...)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	_, _ = w.Write(responseBytes)
}

// result возвращает кубический корень d из кэша или вычисляет его.
// Одновременные промахи по одному ключу ждут одно вычисление (singleflight),
// а не запускают каждый своё.
func (a *api) result(ctx context.Context, d float64) (cubicRootResult, cacheStatus) {
	if a.cache == nil {
		return a.compute(ctx, d), cacheDisabled
	}

	key := cacheKey(d)
	if res, ok := a.cache.Get(key); ok {
		return res, cacheHit
	}

	leader := false
	v, _, shared := a.flight.Do(strconv.FormatUint(key, 16), func() (any, error) {
		// Функцию выполняет только первый запрос, остальные ждут его
		// результат. Отмена этого запроса не должна обрывать вычисление для
		// ожидающих, поэтому контекст отвязан от отмены.
		leader = true
		res := a.compute(context.WithoutCancel(ctx), d)
		if !res.stats.Outcome.failed() {
			a.cache.Add(key, res)
		}
		return res, nil
	})
	if shared && !leader {
		a.metrics.cacheCoalesced.Inc()
		oteltrace.SpanFromContext(ctx).SetAttributes(attribute.Bool("cache.coalesced", true))
	}
	return v.(cubicRootResult), cacheMiss
}

// compute выполняет вычисление: с искусственной задержкой, если задан
// CALC_DELAY, и с метриками работы.
func (a *api) compute(ctx context.Context, d float64) cubicRootResult {
	ctx, span := a.tracer.Start(ctx, "cubeRoot.calculate",
		oteltrace.WithAttributes(attribute.Float64("input", d)),
	)
	defer span.End()

	if delay := a.calcDelay(); delay > 0 {
		_ = sleep(ctx, delay)
	}
	value, stats := cubeRoot(d)
	span.SetAttributes(
		attribute.Float64("result", value),
		attribute.Int("cuberoot.iterations", stats.Iterations),
		attribute.String("cuberoot.outcome", string(stats.Outcome)),
	)
	a.metrics.observeCalculation(d, value, stats)

	if stats.Outcome.failed() {
		logArgs := append(traceAttrs(ctx),
			"d", d, "result", value,
			"iterations", stats.Iterations, "outcome", stats.Outcome,
		)
		slog.WarnContext(ctx, "cubic root did not converge", logArgs...)
	}
	return cubicRootResult{value: value, stats: stats}
}

// calcDelay возвращает CALC_DELAY с равномерным разбросом ±CALC_DELAY_JITTER.
func (a *api) calcDelay() time.Duration {
	delay := a.cfg.CalcDelay
	if jitter := a.cfg.CalcDelayJitter; jitter > 0 {
		delay += time.Duration(rand.Int64N(2*int64(jitter)+1)) - jitter // #nosec G404 -- задержка для экспериментов, не криптография
	}
	return max(delay, 0)
}

// cacheKey — ключ кэша: биты float64 после парсинга, поэтому "27", "27.0" и
// "2.7e1" попадают в одну запись.
func cacheKey(d float64) uint64 {
	if d == 0 {
		d = 0 // -0 == 0, но биты у них разные: приводим -0 к 0.
	}
	return math.Float64bits(d)
}

// sleep ждёт d или отмены ctx.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
