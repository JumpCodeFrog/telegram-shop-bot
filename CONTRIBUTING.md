# Contributing

## Requirements

- Go 1.26+
- Docker (optional, for container workflow)

## Development

```bash
# Run all tests
go test ./...

# Build all binaries
go build ./...

# Check local runtime health (no live token needed)
go run ./cmd/preflight

# Run buyer journey usability smoke (no live token needed)
go run ./cmd/usability-smoke

# Validate live Telegram token and webhook state (requires BOT_TOKEN in .env)
go run ./cmd/telegram-smoke
```

## Docker

```bash
# Start services
docker compose up -d

# View logs
docker compose logs -f bot

# Stop services
docker compose down
```

## Environment

Copy `.env.example` to `.env` and fill in the required values before running the bot.

```bash
cp .env.example .env
```

## Code Style

- Run `gofmt -w` on changed files before committing.
- All new code should have corresponding tests where practical.
- Keep locale strings in `locales/ru.json` and `locales/en.json` in sync.

## Submitting Changes

1. Fork the repository.
2. Create a feature branch.
3. Run `go test ./...` and ensure it is green.
4. Open a pull request against `main`.
