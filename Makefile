BINARY     := vaultkey
BUILD_DIR  := bin
COVERAGE   := coverage.out
GO         := go

# Packages
UNIT_PKGS  := ./internal/wallet/... ./internal/webhook/...
INT_PKGS   := ./internal/nonce/... ./internal/queue/... ./internal/storage/...

.PHONY: all build test test-unit test-integration lint coverage clean help

## all: build the binary (default target)
all: build

## build: compile the server binary
build:
	@mkdir -p $(BUILD_DIR)
	$(GO) build -o $(BUILD_DIR)/$(BINARY) ./cmd/server

## test: run unit tests then integration tests
test: test-unit test-integration

## test-unit: run unit tests (no Docker required, race detector on)
test-unit:
	$(GO) test -race -count=1 -v $(UNIT_PKGS)

## test-integration: run integration tests (requires Docker, race detector on)
test-integration:
	$(GO) test -race -count=1 -v -timeout 120s $(INT_PKGS)

## lint: run golangci-lint (install: https://golangci-lint.run/usage/install)
lint:
	golangci-lint run ./...

## coverage: generate HTML coverage report and open it
coverage:
	$(GO) test -coverprofile=$(COVERAGE) -covermode=atomic ./...
	$(GO) tool cover -html=$(COVERAGE)

## coverage-ci: generate coverage report without opening browser (for CI)
coverage-ci:
	$(GO) test -coverprofile=$(COVERAGE) -covermode=atomic ./...
	$(GO) tool cover -func=$(COVERAGE)

## clean: remove build artifacts and coverage output
clean:
	rm -rf $(BUILD_DIR) $(COVERAGE)

## help: print available targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //' | column -t -s ':'