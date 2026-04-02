# Telegram Shop Bot

Open-source Telegram shop bot written in Go.

It gives you a ready-to-deploy baseline for a catalog, cart, orders, Telegram Stars checkout, optional CryptoBot checkout, promo codes, admin flows, health checks, metrics, and background workers.

The project is designed so that a developer can clone it, add a bot token, and launch it locally or in Docker without rebuilding the architecture from scratch.

## Features

- Inline Telegram UI for `/start`, catalog, product cards, cart, checkout, orders, profile, support, payment support, and terms.
- SQLite storage with embedded migrations.
- Telegram Stars payments.
- Optional CryptoBot checkout for USDT.
- Promo codes and guarded pending-order payment flow.
- Admin flows for products, categories, orders, promos, analytics, and CSV export.
- Health endpoint and Prometheus metrics on `:8080`.
- Redis-backed FSM/cache helpers with graceful fallback to in-memory mode.
- Background workers for backups, cart recovery, onboarding, wishlist watch, and optional CryptoBot polling.

## Project Status

This repository is a stable, deployable baseline.

- `go test ./...` is green.
- `go build ./...` is green.
- `go run ./cmd/preflight` is available for environment checks.
- `go run ./cmd/telegram-smoke` validates live Telegram API access.
- `go run ./cmd/usability-smoke` simulates the main buyer journey without a real Telegram client.

The current runtime uses `go-telegram-bot-api/v5`.
Long-term v3 planning docs for a later `telego` migration are kept in:

- `tz_telegram_shop_bot.md`
- `docs/superpowers/specs/2026-03-30-telegram-shop-bot-v3-design.md`

For actual runtime behavior, trust the code, `README.md`, and `internal/storage/migrations/`.

## Quick Start

### Option 1: Docker Compose

This is the easiest way to deploy the bot.

1. Copy the environment template:

```bash
cp .env.example .env
```

2. Edit `.env` and set at least:

- `BOT_TOKEN`
- optionally `CRYPTOBOT_TOKEN`
- optionally `ADMIN_IDS`

3. Start the stack:

```bash
docker compose up -d --build
```

4. Check that the bot is healthy:

```bash
docker compose ps
curl http://127.0.0.1:8080/health
docker compose logs -f bot
```

Notes:

- Docker Compose automatically overrides `REDIS_ADDR` to `redis:6379`.
- Bot data is stored in the named volume `bot_data`.
- Backups are stored in the named volume `bot_backups`.
- If `CRYPTOBOT_TOKEN` is empty, the bot still works, but USDT checkout stays disabled.

### Option 2: Local Run

Requirements:

- Go `1.26.1` or compatible
- `BOT_TOKEN`
- optional Redis
- optional `sqlite3` CLI if you want the backup worker to actually create dumps

Run:

```bash
cp .env.example .env
go mod tidy
go run ./cmd/preflight
go test ./...
go build ./...
go run ./cmd/bot
```

## Configuration

Copy `.env.example` to `.env`.

Required:

- `BOT_TOKEN`

Common optional values:

- `CRYPTOBOT_TOKEN`
- `ADMIN_IDS`
- `DB_PATH`
- `LOG_LEVEL`
- `APP_ENV`
- `REDIS_ADDR`
- `REDIS_PASSWORD`
- `USD_TO_STARS_RATE`
- `WEBHOOK_URL`
- `TELEGRAM_WEBHOOK_SECRET`
- `LOCALES_DIR`

Behavior:

- `APP_ENV=production` switches logs to JSON.
- When `WEBHOOK_URL` is empty, the bot runs in polling mode.
- When `WEBHOOK_URL` is set, the bot registers a Telegram webhook and exposes webhook handlers on the same `:8080` server.
- Redis is optional. If it is unavailable, the bot falls back to in-memory FSM/cache behavior where possible.

## Deploy Checklist

Before exposing the bot publicly:

