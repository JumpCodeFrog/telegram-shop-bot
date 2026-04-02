.PHONY: build test lint run seed preflight docker-build docker-up docker-down

build:
	go build ./...

test:
	go test ./...

lint:
	go vet ./...

run:
	go run ./cmd/bot

seed:
	go run ./cmd/seed

preflight:
	go run ./cmd/preflight

docker-build:
	docker build -t shop_bot .

docker-up:
	docker compose up --build -d

docker-down:
	docker compose down
