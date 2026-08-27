<p align="center">
  <img src="https://readme-typing-svg.demolab.com?font=Fira+Code&size=30&pause=1000&color=229ED9&center=true&vCenter=true&width=700&lines=🛍️+Telegram+Shop+Bot;Built+with+Go+🐹;Open+Source+✨;One+Command+Setup+🚀" alt="Typing SVG" />
</p>

<p align="center">
  <a href="https://github.com/JumpCodeFrog/telegram-shop-bot/actions/workflows/ci.yml">
    <img src="https://github.com/JumpCodeFrog/telegram-shop-bot/actions/workflows/ci.yml/badge.svg" alt="CI" />
  </a>
  <img src="https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go&logoColor=white" alt="Go" />
  <img src="https://img.shields.io/badge/SQLite-embedded-003B57?logo=sqlite&logoColor=white" alt="SQLite" />
  <img src="https://img.shields.io/badge/Redis-optional-DC382D?logo=redis&logoColor=white" alt="Redis" />
  <img src="https://img.shields.io/badge/Docker-ready-2496ED?logo=docker&logoColor=white" alt="Docker" />
  <img src="https://img.shields.io/badge/Telegram-Bot_API_v5-26A5E4?logo=telegram&logoColor=white" alt="Telegram" />
  <img src="https://img.shields.io/github/license/JumpCodeFrog/telegram-shop-bot?color=green" alt="License" />
</p>

<p align="center">
  <b>🇬🇧 English</b> · <a href="docs/readme/README.ru.md">🇷🇺 Русский</a>
</p>

---

## 🇬🇧 English

A full-featured e-commerce bot for Telegram — catalog, cart, Telegram Stars & USDT payments, Stars subscriptions, reviews & ratings, promo codes, wishlist, working loyalty & referral programs, a built-in Mini App, and an admin panel. 5 languages out of the box. Ships as a single binary. **One guided command, two answers, no config-file editing.**

### ✨ Features

<table>
<tr>
<td width="50%">

**🛍️ Buyer**
- Product catalog with categories & photo galleries
- Cart & checkout inside Telegram
- **Telegram Stars** payments (built-in)
- **Stars subscriptions** — recurring 30-day products, `/mysubs` to manage
- **USDT via CryptoBot** (optional)
- **Mini App** — full shop UI inside Telegram (opt-in via `WEBAPP_URL`)
- **Reviews & ratings** — 1–5 ⭐ after delivery, average shown on the product card
- Promo codes with category limits + personal one-off codes
- Wishlist — price drop & restock alerts
- Search: `/search <query>`
- **Loyalty program** — 1–10 % cashback in points, levels
- **Referral program** — `/referral` link; 100 points for the referrer, −10 % promo for the friend

</td>
<td width="50%">

**🔧 Admin**
- Manage products & categories (photos straight from Telegram, up to 10 per product)
- Order management & status updates
- Promo code CRUD
- Review moderation: `/reviews`
- Analytics: `/analytics` (14-day revenue chart, top buyers, promo report), CSV export with date range
- **Topics notifications** — order events into a supergroup / forum topics (`ADMIN_GROUP_ID`)
- **Button style customization** — `/btnstyle` interactive menu to set Primary/Success/Danger/Default per button
- Admin panel: `/admin`

**⚙️ Infrastructure**
- SQLite embedded DB (WAL), auto-migrations
- Redis FSM (falls back to in-memory)
- Automatic daily backups (`VACUUM INTO`, keeps 7) — no sqlite3 binary needed
- Graceful shutdown of all workers
- Prometheus + Grafana metrics
- Health check at `:8080/health`
- Polling or Webhook (auto-detected)
- Docker multi-stage, non-root runtime
- i18n: 🇬🇧 🇷🇺 🇪🇸 🇩🇪 🇨🇳 — 5 languages out of the box

</td>
</tr>
</table>

---

### 🚀 Quick Start

> For a read-only Telegram Stars ledger check, run `make reconcile-stars`.
> It prints aggregate counts only. Exit `1` means configuration/API/DB failure,
> a truncated or unstable window, provider/local-only rows, amount mismatch, or
> unresolved review items. Model: [ADR 0001](docs/adr/0001-commerce-state-and-payment-ledger.md).
> Recovery stays preview-first and explicit; see the
> [payment operations runbook](docs/payment-operations.md).

