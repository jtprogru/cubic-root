BASE_URL  ?= http://localhost:8080
K6_SCRIPT ?= k6/load.js
IMAGE     ?= ghcr.io/jtprogru/cubic-root:latest

.DEFAULT_GOAL := help

.PHONY: help deps-install tidy deps-update build-bin build-docker push-docker tests test-unit \
	test-bench run-bin check-k6 test-load test-load-smoke run-tempo run-traced obs-up obs-down

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

deps-install: ## Download Go module dependencies
	go mod download

tidy: ## Sync go.mod and go.sum with the code
	go mod tidy

deps-update: ## Update all Go dependencies to the latest minor/patch versions
	go get -u -t ./...
	go mod tidy

build-bin: ## Build binary
	go build -o bin/cubic-root main.go

build-docker: ## Build docker image
	docker build -t $(IMAGE) . --build-arg http_port=8080 --build-arg debug_mode=true

push-docker: ## Push docker image
	docker push $(IMAGE)

tests: ## Run all tests
	@echo "Running unit tests..."
	@$(MAKE) --no-print-directory test-unit
	@echo "Running benchmarks..."
	@$(MAKE) --no-print-directory test-bench

test-unit: ## Run unit tests
	go test -v -timeout 30s ./...

test-bench: ## Run benchmarks
	go test -bench=. -run='^$$' -v

run-bin: build-bin ## Run binary
	PORT=8080 DEBUG=true ./bin/cubic-root

check-k6:
	@command -v k6 >/dev/null 2>&1 || { echo "k6 is not installed: brew install k6"; exit 1; }

test-load: check-k6 ## Run load test using k6 (stages: ramp-up 30s → load 240s → ramp-down 30s)
	k6 run --env BASE_URL=$(BASE_URL) $(K6_SCRIPT)

test-load-smoke: check-k6 ## Smoke test: 10 VU, 10s — быстрая проверка что сервис отвечает
	k6 run --vus 10 --duration 10s --env BASE_URL=$(BASE_URL) $(K6_SCRIPT)

run-tempo: ## Start Tempo for local tracing (OTLP HTTP :4318, API :3200)
	docker compose up tempo -d

run-traced: build-bin ## Run binary with tracing enabled (requires Tempo: make run-tempo)
	PORT=8080 DEBUG=true OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 OTEL_EXPORTER_OTLP_INSECURE=true ./bin/cubic-root

obs-up: ## Start full observability stack (Tempo + Loki + Promtail + Prometheus + Grafana)
	docker compose up -d --build
	@echo "cubic-root  → http://localhost:8080"
	@echo "Grafana     → http://localhost:3333 (по умолчанию admin/admin, переопределяется через GRAFANA_ADMIN_USER и GRAFANA_ADMIN_PASSWORD)"
	@echo "Prometheus  → http://localhost:9090"
	@echo "Tempo API   → http://localhost:3200"
	@echo "Loki API    → http://localhost:3100"

obs-down: ## Stop observability stack
	docker compose down
