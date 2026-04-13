.PHONY: build test lint run seed preflight docker-build docker-up docker-down dev setup coverage

build:
	go build ./...

test:
	go test ./...

coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

lint:
	go vet ./...

run:
	go run ./cmd/bot

seed:
	go run ./cmd/seed

preflight:
	go run ./cmd/preflight

## setup: copy .env.example → .env (if absent), create data/ dir, run preflight checks
setup:
	@if [ ! -f .env ]; then \
		cp .env.example .env; \
		echo "✅ Created .env from .env.example — fill in BOT_TOKEN and ADMIN_IDS before running"; \
	else \
		echo "ℹ️  .env already exists, skipping"; \
	fi
	@mkdir -p data backups
	@echo "✅ Directories ready (data/, backups/)"
	@echo ""
	@echo "Next steps:"
	@echo "  1. Edit .env and set BOT_TOKEN, ADMIN_IDS"
	@echo "  2. make run          (local)"
	@echo "  3. make seed         (optional: load demo products)"

## dev: start development environment with hot-reload (requires Docker)
dev:
	docker compose -f docker-compose.dev.yml up --build

docker-build:
	docker build -t shop_bot .

docker-up:
	docker compose up --build -d

docker-down:
	docker compose down

