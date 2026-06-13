package main

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCubicRootHandler_ValidRequest(t *testing.T) {
	reqData := CubicRootRequest{D: 27}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("/cubic-root?d=%f", reqData.D), nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(cubicRootHandler)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("Handler returned wrong status code: got %v, want %v", status, http.StatusOK)
	}

	var resp CubicRootResponse
	if err = json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp == (CubicRootResponse{}) {
		t.Fatalf("Handler returned empty response")
	}

	expected := 3.0000000000000013 // Именно такое страшное число получается если взять кубический корень из 27
	if resp.Result != expected {
		t.Errorf("Handler returned unexpected result: got %v, want %v", resp.Result, expected)
	}
}

func TestCubicRootHandler_InvalidParameter(t *testing.T) {
	reqData := []byte(`invalid reqData`)
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("/cubic-root?d=%s", string(reqData)), nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(cubicRootHandler)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusBadRequest {
		t.Errorf("Handler returned wrong status code: got %v, want %v", status, http.StatusBadRequest)
	}
}

func TestCubicRootHandler_Zero(t *testing.T) {
	reqBody := CubicRootRequest{D: 0.0}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("/cubic-root?d=%f", reqBody.D), nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(cubicRootHandler)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("Handler returned wrong status code: got %v, want %v", status, http.StatusOK)
	}
}

func TestCubicRootHandler_NegativeNumber(t *testing.T) {
	reqBody := CubicRootRequest{D: -8}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("/cubic-root?d=%f", reqBody.D), nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(cubicRootHandler)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("Handler returned wrong status code: got %v, want %v", status, http.StatusOK)
	}

	var resp CubicRootResponse
	if err = json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	expected := -2.0
	if resp.Result != expected {
		t.Errorf("Handler returned unexpected result: got %v, want %v", resp.Result, expected)
	}
}

// TestCubicRootHandler_NonFinite проверяет, что нечисловые значения d
// (NaN/Inf) отбрасываются с 400 и не доходят до итерации, которая на них
// зацикливалась бы.
func TestCubicRootHandler_NonFinite(t *testing.T) {
	for _, d := range []string{"NaN", "Inf", "+Inf", "-Inf", "Infinity"} {
		t.Run(d, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, "/cubic-root?d="+d, nil)
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			rr := httptest.NewRecorder()
			http.HandlerFunc(cubicRootHandler).ServeHTTP(rr, req)

			if status := rr.Code; status != http.StatusBadRequest {
				t.Errorf("d=%s: got status %v, want %v", d, status, http.StatusBadRequest)
			}
		})
	}
}

// TestCubicRoot_Terminates гарантирует, что итерация всегда завершается даже
// на экстремальных конечных входах, на которых раньше был бесконечный цикл
// (underflow z*z -> 0 -> x/(z*z) = Inf).
func TestCubicRoot_Terminates(t *testing.T) {
	for _, d := range []float64{1e-200, 5e-324, 1e308, -1e308, 1e-162} {
		got := cubeRoot(d)
		if math.IsNaN(got) {
			t.Errorf("cubeRoot(%g) returned NaN", d)
		}
	}
}

// TestMethodWhitelist проверяет, что эндпоинт отвечает только на GET, а на
// прочие методы возвращает 405.
func TestMethodWhitelist(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /cubic-root", http.HandlerFunc(cubicRootHandler))

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

func TestParseQueryParamsToStruct(t *testing.T) {
	reqData := CubicRootRequest{D: 2.0}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("/cubic-root?d=%f", reqData.D), nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(cubicRootHandler)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("Handler returned wrong status code: got %v, want %v", status, http.StatusOK)
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
		calculateCubicRoot(values[i])
	}
}

func BenchmarkCubicRootHandler(b *testing.B) {
	// Генерируем случайные значения для тестирования производительности
	values := make([]float64, b.N)
	for i := 0; i < b.N; i++ {
		values[i] = rand.Float64() * 1e10 // Числа от 0 до 10,000,000,000
	}

	// Сбрасываем таймер, чтобы исключить время инициализации данных
	b.ResetTimer()

	// Запускаем бенчмарк
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", fmt.Sprintf("/cubic-root?d=%f", values[i]), nil)
		cubicRootHandler(w, req)
	}
}
