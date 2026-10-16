BASE_URL  ?= http://localhost:8080
K6_SCRIPT ?= k6/load.js
SCENARIO  ?= baseline

# Images used to validate observability configs; keep in sync with compose.yaml.
VM_VERSION   ?= v1.154.0
VECTOR_IMAGE ?= timberio/vector:0.59.0-alpine
IMAGE     ?= ghcr.io/jtprogru/cubic-root:latest

.DEFAULT_GOAL := help

.PHONY: help deps-install tidy deps-update build-bin build-docker push-docker tests test-unit \
	test-bench run-bin check-k6 test-load test-load-smoke run-traces run-traced obs-up obs-down obs-check

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

test-load: check-k6 ## Run k6 load test, SCENARIO=baseline|wide-range|bad-requests|hot-keys|spike (see k6/load.js)
	k6 run --env BASE_URL=$(BASE_URL) --env SCENARIO=$(SCENARIO) $(K6_SCRIPT)

test-load-smoke: check-k6 ## Smoke test: 10 VU, 10s — быстрая проверка что сервис отвечает
	k6 run --vus 10 --duration 10s --env BASE_URL=$(BASE_URL) --env SCENARIO=$(SCENARIO) $(K6_SCRIPT)

run-traces: ## Start VictoriaTraces for local tracing (OTLP HTTP and UI on :10428)
	docker compose up victoriatraces -d

run-traced: build-bin ## Run binary with tracing enabled (requires VictoriaTraces: make run-traces)
	PORT=8080 DEBUG=true OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://localhost:10428/insert/opentelemetry/v1/traces ./bin/cubic-root

obs-up: ## Start full observability stack (VictoriaMetrics + vmagent + vmalert + VictoriaLogs + Vector + VictoriaTraces + Grafana)
	docker compose up -d --build
	@echo "cubic-root      → http://localhost:8080"
	@echo "Grafana         → http://localhost:3333 (по умолчанию admin/admin, переопределяется через GRAFANA_ADMIN_USER и GRAFANA_ADMIN_PASSWORD)"
	@echo "VictoriaMetrics → http://localhost:8428/vmui"
	@echo "vmagent         → http://localhost:8429/targets"
	@echo "vmalert         → http://localhost:8880"
	@echo "VictoriaLogs    → http://localhost:9428/select/vmui"
	@echo "VictoriaTraces  → http://localhost:10428/select/vmui"

obs-check: ## Validate observability configs: compose, vmalert rules and their unit tests, Vector
	docker compose config --quiet
	docker run --rm -v "$(CURDIR)/observability/vmalert:/vmalert:ro" victoriametrics/vmalert:$(VM_VERSION) \
		-rule='/vmalert/rules/*.yml' -dryRun -loggerLevel=ERROR
	docker run --rm -v "$(CURDIR)/observability/vmalert:/vmalert:ro" victoriametrics/vmalert-tool:$(VM_VERSION) \
		unittest --files='/vmalert/tests/*.yml'
	docker run --rm -v "$(CURDIR)/observability/vector/vector.yaml:/etc/vector/vector.yaml:ro" $(VECTOR_IMAGE) \
		validate --no-environment /etc/vector/vector.yaml

obs-down: ## Stop observability stack
	docker compose down
