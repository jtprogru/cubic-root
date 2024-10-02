package main

import (
	"encoding/json"
	"fmt"
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

	var expected float64 = 3.0000000000000013 // Именно такое страшное число получается если взять кубический корень из 27
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
		t.Errorf("Handler returned wrong status code: got %v, want %v", status, http.StatusBadRequest)
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
	w := httptest.NewRecorder()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", fmt.Sprintf("/cubic-root?d=%f", values[i]), nil)
		cubicRootHandler(w, req)
	}
}
