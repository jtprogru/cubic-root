package main

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// testAPIConfig — конфигурация API для тестов: кэш выключен, чтобы каждый
// запрос считался заново.
var testAPIConfig = apiConfig{}

// newTestHandler собирает публичный handler на свежем registry.
func newTestHandler(cfg apiConfig) (http.Handler, *prometheus.Registry, *metrics) {
	reg := prometheus.NewRegistry()
	m := newMetrics(reg)
	return newPublicHandler(newAPI(cfg, m, reg)), reg, m
}

// newTestHandlerWithCalc — то же, но с подменённым вычислением.
func newTestHandlerWithCalc(cfg apiConfig, calc func(float64) (float64, calcStats)) (http.Handler, *prometheus.Registry, *metrics) {
	reg := prometheus.NewRegistry()
	m := newMetrics(reg)
	a := newAPI(cfg, m, reg)
	a.calc = calc
	return newPublicHandler(a), reg, m
}

// notConverging имитирует вычисление, которое не сошлось.
func notConverging(float64) (float64, calcStats) {
	return 1e15, calcStats{Iterations: 100, Outcome: outcomeMaxIter}
}

// serve выполняет запрос через публичный handler со свежим registry.
func serve(t *testing.T, method, target string) (*httptest.ResponseRecorder, *prometheus.Registry, *metrics) {
	t.Helper()
	h, reg, m := newTestHandler(testAPIConfig)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(method, target, nil))
	return rr, reg, m
}

func decodeResponse(t *testing.T, rr *httptest.ResponseRecorder) CubicRootResponse {
	t.Helper()
	var resp CubicRootResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	return resp
}

func TestCubicRootHandler_ValidRequest(t *testing.T) {
	rr, _, _ := serve(t, http.MethodGet, "/cubic-root?d=27")

	if status := rr.Code; status != http.StatusOK {
		t.Fatalf("Handler returned wrong status code: got %v, want %v", status, http.StatusOK)
	}
	resp := decodeResponse(t, rr)
	// Раньше здесь было 3.0000000000000013: итерация останавливалась по
	// абсолютной точности 1e-10 и не доводила результат до точного.
	expected := 3.0
	if resp.Result != expected {
		t.Errorf("Handler returned unexpected result: got %v, want %v", resp.Result, expected)
	}
}

func TestCubicRootHandler_Zero(t *testing.T) {
	rr, _, m := serve(t, http.MethodGet, "/cubic-root?d=0")

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("Handler returned wrong status code: got %v, want %v", status, http.StatusOK)
	}
	if got := testutil.ToFloat64(m.calculations.WithLabelValues(string(outcomeTrivial))); got != 1 {
		t.Errorf("trivial calculations: got %v, want 1", got)
	}
}

func TestCubicRootHandler_NegativeNumber(t *testing.T) {
	rr, _, _ := serve(t, http.MethodGet, "/cubic-root?d=-8")

	if status := rr.Code; status != http.StatusOK {
		t.Fatalf("Handler returned wrong status code: got %v, want %v", status, http.StatusOK)
	}
	if resp := decodeResponse(t, rr); resp.Result != -2.0 {
		t.Errorf("Handler returned unexpected result: got %v, want %v", resp.Result, -2.0)
	}
}

// TestCubicRootHandler_Invalid проверяет, что невалидные d отбрасываются с 400
// и учитываются в cubic_root_validation_errors_total с правильной причиной.
// NaN/Inf не должны доходить до итерации, которая на них зацикливалась бы.
func TestCubicRootHandler_Invalid(t *testing.T) {
	cases := []struct {
		query, reason string
	}{
		{"", reasonMissing},
		{"d=", reasonMissing},
		{"d=abc", reasonNotANumber},
		{"d=27abc", reasonNotANumber},
		{"d=NaN", reasonNonFinite},
		{"d=Inf", reasonNonFinite},
		{"d=%2BInf", reasonNonFinite}, // "+" в query string декодируется в пробел
		{"d=-Inf", reasonNonFinite},
		{"d=Infinity", reasonNonFinite},
		{"d=1e400", reasonNonFinite},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			rr, _, m := serve(t, http.MethodGet, "/cubic-root?"+tc.query)

			if status := rr.Code; status != http.StatusBadRequest {
				t.Errorf("got status %v, want %v", status, http.StatusBadRequest)
			}
			if got := testutil.ToFloat64(m.validationErrors.WithLabelValues(tc.reason)); got != 1 {
				t.Errorf("validation errors{reason=%q}: got %v, want 1", tc.reason, got)
			}
			if got := testutil.CollectAndCount(m.calculations); got != len(outcomes) {
				t.Errorf("calculations series: got %d, want %d pre-initialised", got, len(outcomes))
			}
		})
	}
}

