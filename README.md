# Telegram Shop Bot

Open-source Telegram shop bot written in Go — ready-to-deploy solution for running a storefront entirely inside Telegram.

Catalog, cart, checkout, Telegram Stars payments, optional CryptoBot (USDT) checkout, promo codes, admin panel, i18n, background workers, health checks, and Prometheus metrics — all in a single binary.

---

## Features

### Buyer flows
- Inline Telegram UI: `/start`, catalog, product cards, cart, checkout, orders, profile, wishlist, support, terms
- Single-message navigation (no message spam — the bot updates one message in-place)
- Telegram Stars payments (built-in)
- Optional CryptoBot checkout for USDT
- Promo codes with category restrictions and usage limits
- Product search: `/search <query>`
- Wishlist with price-drop and back-in-stock notifications

### Admin flows
- Product and category management (add / edit / delete)
- Order management and status updates
- Promo code CRUD
- Analytics dashboard with CSV export
- Admin entrypoint: `/admin`

### Infrastructure
- SQLite storage with embedded auto-migrations
- Redis-backed FSM and cache (optional — graceful fallback to in-memory)
- Health endpoint and Prometheus metrics on `:8080`
- Background workers: backups, cart recovery, onboarding, wishlist watch, CryptoBot polling
- Polling mode or Webhook mode (auto-selected by config)
- Docker-ready with multi-stage build and non-root runtime
- i18n support (Russian and English out of the box)

---

## Quick Start

### Option 1: Docker Compose (recommended)

```bash
# 1. Clone the repo
git clone https://github.com/JumpCodeFrog/telegram-shop-bot.git
cd telegram-shop-bot

# 2. Create config
cp .env.example .env

# 3. Edit .env — set BOT_TOKEN (required)
nano .env

# 4. Start
docker compose up -d --build

# 5. Verify
docker compose ps
curl http://127.0.0.1:8080/health
docker compose logs -f bot
```

Docker Compose automatically:
- Sets `REDIS_ADDR=redis:6379`
- Stores bot data in the `bot_data` named volume
- Stores backups in the `bot_backups` named volume
- Runs health checks every 15 seconds

### Option 2: Local binary

