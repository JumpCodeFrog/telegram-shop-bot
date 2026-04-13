# FAQ

## General

### Can I run this without Redis?

No — Redis is required for FSM state (the add-product wizard and promo code input) and caching.  
The easiest way to get Redis locally: `docker run -d -p 6379:6379 redis:7-alpine`

### What database does it use?

SQLite. The database file is created automatically at `data/shop.db` on first run.  
Migrations run automatically at startup — no manual steps needed.

### Does it support webhooks?

Yes. Set `WEBHOOK_URL` in `.env` to your public HTTPS URL. In production, also set `TELEGRAM_WEBHOOK_SECRET`.  
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

### How do I disable crypto payments?

Leave `CRYPTOBOT_TOKEN` empty. The "Pay with Crypto" button is hidden automatically.

---

## Products & Catalog

### How do I add products?

As admin, send `/addproduct` — the bot guides you through a step-by-step dialog:  
name → description → price (USD) → stock → photo (or `/skip`) → category.

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

Any IETF language tag that Telegram sends (e.g. `en`, `ru`, `de`, `zh`, `es`).  
As long as a matching `.json` file exists in `LOCALES_DIR`, it will be used.

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

The `data/` volume contains the SQLite file. For automated backups:
```bash
cp data/shop.db backups/shop_$(date +%Y%m%d_%H%M%S).db
```
Or use the `backups/` volume that Docker Compose already creates.

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

### The bot sends messages in Russian — how do I change the default language?

The bot mirrors the user's Telegram language. To test a specific language, change your Telegram language setting or use the `lang` field in integration tests.
