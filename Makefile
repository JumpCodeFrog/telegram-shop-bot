.PHONY: build test lint run seed init doctor reconcile-stars payment-review quickstart preflight docker-build docker-up docker-down dev setup coverage

PROVIDER ?= stars

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
	go run ./cmd/bot run

seed:
	go run ./cmd/seed

preflight:
	go run ./cmd/preflight

init:
	go run ./cmd/bot init

doctor:
	go run ./cmd/bot doctor

reconcile-stars:
	go run ./cmd/bot reconcile-stars $(ARGS)

payment-review:
	go run ./cmd/bot payment-review list --provider=$(PROVIDER)

## quickstart: ask two questions, verify the setup, then start the shop
quickstart:
	go run ./cmd/bot quickstart

## setup: legacy non-interactive bootstrap; use `make init` for the guided setup
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
	@echo "  2. make doctor       (online readiness check)"
	@echo "  3. make run          (local)"

## dev: start development environment with hot-reload (requires Docker)
dev:
	docker compose -f docker-compose.dev.yml up --build

docker-build:
	docker build -t shop_bot .

docker-up:
	docker compose up --build -d

docker-down:
	docker compose down
