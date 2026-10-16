package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func get(h http.Handler, target string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, target, nil))
	return rr
}

// TestCache_HitMiss проверяет, что одно и то же число в разной записи
// вычисляется один раз, а повторы отдаются из кэша.
func TestCache_HitMiss(t *testing.T) {
	h, reg, m := newTestHandler(apiConfig{CacheSize: 10, CacheTTL: time.Minute})

	want := []string{"MISS", "HIT", "HIT"}
	for i, d := range []string{"27", "27.0", "2.7e1"} {
		rr := get(h, "/cubic-root?d="+d)
		if rr.Code != http.StatusOK {
			t.Fatalf("d=%s: status %d", d, rr.Code)
		}
		if got := rr.Header().Get("X-Cache"); got != want[i] {
			t.Errorf("d=%s: X-Cache=%q, want %q", d, got, want[i])
		}
		if resp := decodeResponse(t, rr); resp.Result != 3 {
			t.Errorf("d=%s: result %v", d, resp.Result)
		}
	}

	if got := testutil.ToFloat64(m.calculations.WithLabelValues(string(outcomeConverged))); got != 1 {
		t.Errorf("calculations: got %v, want 1", got)
	}
	if hits, misses := testutil.ToFloat64(m.cacheRequests.WithLabelValues("hit")), testutil.ToFloat64(m.cacheRequests.WithLabelValues("miss")); hits != 2 || misses != 1 {
		t.Errorf("cache requests: hits=%v misses=%v, want 2 and 1", hits, misses)
	}
	if got := findMetric(t, reg, "cubic_root_cache_entries", map[string]string{}).GetGauge().GetValue(); got != 1 {
		t.Errorf("cache entries: got %v, want 1", got)
	}
	if got := findMetric(t, reg, "cubic_root_cache_capacity_entries", map[string]string{}).GetGauge().GetValue(); got != 10 {
		t.Errorf("cache capacity: got %v, want 10", got)
	}
}

func TestCache_NegativeZero(t *testing.T) {
	h, _, _ := newTestHandler(apiConfig{CacheSize: 10})
	get(h, "/cubic-root?d=0")
	if got := get(h, "/cubic-root?d=-0").Header().Get("X-Cache"); got != "HIT" {
		t.Errorf("-0 should share the cache entry with 0, X-Cache=%q", got)
	}
}

func TestCache_Disabled(t *testing.T) {
	h, reg, m := newTestHandler(apiConfig{})
	for range 2 {
		if rr := get(h, "/cubic-root?d=27"); rr.Header().Get("X-Cache") != "" {
			t.Errorf("X-Cache is set with the cache disabled: %q", rr.Header().Get("X-Cache"))
		}
	}
	if got := testutil.ToFloat64(m.calculations.WithLabelValues(string(outcomeConverged))); got != 2 {
		t.Errorf("calculations: got %v, want 2", got)
	}
	if got := findMetric(t, reg, "cubic_root_cache_capacity_entries", map[string]string{}).GetGauge().GetValue(); got != 0 {
		t.Errorf("cache capacity: got %v, want 0", got)
	}
}

// TestCache_FailedResultsAreNotCached: результат, которому нельзя доверять,
// не должен закрепляться в кэше.
func TestCache_FailedResultsAreNotCached(t *testing.T) {
	h, _, m := newTestHandlerWithCalc(apiConfig{CacheSize: 10}, notConverging)
	for range 2 {
		if rr := get(h, "/cubic-root?d=1e30"); rr.Code != http.StatusInternalServerError {
			t.Errorf("status %d, want 500", rr.Code)
		}
	}
	if got := testutil.ToFloat64(m.calculations.WithLabelValues(string(outcomeMaxIter))); got != 2 {
		t.Errorf("max_iter calculations: got %v, want 2", got)
	}
}

// TestCache_Coalescing: одновременные промахи по одному ключу ждут одно
// вычисление, а не запускают каждый своё.
func TestCache_Coalescing(t *testing.T) {
	const n = 10
	h, _, m := newTestHandler(apiConfig{CacheSize: 10, CalcDelay: 200 * time.Millisecond})

	start := make(chan struct{})
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			<-start
			if rr := get(h, "/cubic-root?d=27"); rr.Code != http.StatusOK {
				t.Errorf("status %d", rr.Code)
			}
		})
	}
	close(start)
	wg.Wait()

	if got := testutil.ToFloat64(m.calculations.WithLabelValues(string(outcomeConverged))); got != 1 {
		t.Errorf("calculations: got %v, want 1", got)
	}
	// Опоздавший запрос может попасть уже в кэш, а не в ожидание: вместе их n-1.
	coalesced := testutil.ToFloat64(m.cacheCoalesced)
	hits := testutil.ToFloat64(m.cacheRequests.WithLabelValues("hit"))
	if coalesced+hits != n-1 || coalesced == 0 {
		t.Errorf("coalesced=%v hits=%v, want coalesced > 0 and coalesced+hits=%d", coalesced, hits, n-1)
	}
}

func TestCalcDelay(t *testing.T) {
	a := &api{cfg: apiConfig{CalcDelay: 10 * time.Millisecond, CalcDelayJitter: 5 * time.Millisecond}}
	for range 100 {
		if d := a.calcDelay(); d < 5*time.Millisecond || d > 15*time.Millisecond {
			t.Fatalf("calcDelay out of [5ms, 15ms]: %v", d)
		}
	}
	a.cfg = apiConfig{CalcDelay: time.Millisecond, CalcDelayJitter: 10 * time.Millisecond}
	for range 100 {
		if d := a.calcDelay(); d < 0 {
			t.Fatalf("negative delay: %v", d)
		}
	}
}
