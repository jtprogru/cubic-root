package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// config — настройки сервиса из переменных окружения.
type config struct {
	Port        int
	MetricsPort int
	Debug       bool
	API         apiConfig
}

// apiConfig — настройки обработчика /cubic-root.
type apiConfig struct {
	// CacheSize — сколько результатов держать в кэше; 0 выключает кэш.
	CacheSize int
	// CacheTTL — сколько живёт запись кэша; 0 — без устаревания.
	CacheTTL time.Duration
	// CalcDelay и CalcDelayJitter — искусственная задержка вычисления (только
	// на промахе кэша) с равномерным разбросом ±jitter. Имитирует дорогой
	// расчёт, чтобы эффект кэша был виден на метриках.
	CalcDelay       time.Duration
	CalcDelayJitter time.Duration
}

// loadConfig читает конфигурацию и возвращает все ошибки разом, чтобы
// невалидные значения не всплывали по одному при каждом перезапуске.
func loadConfig() (config, error) {
	var p envParser
	cfg := config{
		Port:        p.port("PORT", 8080),
		MetricsPort: p.port("METRICS_PORT", 9090),
		// Невалидное или пустое значение DEBUG означает выключенный debug-режим.
		Debug: p.boolOrFalse("DEBUG"),
		API: apiConfig{
			CacheSize:       p.nonNegativeInt("CACHE_SIZE", 10000),
			CacheTTL:        p.nonNegativeDuration("CACHE_TTL", 5*time.Minute),
			CalcDelay:       p.nonNegativeDuration("CALC_DELAY", 0),
			CalcDelayJitter: p.nonNegativeDuration("CALC_DELAY_JITTER", 0),
		},
	}
	return cfg, errors.Join(p.errs...)
}

// envParser читает переменные окружения и копит ошибки.
type envParser struct {
	errs []error
}

func (p *envParser) fail(name, value, reason string) {
	p.errs = append(p.errs, fmt.Errorf("%s=%q: %s", name, value, reason))
}

func (p *envParser) port(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 65535 {
		p.fail(name, v, "must be a port number 1-65535")
		return def
	}
	return n
}

func (p *envParser) nonNegativeInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		p.fail(name, v, "must be a non-negative integer")
		return def
	}
	return n
}

func (p *envParser) nonNegativeDuration(name string, def time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		p.fail(name, v, "must be a non-negative duration like 250ms or 5m")
		return def
	}
	return d
}

func (p *envParser) boolOrFalse(name string) bool {
	b, _ := strconv.ParseBool(os.Getenv(name))
	return b
}
