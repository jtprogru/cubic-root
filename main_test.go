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

// serve выполняет запрос через публичный handler со свежим registry.
func serve(t *testing.T, method, target string) (*httptest.ResponseRecorder, *prometheus.Registry, *metrics) {
	t.Helper()
	reg := prometheus.NewRegistry()
	m := newMetrics(reg)
	rr := httptest.NewRecorder()
	newPublicHandler(m).ServeHTTP(rr, httptest.NewRequest(method, target, nil))
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
	expected := 3.0000000000000013 // Именно такое страшное число получается если взять кубический корень из 27
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
// заканчивается, в том числе на входах, где текущий алгоритм не сходится.
func TestCubicRoot_Stats(t *testing.T) {
	cases := []struct {
		d          float64
		iterations int
		outcome    outcome
	}{
		{0, 0, outcomeTrivial},
		{27, 8, outcomeConverged},
		{1e9, 37, outcomeConverged},
		{1e30, 100, outcomeMaxIter},
		{1e-200, 1, outcomeDegenerate},
	}
	for _, tc := range cases {
		_, st := cubeRoot(tc.d)
		if st.Iterations != tc.iterations || st.Outcome != tc.outcome {
			t.Errorf("cubeRoot(%g): got %d iterations, %s; want %d, %s", tc.d, st.Iterations, st.Outcome, tc.iterations, tc.outcome)
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
	reg := prometheus.NewRegistry()
	m := newMetrics(reg)
	h := newPublicHandler(m)
	for _, d := range []string{"27", "1e30"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/cubic-root?d="+d, nil))
	}

	if got := testutil.ToFloat64(m.calculations.WithLabelValues(string(outcomeConverged))); got != 1 {
		t.Errorf("converged calculations: got %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.calculations.WithLabelValues(string(outcomeMaxIter))); got != 1 {
		t.Errorf("max_iter calculations: got %v, want 1", got)
	}

	iterations := findMetric(t, reg, "cubic_root_newton_iterations", map[string]string{}).GetHistogram()
	if iterations.GetSampleCount() != 2 || iterations.GetSampleSum() != 8+100 {
		t.Errorf("iterations: got count=%d sum=%v, want count=2 sum=108", iterations.GetSampleCount(), iterations.GetSampleSum())
	}

	// 27 считается точно, а 1e30 даёт ответ с ошибкой больше 100%: он
	// записывается как 1 и не попадает в бакет SLI le=1e-12.
	relErr := findMetric(t, reg, "cubic_root_result_relative_error", map[string]string{}).GetHistogram()
	if relErr.GetSampleCount() != 2 || math.Abs(relErr.GetSampleSum()-1) > 1e-12 {
		t.Errorf("relative error: got count=%d sum=%v, want count=2 sum≈1", relErr.GetSampleCount(), relErr.GetSampleSum())
	}
	for _, b := range relErr.GetBucket() {
		if b.GetUpperBound() == 1e-12 && b.GetCumulativeCount() != 1 {
			t.Errorf("relative error ≤ 1e-12: got %d, want 1", b.GetCumulativeCount())
		}
	}

	magnitude := findMetric(t, reg, "cubic_root_input_magnitude_log10", map[string]string{}).GetHistogram()
	if want := math.Log10(27) + 30; magnitude.GetSampleCount() != 2 || math.Abs(magnitude.GetSampleSum()-want) > 1e-9 {
		t.Errorf("input magnitude: got count=%d sum=%v, want count=2 sum=%v", magnitude.GetSampleCount(), magnitude.GetSampleSum(), want)
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
	mux := newPublicHandler(newMetrics(prometheus.NewRegistry()))

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
	a := newAPI(newMetrics(prometheus.NewRegistry()))

	// Сбрасываем таймер, чтобы исключить время инициализации данных
	b.ResetTimer()

	// Запускаем бенчмарк
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", fmt.Sprintf("/cubic-root?d=%f", values[i]), nil)
		a.cubicRoot(w, req)
	}
}
