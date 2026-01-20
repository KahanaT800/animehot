# Makefile
# IP Liquidity Analyzer - Build & Deploy

.PHONY: help build test lint clean docker-build docker-up docker-down docker-logs \
        full-test full-test-quick full-test-skip-build

# Default target
help:
	@echo "Available targets:"
	@echo "  build         - Build all Go binaries"
	@echo "  test          - Run all tests"
	@echo "  lint          - Run linter"
	@echo "  clean         - Clean build artifacts"
	@echo ""
	@echo "Docker (Full Stack ~1.7GB RAM):"
	@echo "  docker-build  - Build Docker images"
	@echo "  docker-up     - Start all services"
	@echo "  docker-down   - Stop all services"
	@echo "  docker-logs   - Show service logs"
	@echo ""
	@echo "Docker Light (no Crawler):"
	@echo "  docker-light-up   - Start MySQL + Redis + Analyzer only"
	@echo "  docker-light-down - Stop light services"
	@echo "  docker-light-logs - Show light service logs"
	@echo ""
	@echo "Docker with Monitoring (Grafana Cloud):"
	@echo "  docker-up-monitoring   - Start all + monitoring exporters"
	@echo "  docker-down-monitoring - Stop all + monitoring"
	@echo ""
	@echo "Development:"
	@echo "  dev-deps      - Start MySQL & Redis only"
	@echo "  dev-analyzer  - Run analyzer locally"
	@echo "  dev-crawler   - Run crawler locally"
	@echo "  dev-web       - Run analyzer with web UI (http://localhost:8080)"
	@echo ""
	@echo "Data Import (via API):"
	@echo "  api-import-run FILE=x     - Import via API (推荐)"
	@echo "  api-import-run FILE=x API=http://host:8080 - Import to remote"
	@echo ""
	@echo "Grayscale Testing:"
	@echo "  grayscale-start       - Start full stack + import test IPs"
	@echo "  grayscale-start-light - Start light stack (no crawler)"
	@echo "  grayscale-verify      - Verify data flow via API"
	@echo "  grayscale-check-redis - Check Redis queue status"
	@echo "  grayscale-check-mysql - Check MySQL data"
	@echo "  grayscale-trigger     - Manually trigger crawl"
	@echo "  grayscale-stop        - Stop services (keep data)"
	@echo "  grayscale-clean       - Stop + remove all data"
	@echo ""
	@echo "Full Test:"
	@echo "  full-test             - Run full automated test (build → start → verify)"
	@echo "  full-test-quick       - Quick test (skip build, shorter wait)"

# =============================================================================
# Build
# =============================================================================

build:
	@echo "Building binaries..."
	@mkdir -p bin
	go build -o bin/analyzer ./cmd/analyzer
	go build -o bin/crawler ./cmd/crawler
	go build -o bin/import ./cmd/import
	@echo "Done!"

build-analyzer:
	go build -o bin/analyzer ./cmd/analyzer

build-crawler:
	go build -o bin/crawler ./cmd/crawler

build-import:
	go build -o bin/import ./cmd/import

# =============================================================================
# Test & Lint
# =============================================================================

test:
	@echo "Running tests..."
	go list ./... | grep -v '/old/' | xargs go test -v

test-coverage:
	go list ./... | grep -v '/old/' | xargs go test -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

lint:
	golangci-lint run ./...

# =============================================================================
# Clean
# =============================================================================

clean:
	rm -rf bin/
	rm -f coverage.out coverage.html

# =============================================================================
# Docker
# =============================================================================

docker-build:
	@echo "Building Docker images..."
	docker compose --profile tools build

docker-up:
	@echo "Starting services..."
	docker compose up -d
	@echo "Services started. Use 'make docker-logs' to view logs."

docker-down:
	@echo "Stopping services..."
	docker compose down

# 轻量级部署 (t3.small 2GB - 不含 Crawler)
docker-light-up:
	@echo "Starting lightweight services (no crawler)..."
	docker compose -f docker-compose.light.yml up -d
	@echo "Services started. Crawler runs separately."

docker-light-down:
	docker compose -f docker-compose.light.yml down

docker-light-logs:
	docker compose -f docker-compose.light.yml logs -f

docker-logs:
	docker compose logs -f

docker-logs-analyzer:
	docker compose logs -f analyzer

docker-logs-crawler:
	docker compose logs -f crawler

docker-ps:
	docker compose ps

docker-clean:
	@echo "Removing containers, volumes, and images..."
	docker compose down -v --rmi local
	@echo "Done!"

docker-restart:
	docker compose restart

docker-restart-analyzer:
	docker compose restart analyzer

docker-restart-crawler:
	docker compose restart crawler

# With Monitoring (Grafana Cloud)
docker-up-monitoring:
	@echo "Starting services with monitoring..."
	docker compose --profile monitoring up -d
	@echo "Services + monitoring started."