1. Set a real `BOT_TOKEN`.
2. Set `ADMIN_IDS` to your Telegram user ID if you need admin features.
3. Decide whether you want polling mode or webhook mode.
4. If you use webhook mode, make sure `WEBHOOK_URL` is publicly reachable by Telegram.
5. Run:

```bash
go run ./cmd/preflight
go run ./cmd/telegram-smoke
```

6. After startup, check:

```bash
curl http://127.0.0.1:8080/health
curl http://127.0.0.1:8080/metrics
```

7. In Telegram, manually test:

- `/start`
- catalog navigation
- adding items to cart
- checkout
- `/terms`
- `/support`
- `/paysupport`

## Seed Demo Data

Populates the database with demo categories, products, and promo codes for local testing:

```bash
go run ./cmd/seed
```

Safe to run multiple times — inserts use `INSERT OR IGNORE`, so no duplicates are created.

What it creates:

- **Categories:** Одежда (👕), Обувь (👟), Аксессуары (🎒)
- **Products:** 6 items spread across the categories, with USD and Stars prices
- **Promo codes:** `WELCOME10` (10% off, 100 uses), `SALE20` (20% off, 50 uses)

## Smoke Commands

### Preflight

Checks env, SQLite, Redis reachability, webhook mode, and helper CLIs:

```bash
go run ./cmd/preflight
```

### Telegram Smoke

Validates the live `BOT_TOKEN` against the Telegram Bot API without starting the full bot:

```bash
go run ./cmd/telegram-smoke
```

It checks:

- `getMe`
- webhook visibility
- pending updates
- webhook mismatch signals

### Usability Smoke

Runs the buyer journey against a fake local Telegram API and prints the actual bot screens:

```bash
go run ./cmd/usability-smoke
```

It currently simulates:

- `/start`
- catalog navigation
- product card quantity changes
- cart edits
- checkout
- payment method screen
- terms/payment support flows
- Stars invoice request

## Docker Notes

The production image now:

- builds with Go `1.26`
- uses a Linux static binary (`CGO_ENABLED=0`)
- runs as a non-root user
- exposes `/health`
- includes `sqlite` in the runtime image so the backup worker can use the `sqlite3` CLI

Useful commands:

```bash
docker compose up -d --build
docker compose logs -f bot
docker compose restart bot
docker compose down
```

## Public Repo Safety

This repository is intended for public use, so local secrets and runtime data should not be committed.

The project now ignores:

- `.env`
- local agent folders such as `.claude/`, `.gemini/`, `.serena/`
- SQLite runtime databases
- generated backups

If you publish this repo:

- never commit a real `.env`
- never commit real bot tokens, GitHub tokens, Stripe keys, or similar local connector credentials
- rotate any secret that has already been stored in local helper files before publishing

## Commands

Main user commands:

- `/start`
- `/catalog`
- `/search <query>`
- `/cart`
- `/orders`
- `/profile`
- `/wishlist`
- `/support`
- `/paysupport`
- `/terms`
- `/help`
- `/cancel`

Admin entrypoint:

- `/admin`

## Project Layout

```text
.
├── cmd/
│   ├── bot/               # main runtime entrypoint
│   ├── preflight/         # environment readiness check
│   ├── seed/              # local demo data seeder
│   ├── telegram-smoke/    # Telegram API connectivity smoke
│   └── usability-smoke/   # local buyer-journey smoke
├── internal/
│   ├── bot/               # Telegram handlers, middleware, UI formatting
│   ├── config/            # env config loading
│   ├── payment/           # Stars and CryptoBot adapters
│   ├── service/           # business services
│   ├── shop/              # catalog/cart/order logic
│   └── storage/           # SQLite/Redis stores and migrations
├── locales/               # i18n bundles
├── worker/                # background workers
├── Dockerfile
└── docker-compose.yml
```

## Runtime Source Of Truth

- Active DB schema: `internal/storage/migrations/`
- Current project tracker / handoff: `AGENT_WORK_PLAN.md`
- Public runtime guide: this `README.md`

If docs disagree, prefer the runtime code and `internal/storage/migrations/`.
