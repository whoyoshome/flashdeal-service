# FlashDeal Service developer commands. Recipes avoid shell-specific syntax so
# they run the same with GNU make on Linux, macOS and Windows.

COMPOSE ?= docker compose
GO      ?= go

.DEFAULT_GOAL := check

.PHONY: check up down migrate test test-integration lint load build tidy

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

## lint: static analysis, layer rules (depguard) and known vulnerabilities
lint:
	golangci-lint run ./...
	govulncheck ./...

## load: k6 flash-sale scenario (arrives in phase 3)
load:
	$(error the k6 scenario in test/load is not implemented yet; it arrives in phase 3)

## build: compile the API binary into bin/
build:
	$(GO) build -trimpath -o bin/ ./cmd/api

## tidy: sync go.mod and go.sum with the imports
tidy:
	$(GO) mod tidy
