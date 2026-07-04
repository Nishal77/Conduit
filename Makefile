.PHONY: build test test-race test-int test-e2e test-load lint run docker-build docker-run \
	migrate-up migrate-down generate release fuzz security clean

BINARY      := bin/conduit
MODULE      := github.com/conduit-oss/conduit
VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILT       := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.built=$(BUILT)

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

test-load: ## k6 load test against a running stack — set BASE_URL/API_KEY/TENANT/SERVER
	k6 run k6/load-test.js

docker-build: ## Build the Conduit Docker image
	docker build -f docker/Dockerfile --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILT=$(BUILT) -t conduit:$(VERSION) .

docker-run: ## docker compose up -d (postgres, redis, migrate, conduit)
	docker compose -f docker/docker-compose.yml up -d

migrate-up: ## Apply pending migrations to $DATABASE_URL
	go run ./cmd/conduit migrate --db-url "$(DATABASE_URL)"

migrate-down: ## Rollback the last migration
	go run ./cmd/conduit migrate --down --db-url "$(DATABASE_URL)"

generate: ## go generate (mocks, swaggo) — added in later phases
	go generate ./...

release: ## GoReleaser dry-run (added in Phase 7)
	@echo "no .goreleaser.yaml yet (Phase 7)"

security: ## govulncheck + trivy
	govulncheck ./...

clean:
	rm -rf bin/
