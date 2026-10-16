package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestFaults_Disabled(t *testing.T) {
	h, _, m := newTestHandler(testAPIConfig)
	for range 20 {
		if rr := get(h, "/cubic-root?d=27"); rr.Code != http.StatusOK {
			t.Fatalf("status %d with fault injection disabled", rr.Code)
		}
	}
	if got := testutil.ToFloat64(m.faultInjectionEnabled); got != 0 {
		t.Errorf("fault injection enabled gauge: got %v, want 0", got)
	}
}

// TestFaults_Error: инжектированные ошибки видны как 5xx в HTTP-метриках
// (их и считает SLI) и отдельно в cubic_root_injected_faults_total.
func TestFaults_Error(t *testing.T) {
	h, reg, m := newTestHandler(apiConfig{Faults: faultConfig{ErrorRate: 1}})
	for range 3 {
		if rr := get(h, "/cubic-root?d=27"); rr.Code != http.StatusInternalServerError {
			t.Fatalf("status %d, want 500", rr.Code)
		}
	}
	if got := testutil.ToFloat64(m.injectedFaults.WithLabelValues("error")); got != 3 {
		t.Errorf("injected errors: got %v, want 3", got)
	}
	if got := testutil.ToFloat64(m.faultInjectionEnabled); got != 1 {
		t.Errorf("fault injection enabled gauge: got %v, want 1", got)
	}
	series := findMetric(t, reg, "http_server_request_duration_seconds", map[string]string{
		"http_request_method": "GET", "http_route": "/cubic-root", "http_response_status_code": "500",
	})
	if series == nil || series.GetHistogram().GetSampleCount() != 3 {
		t.Error("injected errors are not recorded as 500 in HTTP metrics")
	}
	if got := testutil.ToFloat64(m.calculations.WithLabelValues(string(outcomeConverged))); got != 0 {
		t.Errorf("a failed request still reached the computation: %v", got)
	}
}

// TestFaults_HealthIsUntouched: /health не должен падать, иначе в k8s начнут
// срабатывать пробы.
func TestFaults_HealthIsUntouched(t *testing.T) {
	h, _, _ := newTestHandler(apiConfig{Faults: faultConfig{ErrorRate: 1, Latency: time.Second, LatencyRate: 1}})
	start := time.Now()
	if rr := get(h, "/health"); rr.Code != http.StatusOK {
		t.Fatalf("/health: status %d", rr.Code)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Error("/health got injected latency")
	}
}

func TestFaults_Latency(t *testing.T) {
	const latency = 30 * time.Millisecond
	h, _, m := newTestHandler(apiConfig{Faults: faultConfig{Latency: latency, LatencyRate: 1}})

	start := time.Now()
	if rr := get(h, "/cubic-root?d=27"); rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	if elapsed := time.Since(start); elapsed < latency {
		t.Errorf("request took %v, want at least %v", elapsed, latency)
	}
	if got := testutil.ToFloat64(m.injectedFaults.WithLabelValues("latency")); got != 1 {
		t.Errorf("injected latency: got %v, want 1", got)
	}
}

// TestFaults_ClientGone: клиент, ушедший во время задержки, получает 499, а
// не 5xx, и не расходует бюджет ошибок.
func TestFaults_ClientGone(t *testing.T) {
	h, _, _ := newTestHandler(apiConfig{Faults: faultConfig{Latency: time.Minute, LatencyRate: 1}})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/cubic-root?d=27", nil).WithContext(ctx))

	if rr.Code != statusClientClosedRequest {
		t.Errorf("status %d, want %d", rr.Code, statusClientClosedRequest)
	}
}

func TestFaultConfig_Enabled(t *testing.T) {
	cases := []struct {
		cfg  faultConfig
		want bool
	}{
		{faultConfig{}, false},
		{faultConfig{ErrorRate: 0.01}, true},
		{faultConfig{Latency: time.Second}, false}, // задержка без доли запросов ничего не делает
		{faultConfig{LatencyRate: 1}, false},
		{faultConfig{Latency: time.Second, LatencyRate: 0.1}, true},
	}
	for _, tc := range cases {
		if got := tc.cfg.enabled(); got != tc.want {
			t.Errorf("%+v.enabled() = %v, want %v", tc.cfg, got, tc.want)
		}
	}
}