docker-down-monitoring:
	docker compose --profile monitoring down

# =============================================================================
# Development (Local)
# =============================================================================

dev-deps:
	@echo "Starting MySQL & Redis..."
	docker compose up -d mysql redis
	@echo "Waiting for services to be healthy..."
	@sleep 10
	docker compose ps

dev-analyzer: build-analyzer
	DEBUG=true AUTO_MIGRATE=true ./bin/analyzer

dev-crawler: build-crawler
	DEBUG=true ./bin/crawler

# Run analyzer with web frontend (http://localhost:8080)
dev-web: build-analyzer
	DEBUG=true AUTO_MIGRATE=true STATIC_DIR=web ENABLE_CORS=true ./bin/analyzer

# =============================================================================
# Data Import (via API)
# =============================================================================

import: build-import
	@echo "Usage: ./bin/import -dsn 'DSN' -file data/ips.json"
	@echo "       ./bin/import -file data/ips.json -dry-run"

import-sample: build-import
	./bin/import -file data/ips_sample.json -dry-run

# API import commands (推荐)
api-import:
	@echo "Usage: make api-import-run FILE=data/ips.json"
	@echo "       make api-import-run FILE=data/ips.json API=http://your-server:8080"

api-import-run:
	@if [ -z "$(FILE)" ]; then echo "Error: FILE is required. Usage: make api-import-run FILE=data/ips.json"; exit 1; fi
	@curl -s -X POST $${API:-http://localhost:8080}/api/v1/admin/import \
		-H "Content-Type: application/json" \
		-d @$(FILE) | jq .

# =============================================================================
# Database
# =============================================================================

db-migrate:
	@echo "Running migrations..."
	docker compose exec mysql mysql -u animetop -panimetop_pass animetop < migrations/001_init.sql

db-shell:
	docker compose exec mysql mysql -u animetop -panimetop_pass animetop

redis-cli:
	docker compose exec redis redis-cli

# =============================================================================
# Grayscale Testing
# =============================================================================

grayscale-start:
	@echo "Starting grayscale test environment..."
	@echo "1. Starting services..."
	docker compose up -d
	@echo "2. Waiting for services to be healthy (35s)..."
	@sleep 35
	@echo "3. Importing grayscale test IPs via API..."
	@curl -s -X POST http://localhost:8080/api/v1/admin/import \
		-H "Content-Type: application/json" \
		-d @data/ips_grayscale.json | jq -r '"Imported: created=\(.data.created), updated=\(.data.updated), failed=\(.data.failed)"'
	@echo ""
	@echo "Grayscale test environment ready!"
	@echo "  - API: http://localhost:8080"
	@echo "  - Run 'make grayscale-verify' to check status"

grayscale-start-light:
	@echo "Starting lightweight grayscale test (no crawler)..."
	@echo "1. Starting services..."
	docker compose -f docker-compose.light.yml up -d
	@echo "2. Waiting for services to be healthy (35s)..."
	@sleep 35
	@echo "3. Importing grayscale test IPs via API..."
	@curl -s -X POST http://localhost:8080/api/v1/admin/import \
		-H "Content-Type: application/json" \
		-d @data/ips_grayscale.json | jq -r '"Imported: created=\(.data.created), updated=\(.data.updated), failed=\(.data.failed)"'
	@echo ""
	@echo "Light grayscale test ready! Run crawler separately."

grayscale-verify:
	@chmod +x scripts/grayscale_verify.sh
	@./scripts/grayscale_verify.sh

grayscale-check-redis:
	@chmod +x scripts/check_redis.sh
	@./scripts/check_redis.sh

grayscale-check-mysql:
	@chmod +x scripts/check_mysql.sh
	@./scripts/check_mysql.sh

grayscale-trigger:
	@echo "Manually triggering crawl for all IPs..."
	@curl -s http://localhost:8080/api/v1/ips | jq -r '.data.items[].id' | while read id; do \
		echo "Triggering IP $$id..."; \
		curl -s -X POST "http://localhost:8080/api/v1/ips/$$id/trigger" | jq -r '.message // .error'; \
	done

grayscale-stop:
	@echo "Stopping grayscale test environment..."
	docker compose down
	@echo "Done. Data is preserved in volumes."

grayscale-clean:
	@echo "Cleaning grayscale test environment (including data)..."
	docker compose down -v
	@echo "Done. All data removed."

# =============================================================================
# Protobuf
# =============================================================================

proto:
	protoc --go_out=. --go_opt=module=animetop proto/crawler.proto

# =============================================================================
# Full Test (Automated)
# =============================================================================

full-test:
	@echo "Running full automated test..."
	@./scripts/full_test.sh

full-test-quick:
	@echo "Running quick automated test (skip build, shorter wait)..."
	@./scripts/full_test.sh --skip-build --quick

full-test-skip-build:
	@echo "Running full test (skip build)..."
	@./scripts/full_test.sh --skip-build
