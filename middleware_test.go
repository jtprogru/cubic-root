package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

// findMetric возвращает серию метрики name с точно такими labels или nil.
func findMetric(t *testing.T, g prometheus.Gatherer, name string, labels map[string]string) *dto.Metric {
	t.Helper()
	mfs, err := g.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if len(m.GetLabel()) != len(labels) {
				continue
			}
			match := true
			for _, lp := range m.GetLabel() {
				if labels[lp.GetName()] != lp.GetValue() {
					match = false
					break
				}
			}
			if match {
				return m
			}
		}
	}
	return nil
}

func TestInstrumentHTTP_Labels(t *testing.T) {
	cases := []struct {
		name, method, target  string
		wantMethod, wantRoute string
		wantCode              int
	}{
		{"ok", http.MethodGet, "/cubic-root?d=27", "GET", "/cubic-root", http.StatusOK},
		{"bad request", http.MethodGet, "/cubic-root?d=abc", "GET", "/cubic-root", http.StatusBadRequest},
		{"health", http.MethodGet, "/health", "GET", "/health", http.StatusOK},
		{"method not allowed", http.MethodPost, "/cubic-root?d=27", "POST", routeUnmatched, http.StatusMethodNotAllowed},
		{"not found", http.MethodGet, "/nope", "GET", routeUnmatched, http.StatusNotFound},
		{"unknown method", "BREW", "/cubic-root", methodOther, routeUnmatched, http.StatusMethodNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, reg, m := newTestHandler(testAPIConfig)

			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.target, nil))

			if rr.Code != tc.wantCode {
				t.Fatalf("status: got %d, want %d", rr.Code, tc.wantCode)
			}
			series := findMetric(t, reg, "http_server_request_duration_seconds", map[string]string{
				"http_request_method":       tc.wantMethod,
				"http_route":                tc.wantRoute,
				"http_response_status_code": strconv.Itoa(tc.wantCode),
			})
			if series == nil || series.GetHistogram().GetSampleCount() != 1 {
				t.Fatalf("expected one observation for method=%s route=%s code=%d", tc.wantMethod, tc.wantRoute, tc.wantCode)
			}
			if got := testutil.ToFloat64(m.activeRequests.WithLabelValues(tc.wantMethod)); got != 0 {
				t.Errorf("active requests after response: got %v, want 0", got)
			}
		})
	}
}

// TestInstrumentHTTP_UntrustedPattern проверяет, что в http_route не попадает
// паттерн, которого нет среди зарегистрированных маршрутов, даже если он
// оказался в r.Pattern.
func TestInstrumentHTTP_UntrustedPattern(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := newMetrics(reg)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Pattern = "/attacker/controlled"
		w.WriteHeader(http.StatusTemporaryRedirect)
	})

	rr := httptest.NewRecorder()
	instrumentHTTP(next, m, "GET /cubic-root").ServeHTTP(rr, httptest.NewRequest(http.MethodConnect, "/attacker/controlled", nil))

	if findMetric(t, reg, "http_server_response_body_size_bytes", map[string]string{"http_route": "/attacker/controlled"}) != nil {
		t.Fatal("untrusted pattern leaked into http_route")
	}
	if findMetric(t, reg, "http_server_response_body_size_bytes", map[string]string{"http_route": routeUnmatched}) == nil {
		t.Fatalf("expected request to be recorded as %q", routeUnmatched)
	}
}

func TestInstrumentHTTP_ResponseSize(t *testing.T) {
	h, reg, _ := newTestHandler(testAPIConfig)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/cubic-root?d=27", nil))
	body, _ := io.ReadAll(rr.Body)

	series := findMetric(t, reg, "http_server_response_body_size_bytes", map[string]string{"http_route": "/cubic-root"})
	if series == nil {
		t.Fatal("response size not recorded")
	}
	if got, want := series.GetHistogram().GetSampleSum(), float64(len(body)); got != want {
		t.Errorf("response size: got %v, want %v", got, want)
	}
}

// TestMetricsLint прогоняет метрики приложения через promlint: имена, единицы
// измерения и суффиксы должны соответствовать конвенциям Prometheus.
func TestMetricsLint(t *testing.T) {
	reg := newRegistry()
	h := newPublicHandler(newAPI(apiConfig{CacheSize: 10}, newMetrics(reg), reg))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/cubic-root?d=27", nil))

	problems, err := testutil.GatherAndLint(reg,
		"http_server_request_duration_seconds",
		"http_server_active_requests",
		"http_server_response_body_size_bytes",
		"cubic_root_build_info",
		"cubic_root_calculations_total",
		"cubic_root_newton_iterations",
		"cubic_root_result_relative_error",
		"cubic_root_input_magnitude_log10",
		"cubic_root_validation_errors_total",
		"cubic_root_cache_requests_total",
		"cubic_root_cache_evictions_total",
		"cubic_root_cache_hit_age_seconds",
		"cubic_root_cache_coalesced_total",
		"cubic_root_cache_entries",
		"cubic_root_cache_capacity_entries",
		"cubic_root_injected_faults_total",
		"cubic_root_fault_injection_enabled",
	)
	if err != nil {
		t.Fatalf("lint: %v", err)
	}
	for _, p := range problems {
		t.Errorf("%s: %s", p.Metric, p.Text)
	}
}

func TestMetricsHandler(t *testing.T) {
	reg := newRegistry()
	h := newPublicHandler(newAPI(apiConfig{CacheSize: 10}, newMetrics(reg), reg))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/cubic-root?d=27", nil))

	rr := httptest.NewRecorder()
	newMetricsHandler(reg).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	for _, want := range []string{
		"http_server_request_duration_seconds_bucket",
		"go_sched_latencies_seconds_bucket",
		"go_sync_mutex_wait_total_seconds_total",
		"go_cpu_classes_gc_total_cpu_seconds_total",
		"cubic_root_build_info",
		"promhttp_metric_handler_requests_total",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics has no %s", want)
		}
	}
}