// TestCubicRoot_Terminates гарантирует, что итерация всегда завершается даже
// на экстремальных конечных входах, на которых раньше был бесконечный цикл
// (underflow z*z -> 0 -> x/(z*z) = Inf).
func TestCubicRoot_Terminates(t *testing.T) {
	for _, d := range []float64{1e-200, 5e-324, 1e308, -1e308, 1e-162} {
		got, _ := cubeRoot(d)
		if math.IsNaN(got) {
			t.Errorf("cubeRoot(%g) returned NaN", d)
		}
	}
}

// TestCubicRoot_Stats фиксирует, сколько работы стоит вычисление и чем оно
// заканчивается. До перехода на итерацию по мантиссе 1e9 стоил 37 шагов, 1e30
// упирался в лимит 100 шагов, а 1e-200 вырождался.
func TestCubicRoot_Stats(t *testing.T) {
	cases := []struct {
		d          float64
		iterations int
		outcome    outcome
	}{
		{0, 0, outcomeTrivial},
		{27, 4, outcomeConverged},
		{1e9, 5, outcomeConverged},
		{1e30, 5, outcomeConverged},
		{1e-200, 4, outcomeConverged},
		{math.NaN(), 0, outcomeDegenerate},
		{math.Inf(-1), 0, outcomeDegenerate},
	}
	for _, tc := range cases {
		_, st := cubeRoot(tc.d)
		if st.Iterations != tc.iterations || st.Outcome != tc.outcome {
			t.Errorf("cubeRoot(%g): got %d iterations, %s; want %d, %s", tc.d, st.Iterations, st.Outcome, tc.iterations, tc.outcome)
		}
	}
}

// TestCubicRoot_MatchesCbrt сверяет результат с math.Cbrt на входах по всему
// диапазону float64, включая субнормальные числа, с обоими знаками.
func TestCubicRoot_MatchesCbrt(t *testing.T) {
	r := rand.New(rand.NewPCG(27, 3))
	for range 200_000 {
		d := math.Pow(10, -323+r.Float64()*631)
		if r.IntN(2) == 0 {
			d = -d
		}
		if d == 0 || math.IsInf(d, 0) {
			continue
		}
		got, st := cubeRoot(d)
		want := math.Cbrt(d)
		if diff := math.Abs(got-want) / math.Abs(want); diff > 1e-15 || st.Outcome != outcomeConverged || st.Iterations > 6 {
			t.Fatalf("cubeRoot(%g) = %g after %d iterations (%s), math.Cbrt = %g, relative diff %g",
				d, got, st.Iterations, st.Outcome, want, diff)
		}
	}
}

func TestRelativeError(t *testing.T) {
	cases := []struct {
		d, z, want, tolerance float64
	}{
		{27, 3, 0, 1e-15},
		{-8, -2, 0, 1e-15},
		{8, 2.5, 0.953125, 1e-15},
		{1e300, 1e100, 0, 1e-15},
		{5e-324, math.Cbrt(5e-324), 0, 1e-15},
		{1, math.NaN(), 1, 0},
		{1, 1e200, 1, 0}, // z*z переполняется
		{1, 10, 1, 0},    // ошибка больше 100% записывается как 1
	}
	for _, tc := range cases {
		if got := relativeError(tc.d, tc.z); math.Abs(got-tc.want) > tc.tolerance {
			t.Errorf("relativeError(%g, %g) = %g, want %g", tc.d, tc.z, got, tc.want)
		}
	}
}

