package main

import (
	"strings"
	"testing"
	"time"
)

func TestLoadConfig_Defaults(t *testing.T) {
	for _, name := range []string{"PORT", "METRICS_PORT", "DEBUG", "CACHE_SIZE", "CACHE_TTL", "CALC_DELAY", "CALC_DELAY_JITTER"} {
		t.Setenv(name, "")
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	want := config{
		Port: 8080, MetricsPort: 9090,
		API: apiConfig{CacheSize: 10000, CacheTTL: 5 * time.Minute},
	}
	if cfg != want {
		t.Errorf("defaults: got %+v, want %+v", cfg, want)
	}
}

func TestLoadConfig_Values(t *testing.T) {
	t.Setenv("PORT", "8081")
	t.Setenv("METRICS_PORT", "9091")
	t.Setenv("DEBUG", "true")
	t.Setenv("CACHE_SIZE", "0")
	t.Setenv("CACHE_TTL", "30s")
	t.Setenv("CALC_DELAY", "2ms")
	t.Setenv("CALC_DELAY_JITTER", "500us")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	want := config{
		Port: 8081, MetricsPort: 9091, Debug: true,
		API: apiConfig{CacheTTL: 30 * time.Second, CalcDelay: 2 * time.Millisecond, CalcDelayJitter: 500 * time.Microsecond},
	}
	if cfg != want {
		t.Errorf("got %+v, want %+v", cfg, want)
	}
}

func TestLoadConfig_Invalid(t *testing.T) {
	t.Setenv("PORT", "http")
	t.Setenv("METRICS_PORT", "70000")
	t.Setenv("CACHE_SIZE", "-1")
	t.Setenv("CACHE_TTL", "5")
	t.Setenv("CALC_DELAY", "-1ms")
	t.Setenv("CALC_DELAY_JITTER", "")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, name := range []string{"PORT", "METRICS_PORT", "CACHE_SIZE", "CACHE_TTL", "CALC_DELAY"} {
		if !strings.Contains(err.Error(), name+"=") {
			t.Errorf("error does not mention %s: %v", name, err)
		}
	}
}
