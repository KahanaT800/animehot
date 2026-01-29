.PHONY: install dev test lint format run docker-build docker-up docker-down clean

# Install dependencies
install:
	poetry install

# Install dev dependencies
dev:
	poetry install --with dev
	poetry run playwright install chromium

# Run tests
test:
	poetry run pytest -v

# Run linter
lint:
	poetry run ruff check src/
	poetry run mypy src/

# Format code
format:
	poetry run black src/
	poetry run ruff check --fix src/

# Run locally (requires Redis at localhost:6379)
run:
	PYTHONPATH=src poetry run python -m mercari_crawler.main

# Build Docker image
docker-build:
	docker build -t mercari-py-crawler:latest .

# Start with docker-compose (dev mode with Redis)
docker-up:
	docker-compose -f docker-compose.dev.yml up -d

# Stop docker-compose
docker-down:
	docker-compose -f docker-compose.dev.yml down

# View logs
docker-logs:
	docker-compose -f docker-compose.dev.yml logs -f py-crawler

# Clean build artifacts
clean:
	rm -rf __pycache__ .pytest_cache .mypy_cache .ruff_cache
	find . -type d -name "__pycache__" -exec rm -rf {} + 2>/dev/null || true
	find . -type f -name "*.pyc" -delete 2>/dev/null || true

# Check health
health:
	curl -s http://localhost:8081/health | python -m json.tool

# Get metrics
metrics:
	curl -s http://localhost:2113/metrics

# Local test (standalone, no crawler needed)
test-local:
	PYTHONPATH=src poetry run python test_local.py

# Start local Redis for testing
redis-start:
	docker run -d --name redis-test -p 6379:6379 redis:7-alpine || docker start redis-test

# Stop local Redis
redis-stop:
	docker stop redis-test || true
