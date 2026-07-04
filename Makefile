.PHONY: build test test-race test-int test-e2e test-load lint run docker-build docker-run \
	migrate-up migrate-down generate release fuzz security clean

BINARY      := bin/conduit
MODULE      := github.com/conduit-oss/conduit
VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -X main.version=$(VERSION)

build: ## Build the conduit binary to ./bin/conduit
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/conduit

run: build ## Run the proxy with ./conduit.yaml
	./$(BINARY) proxy start --config conduit.yaml

test: ## Run unit tests
	go test ./...

test-race: ## Run unit tests with the race detector
	go test -race ./...

fuzz: ## Run the MCP parser fuzz test for 60 seconds
	go test ./internal/mcp/ -fuzz=FuzzParseMessage -fuzztime=60s

lint: ## Run golangci-lint
	golangci-lint run ./...

test-int: ## Integration tests against real PostgreSQL + Redis
	## Uses testcontainers-go (requires Docker) unless TEST_DATABASE_URL /
	## TEST_REDIS_URL point at already-running instances.
	go test -tags=integration ./...

test-e2e: ## Playwright E2E tests (added in Phase 4)
	@echo "no e2e suite yet (Phase 4)"

test-load: ## k6 load test against a running stack (added in Phase 3)
	@echo "no k6 suite yet (Phase 3)"

docker-build: ## Build the Conduit Docker image (added in Phase 3)
	@echo "no Dockerfile yet (Phase 3)"

docker-run: ## docker compose up -d (added in Phase 3)
	@echo "no docker-compose.yml yet (Phase 3)"

migrate-up: ## Apply pending migrations to $DATABASE_URL
	go run ./cmd/conduit migrate --db-url "$(DATABASE_URL)"

migrate-down: ## Rollback the last migration (added in Phase 3's full CLI)
	@echo "conduit migrate only applies Up() today; Down() lands with the rest of the CLI in Phase 3"

generate: ## go generate (mocks, swaggo) — added in later phases
	go generate ./...

release: ## GoReleaser dry-run (added in Phase 7)
	@echo "no .goreleaser.yaml yet (Phase 7)"

security: ## govulncheck + trivy
	govulncheck ./...

clean:
	rm -rf bin/
