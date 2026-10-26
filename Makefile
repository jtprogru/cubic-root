BASE_URL  ?= http://localhost:8080
K6_SCRIPT ?= k6/load.js
SCENARIO  ?= baseline

# Client-side k6 metrics are pushed to VictoriaMetrics for the Load test dashboard,
# one testid per run. K6_OUT= disables it, e.g. against a stack without VictoriaMetrics.
K6_OUT    ?= experimental-prometheus-rw
K6_RW_URL ?= http://localhost:8428/api/v1/write
K6_TESTID ?= $(SCENARIO)-$(shell date +%Y%m%d-%H%M%S)
K6_ENV     = K6_PROMETHEUS_RW_SERVER_URL=$(K6_RW_URL) K6_PROMETHEUS_RW_PUSH_INTERVAL=5s \
	K6_PROMETHEUS_RW_TREND_STATS='p(50),p(95),p(99),max'
# Profile parameters are forwarded only when set, e.g. make test-load SCENARIO=soak RATE=2000 DURATION=2h.
# FAST=1 reads no response bodies and checks only statuses: k6 spends less CPU per request.
K6_PARAMS  = MIX RATE DURATION VUS THINK_TIME SPIKE_RATE PERIOD STEP MAX_RATE MAX_VUS FAST
# Extra k6 flags, e.g. K6_ARGS=--quiet for unattended runs: without a terminal k6
# prints a progress line per scenario every second.
K6_ARGS   ?=
K6_FLAGS   = --env BASE_URL=$(BASE_URL) --env SCENARIO=$(SCENARIO) --tag testid=$(K6_TESTID) \
	$(foreach p,$(K6_PARAMS),$(if $($(p)),--env $(p)=$($(p)))) $(if $(K6_OUT),--out $(K6_OUT)) $(K6_ARGS)

# Images used to validate observability configs; keep in sync with compose.yaml.
VM_VERSION   ?= v1.154.0
VECTOR_IMAGE ?= timberio/vector:0.59.0-alpine

# Validation of rendered Helm manifests, VictoriaMetrics Operator CRDs included.
KUBECONFORM_IMAGE ?= ghcr.io/yannh/kubeconform:v0.8.0
CRD_SCHEMAS       ?= https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json
IMAGE     ?= ghcr.io/jtprogru/cubic-root:latest

.DEFAULT_GOAL := help

.PHONY: help deps-install tidy deps-update build-bin build-docker push-docker tests test-unit \
	test-bench run-bin check-k6 test-load test-load-smoke test-load-demo run-traces run-traced obs-up obs-down obs-check chart-check

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

test-load: check-k6 ## Run a k6 profile: SCENARIO=smoke|demo|load|stress|spike|soak|waves|breakpoint|marathon|baseline, MIX=...
	$(K6_ENV) k6 run $(K6_FLAGS) $(K6_SCRIPT)

test-load-smoke: check-k6 ## Smoke test: 30s of mixed traffic at 50 req/s with strict thresholds
	$(MAKE) --no-print-directory test-load SCENARIO=smoke

test-load-demo: check-k6 ## ~3m that light up every dashboard: warm-up, steady load, spike, recovery
	$(MAKE) --no-print-directory test-load SCENARIO=demo

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
	docker run --rm -v "$(CURDIR):/repo:ro" victoriametrics/vmalert:$(VM_VERSION) \
		-rule='/repo/observability/vmalert/rules/*.yml' -rule='/repo/chart/files/vmrules/*.yml' -dryRun -loggerLevel=ERROR
	docker run --rm -v "$(CURDIR):/repo:ro" victoriametrics/vmalert-tool:$(VM_VERSION) \
		unittest --files='/repo/observability/vmalert/tests/*.yml'
	docker run --rm -v "$(CURDIR)/observability/vector/vector.yaml:/etc/vector/vector.yaml:ro" $(VECTOR_IMAGE) \
		validate --no-environment /etc/vector/vector.yaml

chart-check: ## Lint the Helm chart and validate rendered manifests with every feature enabled
	helm lint chart
	helm template cubic-root chart \
		--set monitoring.vmServiceScrape.enabled=true --set monitoring.vmRule.enabled=true \
		--set networkPolicy.enabled=true --set ingress.enabled=true --set autoscaling.enabled=true \
		| docker run --rm -i $(KUBECONFORM_IMAGE) -strict -summary \
			-schema-location default -schema-location '$(CRD_SCHEMAS)' -

obs-down: ## Stop observability stack
	docker compose down