Get a token from [@BotFather](https://t.me/BotFather), then run:

```bash
git clone https://github.com/JumpCodeFrog/telegram-shop-bot.git && cd telegram-shop-bot
make quickstart
```

The built-in setup asks only for the BotFather token and your numeric Telegram user ID. It validates the bot through Telegram, derives its username, writes `.env` without overwriting an existing file (`0600` on Unix), checks SQLite and optional Redis, then starts the shop. The token is hidden in an interactive terminal and never printed.

Need your numeric ID? Send any message to [@userinfobot](https://t.me/userinfobot); it is not your phone number or `@username`.

Optional: enable Inline Mode with `/setinline` in @BotFather to share products from the catalog in any chat. `doctor` reports when it is off.

```bash
make doctor     # actionable setup report
make run        # start without the wizard
make payment-review PROVIDER=stars # redacted local review inbox
```

**Requirements:** [Go 1.24+](https://go.dev/dl/) and a bot token. Redis is optional; without it the bot uses its in-memory fallback.

#### Docker Compose

> Only Docker is required. The source checkout is built locally.

```bash
git clone https://github.com/JumpCodeFrog/telegram-shop-bot.git
cd telegram-shop-bot
cp .env.example .env
# Set BOT_TOKEN and ADMIN_IDS in .env, then:
docker compose up -d --build
curl http://127.0.0.1:8080/health
docker compose logs -f bot
```

---

### 🔑 Get a Bot Token

1. Open Telegram → search **[@BotFather](https://t.me/BotFather)**
2. Send `/newbot`
3. Choose a name and username
4. Copy the token: `1234567890:ABCdefGHIjklMNOpqrsTUVwxyz`
5. Paste it when `make quickstart` asks (or set `BOT_TOKEN` in `.env` for Docker)

---

### ⚙️ Configuration

| Variable | Required | Default | Description |
|---|---|---|---|
| `BOT_TOKEN` | **yes** | — | Token from @BotFather |
| `BOT_USERNAME` | no | — | Bot @username (without @), for referral links |
| `ADMIN_IDS` | no | — | Comma-separated Telegram IDs of admins |
| `CRYPTOBOT_TOKEN` | no | — | CryptoBot token for USDT payments |
| `DB_PATH` | no | `data/shop.db` | SQLite database path |
| `LOG_LEVEL` | no | `info` | `debug` / `info` / `warn` / `error` |
| `APP_ENV` | no | `development` | `production` for JSON logs |
| `REDIS_ADDR` | no | `localhost:6379` | Redis address |
| `REDIS_PASSWORD` | no | — | Redis password |
| `USD_TO_STARS_RATE` | no | `50` | Telegram Stars per 1 USD |
| `WEBHOOK_URL` | no | — | Public HTTPS URL for webhook mode |
| `TELEGRAM_WEBHOOK_SECRET` | with `WEBHOOK_URL` | — | Strong webhook verification secret (generate with `openssl rand -hex 32`) |
| `LOCALES_DIR` | no | `locales` | Path to translations folder |
| `WEBAPP_URL` | no | — | Public HTTPS URL of the Mini App; empty = Mini App disabled |
| `ADMIN_GROUP_ID` | no | — | Supergroup ID for admin order notifications (instead of DMs) |
| `TOPIC_ORDERS_NEW` | no | — | Forum topic ID for new-order notifications |
| `TOPIC_ORDERS_PAID` | no | — | Forum topic ID for paid-order notifications |
| `TOPIC_ORDERS_DELIVERED` | no | — | Forum topic ID for delivered-order notifications |
| `OUTBOUND_WEBHOOK_URL` | no | — | External URL receiving `order.paid` / `order.delivered` events |
| `OUTBOUND_WEBHOOK_SECRET` | no | — | `X-Webhook-Secret` header value for outbound webhooks |

> 💡 No `WEBHOOK_URL` → polling mode (great for development).  
> No Redis → automatic fallback to in-memory state.

---

### 📋 Bot Commands

| Command | Description |
|---|---|
| `/start` | Main menu |
| `/catalog` | Browse products |
| `/search <query>` | Search products |
| `/cart` | Your cart |
| `/orders` | Order history |
| `/mysubs` | Your Stars subscriptions (with cancel buttons) |
| `/profile` | Your profile & loyalty status |
| `/referral` | Your referral link, invited count & earned points |
| `/wishlist` | Your wishlist |
| `/support` | Contact support |
| `/paysupport` | Payment help |
| `/terms` | Terms of service |
| `/help` | List of commands |
| `/cancel` | Cancel current action |

**Admin commands** *(require your ID in `ADMIN_IDS`)*:

| Command | Description |
|---|---|
| `/admin` | Admin panel |
| `/addproduct` | Add a product (step-by-step wizard, photos & subscriptions supported) |
| `/editproduct <id> <field> <value>` | Edit a product |
| `/deleteproduct <id>` | Delete a product |
| `/addcategory <name>` | Add a category |
| `/editcategory` / `/deletecategory` / `/listcategories` | Manage categories |
| `/addpromo` / `/listpromos` / `/deletepromo` | Manage promo codes |
| `/orders_all` | All orders |
| `/setdelivered <id>` | Mark an order delivered (triggers review request) |
| `/reviews` | Latest reviews with delete buttons |
| `/analytics` | Revenue chart, top buyers, promo report |
| `/export_orders [from] [to]` | CSV export, optional date range |
| `/btnstyle` | Customize button colors |

---

### 💳 Payments

**Telegram Stars** — built-in, works out of the box. Rate is configurable via `USD_TO_STARS_RATE` (default: 50 Stars = $1).

**CryptoBot (USDT)** — optional. Set `CRYPTOBOT_TOKEN` to enable.  
Background worker polls payment status every 30 seconds. Signatures verified via HMAC-SHA256.

**Stars subscriptions** — a product created as a "30-day subscription" is sold as a recurring Stars payment (`subscription_period=2592000`). Subscriptions are Stars-only; users manage them via `/mysubs`.

---

### 📱 Mini App

A lightweight web shop (vanilla JS, embedded in the binary — no build step) that opens right inside Telegram: catalog, product cards with photos and ratings, cart, and checkout via `openInvoice` (Stars) or CryptoBot.

1. Serve the bot's port 8080 behind a public **HTTPS** domain (Telegram requires HTTPS for Web Apps).
2. Set `WEBAPP_URL=https://shop.example.com/app` in `.env`.
3. Restart — the bot's menu button becomes the Mini App, static files are served at `/app/`, and the REST API at `/api/*` (authorized via Telegram `initData`, HMAC-verified).

> Without `WEBAPP_URL` the Mini App and its API are not mounted at all — the bot works exactly as before.

---

### 🌱 Seed Demo Data

```bash
go run ./cmd/seed
```

Creates categories (Clothing, Shoes, Accessories), 6 products, and promo codes `WELCOME10` (−10%) and `SALE20` (−20%). Safe to run multiple times.

---

### 🚢 Production Deployment

```bash
# 1. Set real BOT_TOKEN and ADMIN_IDS in .env (or run `make init`)
# 2. Run unified checks: config, DB, Redis, Telegram and webhook
make doctor

# 3. Start
docker compose up -d --build
curl http://127.0.0.1:8080/health
curl http://127.0.0.1:8080/metrics
```

<details>
<summary>Webhook + nginx config</summary>

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

```env
WEBHOOK_URL=https://shop.example.com
TELEGRAM_WEBHOOK_SECRET=random-secret-string
```

Telegram posts to `https://shop.example.com/telegram-webhook`. If CryptoBot is enabled, configure its webhook as `https://shop.example.com/cryptobot-webhook`.

</details>

---

### 🏗️ Project Structure

```
telegram-shop-bot/
├── cmd/
│   ├── bot/               # Entrypoint — start the bot
│   ├── preflight/         # Pre-launch env check
│   ├── seed/              # Load demo data
│   ├── telegram-smoke/    # Smoke test via Telegram API
│   └── usability-smoke/   # Buyer flow smoke test (no token)
├── internal/
│   ├── bot/               # Handlers, keyboards, notifications, webhook
│   ├── config/            # Configuration loading
│   ├── payment/           # Stars (incl. subscriptions) & CryptoBot adapters
│   ├── service/           # Cross-cutting services (i18n, loyalty, metrics)
│   ├── shop/              # Catalog, cart, orders (PaymentOutcome pipeline)
│   ├── storage/           # SQLite stores, Redis cache/FSM, migrations
│   └── webapi/            # Mini App REST API (initData auth)
├── web/app/               # Mini App frontend (embedded, vanilla JS)
├── locales/               # Translations (ru, en, es, de, zh)
├── worker/                # Background workers
├── monitoring/            # Grafana dashboard JSON
├── Dockerfile
├── docker-compose.yml
├── Makefile
└── .env.example
```

---

### 🛠️ Makefile

```bash
make build      # Build all binaries
make test       # Run tests
make lint       # go vet
make quickstart # Guided init + doctor + run
make init       # Create .env with two guided answers
make doctor     # Unified environment and service checks
make run        # Start the bot
make seed       # Load demo data
make preflight  # Legacy local/offline checks (doctor is the online check)
make reconcile-stars # Read-only Telegram Stars ledger comparison
make payment-review  # List the Stars review inbox (PROVIDER=crypto is supported)
```

---

### 🔒 Security

- Webhook signature verification (HMAC-SHA256) for CryptoBot
- Secret token support for Telegram webhooks
- Admin access restricted to explicit Telegram ID list
- Docker container runs as non-root user
- Tokens and secrets never appear in logs

Found a vulnerability? See [SECURITY.md](SECURITY.md).

---

### 🤝 Contributing

1. Fork the repo
2. Create a branch: `git checkout -b feature/my-feature`
3. Run tests: `go test ./...`
4. Run linter: `go vet ./...`
5. Open a Pull Request

See [CONTRIBUTING.md](CONTRIBUTING.md) for details.

---

### 📄 License

MIT — do whatever you want. See [LICENSE](LICENSE).

---

Русская версия: [README.ru.md](docs/readme/README.ru.md).
