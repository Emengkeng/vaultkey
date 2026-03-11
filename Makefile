BINARY     := vaultkey
BUILD_DIR  := bin
COVERAGE   := coverage.out
GO         := go

# Packages
UNIT_PKGS  := ./internal/wallet/... ./internal/webhook/...
INT_PKGS   := ./internal/nonce/... ./internal/queue/... ./internal/storage/...

.PHONY: all build test test-unit test-integration lint coverage coverage-ci clean help
.PHONY: fmt docker-up docker-down docker-logs dev deps install-tools migrate-up migrate-down

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

## fmt: format code with gofmt
fmt:
	$(GO) fmt ./...
	gofmt -s -w .

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

## docker-up: start Docker services (Postgres, Redis, Vault)
docker-up:
	@echo "Starting Docker services..."
	docker compose up -d
	@echo "Waiting for services to initialize..."
	@sleep 5

## docker-down: stop Docker services
docker-down:
	docker compose down

## docker-logs: view Docker service logs
docker-logs:
	docker compose logs -f

## migrate-up: run database migrations
migrate-up:
	@echo "Running database migrations..."
	$(GO) run cmd/migrate/main.go up

## migrate-down: rollback database migrations
migrate-down:
	@echo "Rolling back database migrations..."
	$(GO) run cmd/migrate/main.go down

## migrate-create: create a new migration (usage: make migrate-create name=add_users_table)
migrate-create:
	@if [ -z "$(name)" ]; then \
		echo "Error: name is required. Usage: make migrate-create name=add_users_table"; \
		exit 1; \
	fi
	@echo "Creating migration: $(name)"
	migrate create -ext sql -dir migrations -seq $(name)

## dev: setup development environment (Docker + migrations)
dev: docker-up migrate-up
	@echo "Development environment ready!"
	@echo "Run 'make run' to start the server"

## run: start the VaultKey server
run:
	@echo "Starting VaultKey server..."
	$(GO) run ./cmd/server

## deps: download and tidy dependencies
deps:
	@echo "Downloading dependencies..."
	$(GO) mod download
	$(GO) mod tidy

## install-tools: install development tools (golangci-lint, migrate)
install-tools:
	@echo "Installing development tools..."
	@$(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	@$(GO) install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
	@echo "Tools installed"

## help: print available targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //' | column -t -s ':'