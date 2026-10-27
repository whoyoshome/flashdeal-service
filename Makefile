# FlashDeal Service developer commands. Recipes avoid shell-specific syntax so
# they run the same with GNU make on Linux, macOS and Windows.

COMPOSE ?= docker compose
GO      ?= go

# Git Bash on Windows rewrites arguments that look like POSIX paths (the
# /spec and /load mount points below) into Windows paths. Elsewhere it is
# ignored.
export MSYS_NO_PATHCONV := 1

.DEFAULT_GOAL := check

REDOCLY_IMAGE ?= redocly/cli:2.71.1
K6_IMAGE      ?= grafana/k6:2.3.0
# k6 joins the Compose network and calls the api service directly.
K6_NETWORK    ?= flashdeal_default
LOAD_SUMMARY  ?= test/load/results/latest.json
# Compose profiles started by `make up`. CI clears it (make up PROFILES=) to
# run only the API and its stores.
PROFILES      ?= --profile observability

.PHONY: check up down migrate demo test test-integration test-e2e test-chaos lint lint-api load build tidy

## check: lint and unit tests (required before declaring a task done)
check: lint test

## demo: start everything, run a short flash sale and print where to watch it
demo: up migrate
	docker run --rm --network $(K6_NETWORK) -v "$(CURDIR)/test/load:/load" \
		-e STOCK=1000 -e PEAK_RPS=2000 -e RAMP=2s -e HOLD=10s -e THRESHOLDS=correctness \
		$(K6_IMAGE) run /load/flash_sale.js
# Plain echo text, no quotes or shell metacharacters: make may run recipes
# with sh or with cmd.exe, which would print quotes literally.
	@echo A deal of 1000 units just sold out to about 20000 buyers, none oversold.
	@echo Dashboard: http://localhost:3000 - FlashDeal dashboard, last 5 minutes
	@echo Traces:    http://localhost:16686 - service flashdeal-api
	@echo Run it again with make demo, stop everything with make down.

## up: start the local stack (API, stores, Jaeger, Prometheus, Grafana) and wait until healthy
up:
	$(COMPOSE) $(PROFILES) up -d --build --wait

## down: stop the local stack (data volumes are kept)
down:
	$(COMPOSE) --profile observability down

## migrate: apply database migrations with the golang-migrate container
migrate:
	$(COMPOSE) --profile tools run --rm migrate

# The race detector needs cgo (and a C compiler on Windows).
test test-integration: export CGO_ENABLED := 1

## test: unit tests with the race detector
test:
	$(GO) test -race -count=1 ./...

## test-integration: integration tests against real Postgres and Redis
test-integration:
	$(GO) test -race -count=1 -tags=integration ./...

## test-e2e: end-to-end tests over HTTP against the running stack (make up + make migrate first)
test-e2e:
	$(GO) test -count=1 -tags=e2e ./test/e2e/...

## test-chaos: stop Redis and the API under load and check the service degrades safely (make up + make migrate first)
test-chaos:
	$(GO) test -count=1 -v -timeout=10m -tags=chaos ./test/chaos/...

## lint: static analysis, layer rules (depguard) and known vulnerabilities
lint:
	golangci-lint run ./...
	govulncheck ./...

## lint-api: validate api/openapi.yaml with Redocly (runs in Docker)
lint-api:
	docker run --rm -v "$(CURDIR):/spec" -w /spec $(REDOCLY_IMAGE) lint api/openapi.yaml

## load: k6 flash-sale scenario against the running stack (make up + make migrate first)
load:
	docker run --rm --network $(K6_NETWORK) -v "$(CURDIR)/test/load:/load" \
		-e STOCK -e PEAK_RPS -e RAMP -e HOLD -e BASE_URL -e THRESHOLDS \
		$(K6_IMAGE) run --summary-export=/load/results/$(notdir $(LOAD_SUMMARY)) /load/flash_sale.js

## build: compile the API binary into bin/
build:
	$(GO) build -trimpath -o bin/ ./cmd/api

## tidy: sync go.mod and go.sum with the imports
tidy:
	$(GO) mod tidy
