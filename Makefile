# FlashDeal Service developer commands. Recipes avoid shell-specific syntax so
# they run the same with GNU make on Linux, macOS and Windows.

COMPOSE ?= docker compose
GO      ?= go

.DEFAULT_GOAL := check

REDOCLY_IMAGE ?= redocly/cli:2.71.1
K6_IMAGE      ?= grafana/k6:2.3.0
# k6 joins the Compose network and calls the api service directly.
K6_NETWORK    ?= flashdeal_default
LOAD_SUMMARY  ?= test/load/results/latest.json

.PHONY: check up down migrate test test-integration test-e2e lint lint-api load build tidy

## check: lint and unit tests (required before declaring a task done)
check: lint test

## up: start the local stack and wait until dependencies are healthy
up:
	$(COMPOSE) up -d --build --wait

## down: stop the local stack (data volumes are kept)
down:
	$(COMPOSE) down

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
		-e STOCK -e PEAK_RPS -e RAMP -e HOLD -e BASE_URL \
		$(K6_IMAGE) run --summary-export=/load/results/$(notdir $(LOAD_SUMMARY)) /load/flash_sale.js

## build: compile the API binary into bin/
build:
	$(GO) build -trimpath -o bin/ ./cmd/api

## tidy: sync go.mod and go.sum with the imports
tidy:
	$(GO) mod tidy
