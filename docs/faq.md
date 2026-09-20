# FAQ

## General

### Can I run this without Redis?

Yes. If Redis is unreachable at startup, the bot falls back to an in-memory FSM store and disables the Redis-dependent loyalty notification worker — everything else works. Redis is still recommended in production for persistent dialog state and product caching.
The easiest way to get Redis locally: `docker run -d -p 6379:6379 redis:7-alpine`

### What database does it use?

SQLite. The database file is created automatically at `data/shop.db` on first run.
Migrations run automatically at startup — no manual steps needed.

### Does it support webhooks?

Yes. Set `WEBHOOK_URL` in `.env` to your public HTTPS URL and always set a strong `TELEGRAM_WEBHOOK_SECRET` (`openssl rand -hex 32`).
Leave `WEBHOOK_URL` empty to use long polling (recommended for local development).

---

## Payments

### How do I enable Telegram Stars payments?

Stars payments are enabled by default — no extra configuration needed.
Just set `USD_TO_STARS_RATE` to control the price conversion (default: 50 Stars = $1).

### How do I enable CryptoBot (USDT) payments?

1. Open [@CryptoBot](https://t.me/CryptoBot) → My Apps → Create App.
2. Copy the token and set `CRYPTOBOT_TOKEN=<token>` in `.env`.
3. Restart the bot — crypto payment buttons appear automatically.

### How do I enable YooKassa (RUB card) payments?

1. In the [YooKassa merchant cabinet](https://yookassa.ru/) create a shop and copy its shop ID and secret key.
2. Set `YOOKASSA_SHOP_ID`, `YOOKASSA_SECRET_KEY`, `YOOKASSA_RETURN_URL` (public HTTPS page the buyer returns to) and `USD_TO_RUB_RATE` (RUB per 1 USD, e.g. `92.5`) in `.env` — the first three must be set together.
3. Set `WEBHOOK_URL` and register `<WEBHOOK_URL>/yookassa-webhook` as the notification URL in the merchant cabinet.
4. Restart the bot — the "Pay by card" button appears automatically.

### How do I enable Stripe (USD card) payments?

1. In the Stripe Dashboard get your secret key (`sk_live_…` / `sk_test_…`).
2. Set `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET` and `STRIPE_RETURN_URL` (public HTTPS) in `.env` — all three must be set together.
3. Set `WEBHOOK_URL`; in the dashboard (Developers → Webhooks) register `<WEBHOOK_URL>/stripe-webhook` for `checkout.session.completed` and copy the endpoint's signing secret (`whsec_…`) into `STRIPE_WEBHOOK_SECRET`.
4. Restart the bot — the Stripe card button appears automatically.

### How do I enable TON (on-chain) payments?

1. Set `TON_WALLET_ADDRESS` (the shop's 48-character TON wallet) and `USD_PER_TON` (USD per 1 TON, e.g. `5.25`) together in `.env`; `TON_API_KEY` (toncenter) is optional and only lifts rate limits.
2. Restart the bot — the TON button appears automatically. There is no webhook: a background worker polls the wallet via toncenter every ~30 seconds.

### How do I enable NOWPayments (300+ coins) payments?

1. In the NOWPayments dashboard get your API key.
2. Set `NOWPAYMENTS_API_KEY`, `NOWPAYMENTS_IPN_SECRET` and `NOWPAYMENTS_RETURN_URL` (public HTTPS) in `.env` — all three must be set together.
3. Set `WEBHOOK_URL` and register `<WEBHOOK_URL>/nowpayments-webhook` as the IPN callback URL in the dashboard.
4. Restart the bot — the NOWPayments button appears automatically. Confirm the signed-IPN canonicalization with ONE live test payment before going to production (verification fails closed).

### How do I disable crypto payments?

Leave `CRYPTOBOT_TOKEN` empty. The "Pay with Crypto" button is hidden automatically. The same all-or-nothing rule disables the other optional rails: leave the YooKassa/Stripe/NOWPayments triples and the TON address+rate pair fully empty and their buttons stay hidden.

---

## Products & Catalog

### How do I add products?

As admin, send `/addproduct` — the bot guides you through a step-by-step dialog:
name → description → price (USD) → stock → photos (send photos as messages or a URL, up to 10; `/done` or `/skip`) → category → type (regular product or 30-day Stars subscription).

### How do I add categories?

As admin, send `/addcategory <name>`.

### Can I edit an existing product?

Yes: `/editproduct <id> <field> <value>`
Fields: `name`, `description`, `price`, `stock`, `category`, `active`
Example: `/editproduct 5 price 14.99`

### What does "digital product" mean?

If a product is marked as digital (`is_digital = true`), the bot sends the `digital_content` field directly to the buyer as a message after payment. Useful for licenses, PDFs, download links.

---

## Localization

### How do I add a new language?

1. Copy `locales/en.json` to `locales/<lang_code>.json` (e.g. `locales/de.json`).
2. Translate all the values.
3. Restart the bot — it picks up new files automatically.

The bot selects the language based on the user's Telegram language setting (`LanguageCode`).
Falls back to `en` if the user's language file is not found.

See [`CONTRIBUTING.md`](../CONTRIBUTING.md) for details on submitting translations.

### What language codes are supported?

The bot ships with 5 locales: `ru`, `en`, `es`, `de`, `zh`.
Any IETF language tag that Telegram sends is normalized to its primary subtag (`ru-RU` → `ru`, `zh-hans-CN` → `zh`); as long as a matching `.json` file exists in `LOCALES_DIR`, it will be used. Anything else falls back to `en`.

---

## Deployment

### How do I run in production with Docker?

```bash
cp .env.example .env
# fill in BOT_TOKEN, ADMIN_IDS, etc.
make docker-up
```

Logs: `docker compose logs -f bot`

### How do I back up the database?

Backups are automatic: a background worker runs `VACUUM INTO 'backups/shop_YYYYMMDD_HHMMSS.db'` every 24 hours on the live connection (no `sqlite3` binary needed — works inside scratch Docker images) and keeps the 7 newest files. The `backups/` volume is already created by Docker Compose.

To take a manual snapshot, use the same mechanism from any SQLite client:
```sql
VACUUM INTO 'backups/manual.db';
```
> ⚠️ Don't just `cp data/shop.db` while the bot is running — the database uses WAL mode, so a plain copy without the `-wal`/`-shm` files can be inconsistent.

### How do I update to a new version?

```bash
git pull
make docker-up     # rebuilds the image automatically
```

Migrations run automatically on startup — no manual DB steps needed.

---

## Development

### How do I run with hot-reload?

```bash
make dev    # starts docker-compose.dev.yml with Air hot-reload
```

Any change to `.go` files triggers an automatic rebuild and restart.

### How do I run tests?

```bash
make test
make coverage   # with HTML coverage report
```

### What is the default language?

English. The bot mirrors the user's Telegram language (`language_code`) across the 5 shipped locales and falls back to `en` when the language is empty or has no locale file. To test a specific language, change your Telegram language setting or use the `lang` field in integration tests.