func TestDomainMetrics(t *testing.T) {
	h, reg, m := newTestHandler(testAPIConfig)
	for _, d := range []string{"27", "1e30"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/cubic-root?d="+d, nil))
	}

	if got := testutil.ToFloat64(m.calculations.WithLabelValues(string(outcomeConverged))); got != 2 {
		t.Errorf("converged calculations: got %v, want 2", got)
	}

	iterations := findMetric(t, reg, "cubic_root_newton_iterations", map[string]string{}).GetHistogram()
	if iterations.GetSampleCount() != 2 || iterations.GetSampleSum() != 4+5 {
		t.Errorf("iterations: got count=%d sum=%v, want count=2 sum=9", iterations.GetSampleCount(), iterations.GetSampleSum())
	}

	relErr := findMetric(t, reg, "cubic_root_result_relative_error", map[string]string{}).GetHistogram()
	if relErr.GetSampleCount() != 2 {
		t.Errorf("relative error: got count=%d, want 2", relErr.GetSampleCount())
	}
	for _, b := range relErr.GetBucket() {
		if b.GetUpperBound() == 1e-12 && b.GetCumulativeCount() != 2 {
			t.Errorf("relative error ≤ 1e-12: got %d, want 2", b.GetCumulativeCount())
		}
	}

	magnitude := findMetric(t, reg, "cubic_root_input_magnitude_log10", map[string]string{}).GetHistogram()
	if want := math.Log10(27) + 30; magnitude.GetSampleCount() != 2 || math.Abs(magnitude.GetSampleSum()-want) > 1e-9 {
		t.Errorf("input magnitude: got count=%d sum=%v, want count=2 sum=%v", magnitude.GetSampleCount(), magnitude.GetSampleSum(), want)
	}
}

// TestNonConverged_Returns500: если вычисление не сошлось, клиент получает 500,
// а не число, которое не отличить от верного. Ошибка видна в метриках.
func TestNonConverged_Returns500(t *testing.T) {
	h, reg, m := newTestHandlerWithCalc(testAPIConfig, notConverging)

	rr := get(h, "/cubic-root?d=1e30")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rr.Code)
	}
	if got := testutil.ToFloat64(m.calculations.WithLabelValues(string(outcomeMaxIter))); got != 1 {
		t.Errorf("max_iter calculations: got %v, want 1", got)
	}
	// Ответ с ошибкой больше 100% записывается как 1 и не попадает в бакет SLI.
	relErr := findMetric(t, reg, "cubic_root_result_relative_error", map[string]string{}).GetHistogram()
	if relErr.GetSampleCount() != 1 || relErr.GetSampleSum() != 1 {
		t.Errorf("relative error: got count=%d sum=%v, want count=1 sum=1", relErr.GetSampleCount(), relErr.GetSampleSum())
	}
}

func TestParseD(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want float64
	}{
		{"2", 2}, {"2.000000", 2}, {"2.7e1", 27}, {"-0.5", -0.5}, {"1e-400", 0},
	} {
		got, err := parseD(map[string][]string{"d": {tc.raw}})
		if err != nil || got != tc.want {
			t.Errorf("parseD(%q) = %v, %v; want %v", tc.raw, got, err, tc.want)
		}
	}
}

// TestMethodWhitelist проверяет, что эндпоинт отвечает только на GET, а на
// прочие методы возвращает 405.
func TestMethodWhitelist(t *testing.T) {
	mux, _, _ := newTestHandler(testAPIConfig)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/cubic-root?d=27", nil)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s: got status %v, want %v", method, rr.Code, http.StatusMethodNotAllowed)
			}
		})
	}
}

func BenchmarkCubicRoot(b *testing.B) {
	// Генерируем случайные значения для тестирования производительности
	values := make([]float64, b.N)
	for i := 0; i < b.N; i++ {
		values[i] = rand.Float64() * 1e10 // Числа от 0 до 10,000,000,000
	}

	// Сбрасываем таймер, чтобы исключить время инициализации данных
	b.ResetTimer()

	// Запускаем бенчмарк
	for i := 0; i < b.N; i++ {
		cubeRoot(values[i])
	}
}

func BenchmarkCubicRootHandler(b *testing.B) {
	// Генерируем случайные значения для тестирования производительности
	values := make([]float64, b.N)
	for i := 0; i < b.N; i++ {
		values[i] = rand.Float64() * 1e10 // Числа от 0 до 10,000,000,000
	}
	reg := prometheus.NewRegistry()
	a := newAPI(testAPIConfig, newMetrics(reg), reg)

	// Сбрасываем таймер, чтобы исключить время инициализации данных
	b.ResetTimer()

	// Запускаем бенчмарк
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", fmt.Sprintf("/cubic-root?d=%f", values[i]), nil)
		a.cubicRoot(w, req)
	}
}
