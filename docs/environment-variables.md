# Environment Variables

All configuration is done via environment variables.  
Copy `.env.example` to `.env` and fill in the values.

---

## Required

| Variable | Description | Example |
|----------|-------------|---------|
| `BOT_TOKEN` | Telegram bot token from @BotFather | `123456789:AAxxx...` |
| `ADMIN_IDS` | Comma-separated Telegram user IDs with admin access | `123456789,987654321` |

---

## Bot

| Variable | Default | Description |
|----------|---------|-------------|
| `BOT_USERNAME` | — | Bot's username without `@`. Used for referral deep-links in onboarding messages. |

---

## Database

| Variable | Default | Description |
|----------|---------|-------------|
| `DB_PATH` | `data/shop.db` | Path to the SQLite database file. Created automatically on first run. |

---

## Redis

| Variable | Default | Description |
|----------|---------|-------------|
| `REDIS_ADDR` | `localhost:6379` | Redis address. In Docker Compose this is automatically set to `redis:6379`. |
| `REDIS_PASSWORD` | _(empty)_ | Redis password. Leave empty if Redis has no auth. |

Redis is used for:
- FSM state (add-product dialog, promo code entry)
- Caching product catalog (reduces DB reads)

---

## Payments

| Variable | Default | Description |
|----------|---------|-------------|
| `USD_TO_STARS_RATE` | `50` | How many Telegram Stars equal $1.00. Telegram's official rate is ~50 Stars / $1. |
| `CRYPTOBOT_TOKEN` | _(empty)_ | Token from [@CryptoBot](https://t.me/CryptoBot) → My Apps. Leave empty to disable crypto payments. |

---

## Webhook

| Variable | Default | Description |
|----------|---------|-------------|
| `WEBHOOK_URL` | _(empty)_ | Public HTTPS URL for Telegram to POST updates. Leave empty to use long polling. |
| `TELEGRAM_WEBHOOK_SECRET` | _(empty)_ | Secret token for webhook request verification. Required in production when `WEBHOOK_URL` is set. Generate with `openssl rand -hex 32`. |

> When `WEBHOOK_URL` is empty the bot uses **long polling** — recommended for local development.

---

## Outbound Webhooks

| Variable | Default | Description |
|----------|---------|-------------|
| `OUTBOUND_WEBHOOK_URL` | _(empty)_ | Your server URL that receives HTTP POST notifications on order events. Leave empty to disable. |
| `OUTBOUND_WEBHOOK_SECRET` | _(empty)_ | Sent as `X-Webhook-Secret` header so your server can verify the request origin. |

### Payload format

```json
{
  "event": "order.paid",
  "order_id": 42,
  "user_id": 123456789,
  "total_usd": 9.99,
  "total_stars": 499,
  "method": "stars",
  "payment_id": "telegram_charge_id"
}
```

Events: `order.paid`, `order.delivered`

---

## Application

| Variable | Default | Description |
|----------|---------|-------------|
| `APP_ENV` | `development` | `development` → text logs; `production` → JSON logs + webhook secret enforced. |
| `LOG_LEVEL` | `info` | Log verbosity: `debug`, `info`, `warn`, `error`. |
| `LOCALES_DIR` | `locales` | Path to directory with translation files (`ru.json`, `en.json`, …). |