**Requirements:**
- Go 1.23+
- `BOT_TOKEN` from [@BotFather](https://t.me/BotFather)
- Redis (optional)
- `sqlite3` CLI (optional, for backup worker)

```bash
# 1. Clone and configure
git clone https://github.com/JumpCodeFrog/telegram-shop-bot.git
cd telegram-shop-bot
cp .env.example .env
nano .env   # set BOT_TOKEN

# 2. Build and verify
go mod tidy
go build ./...
go test ./...

# 3. Run environment checks
go run ./cmd/preflight

# 4. Start the bot
go run ./cmd/bot
```

---

## Configuration

All settings are in `.env`. Copy `.env.example` to get started.

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `BOT_TOKEN` | **yes** | — | Telegram Bot API token from @BotFather |
| `BOT_USERNAME` | no | — | Bot's @username (without @), used for referral deep-links |
| `CRYPTOBOT_TOKEN` | no | — | CryptoBot API token for USDT payments |
| `ADMIN_IDS` | no | — | Comma-separated Telegram user IDs for admin access |
| `DB_PATH` | no | `data/shop.db` | SQLite database file path |
| `LOG_LEVEL` | no | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `APP_ENV` | no | `development` | Set `production` for JSON logs |
| `REDIS_ADDR` | no | `localhost:6379` | Redis address |
| `REDIS_PASSWORD` | no | — | Redis password |
| `USD_TO_STARS_RATE` | no | `50` | Stars per 1 USD (Telegram sells 50 Stars ≈ $0.99) |
| `WEBHOOK_URL` | no | — | Public URL for Telegram webhook mode |
| `TELEGRAM_WEBHOOK_SECRET` | no | — | Secret token for webhook verification |
| `LOCALES_DIR` | no | `locales` | Path to i18n locale files |

**Behavior notes:**
- When `WEBHOOK_URL` is empty → polling mode (good for development)
- When `WEBHOOK_URL` is set → webhook mode (recommended for production)
- When Redis is unavailable → automatic fallback to in-memory FSM/cache
- When `CRYPTOBOT_TOKEN` is empty → USDT checkout disabled, Stars still works

---

## Seed Demo Data

Populates the database with demo categories, products, and promo codes:

```bash
go run ./cmd/seed
```

Safe to run multiple times (uses `INSERT OR IGNORE`).

Creates:
- **Categories:** Одежда (👕), Обувь (👟), Аксессуары (🎒)
- **Products:** 6 items across categories, with USD and Stars prices
- **Promo codes:** `WELCOME10` (10% off, 100 uses), `SALE20` (20% off, 50 uses)

---

## Bot Commands

### User commands

| Command | Description |
|---------|-------------|
| `/start` | Main menu |
| `/catalog` | Browse product catalog |
| `/search <query>` | Search products |
| `/cart` | View cart |
| `/orders` | Order history |
| `/profile` | User profile |
| `/wishlist` | Saved items |
| `/support` | Customer support |
| `/paysupport` | Payment help |
| `/terms` | Terms and conditions |
| `/help` | Command list |
| `/cancel` | Cancel current action |

### Admin commands

| Command | Description |
|---------|-------------|
| `/admin` | Admin panel entry |

Admin access requires your Telegram user ID in the `ADMIN_IDS` environment variable.

---

## Payments

### Telegram Stars

Built-in, works out of the box. The bot sends a Telegram Stars invoice and handles the `successful_payment` callback automatically.

Rate is configurable via `USD_TO_STARS_RATE` (default: 50 Stars = $1).

### CryptoBot (USDT)

Optional. Set `CRYPTOBOT_TOKEN` to enable.

- The bot creates invoices via CryptoBot API
- A background polling worker checks invoice status every 30 seconds
- Webhook signature verification uses HMAC-SHA256

---

## Deployment

### Production checklist

1. Set a real `BOT_TOKEN`
2. Set `ADMIN_IDS` to your Telegram user ID
3. Choose polling or webhook mode
4. If using webhooks — ensure `WEBHOOK_URL` is publicly reachable (HTTPS required by Telegram)
5. Run pre-flight checks:

```bash
go run ./cmd/preflight        # env, DB, Redis, webhook checks
go run ./cmd/telegram-smoke   # live Telegram API validation
```

6. Start the bot and verify:

```bash
# Docker
docker compose up -d --build
curl http://127.0.0.1:8080/health
curl http://127.0.0.1:8080/metrics

# or local
go run ./cmd/bot
```

7. Manual smoke test in Telegram:
   - `/start` → navigate catalog → add item → cart → checkout → pay
   - `/terms`, `/support`, `/paysupport`

### Webhook mode with reverse proxy

For production webhook mode, put the bot behind nginx or Caddy with TLS:

```nginx
server {
    listen 443 ssl;
    server_name shop.example.com;

    ssl_certificate     /etc/letsencrypt/live/shop.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/shop.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }
}
```

Then set:
```
WEBHOOK_URL=https://shop.example.com/webhook/telegram
TELEGRAM_WEBHOOK_SECRET=your-random-secret-here
```

### Docker commands

```bash
docker compose up -d --build   # start / rebuild
docker compose logs -f bot     # follow logs
docker compose restart bot     # restart bot only
docker compose down            # stop everything
```

---

## Smoke Tests

| Command | What it checks |
|---------|---------------|
| `go run ./cmd/preflight` | Env vars, SQLite, Redis, webhook config, helper CLIs |
| `go run ./cmd/telegram-smoke` | Live `BOT_TOKEN` against Telegram API (getMe, webhook, pending updates) |
| `go run ./cmd/usability-smoke` | Buyer journey against a fake local Telegram API (no real bot needed) |

---

## Project Structure

```
.
├── cmd/
│   ├── bot/               # Main runtime entrypoint
│   ├── preflight/         # Environment readiness checks
│   ├── seed/              # Demo data seeder
│   ├── telegram-smoke/    # Telegram API smoke test
│   └── usability-smoke/   # Local buyer-journey simulation
├── internal/
│   ├── bot/               # Telegram handlers, middleware, UI
│   ├── config/            # Env config loading
│   ├── payment/           # Stars and CryptoBot adapters
│   ├── service/           # Business logic (i18n, loyalty, metrics, payments)
│   ├── shop/              # Catalog, cart, order domain logic
│   └── storage/           # SQLite / Redis stores and migrations
├── locales/               # i18n bundles (ru.json, en.json)
├── worker/                # Background workers
├── Dockerfile             # Multi-stage production build
├── docker-compose.yml     # Full stack (bot + Redis)
├── Makefile               # Dev shortcuts
└── .env.example           # Configuration template
```

---

## Makefile

```bash
make build      # go build ./...
make test       # go test ./...
make lint       # go vet ./...
make run        # go run ./cmd/bot
make seed       # go run ./cmd/seed
make preflight  # go run ./cmd/preflight
```

---

## Localization

The bot supports multiple languages via JSON locale files in the `locales/` directory.

Currently included:
- `ru.json` — Russian (default)
- `en.json` — English

To add a new language, create a new JSON file (e.g., `locales/de.json`) following the same key structure. Language selection is based on the user's Telegram language setting.

---

## Security

- Webhook signature verification (HMAC-SHA256) for CryptoBot
- Telegram webhook secret token support
- Admin access gated by explicit user ID allowlist
- Non-root Docker runtime
- No secrets in logs or error messages

See [SECURITY.md](SECURITY.md) for vulnerability reporting and best practices.

---

## Contributing

1. Fork the repository
2. Create a feature branch: `git checkout -b feature/my-feature`
3. Run tests: `go test ./...`
4. Run linter: `go vet ./...`
5. Commit your changes
6. Open a Pull Request

---

## License

MIT License. See [LICENSE](LICENSE) for details.
