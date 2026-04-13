# Getting Started

This guide walks you through running your own **Telegram Shop Bot** from scratch.

---

## Prerequisites

| Tool | Version | Notes |
|------|---------|-------|
| Go | 1.24+ | [golang.org](https://golang.org/dl/) |
| Redis | 7+ | Or use Docker — see below |
| Git | any | |

> **No Redis?** Use Docker: `docker run -d -p 6379:6379 redis:7-alpine`

---

## 1. Clone the repository

```bash
git clone https://github.com/JumpCodeFrog/telegram-shop-bot.git
cd telegram-shop-bot
```

---

## 2. Create your Telegram bot

1. Open [@BotFather](https://t.me/BotFather) in Telegram.
2. Send `/newbot` and follow the instructions.
3. Copy the **bot token** (looks like `123456789:AAxxxxxxx...`).
4. Send `/mybots` → your bot → **Bot Settings** → **Inline Mode** → **Enable** (required for inline catalog).

---

## 3. Configure environment

```bash
make setup          # copies .env.example → .env and creates data/ directories
```

Open `.env` and fill in at minimum:

```env
BOT_TOKEN=123456789:AAxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
BOT_USERNAME=my_shop_bot
ADMIN_IDS=<your Telegram user ID>
```

> **How to find your Telegram ID?** Send any message to [@userinfobot](https://t.me/userinfobot).

See [`docs/environment-variables.md`](environment-variables.md) for the full list of options.

---

## 4. Run the bot

```bash
make run
```

The bot starts in **polling mode** — no public URL needed. You should see:

```
time=... level=INFO msg="Bot started" username=my_shop_bot
```

---

## 5. Load demo products (optional)

```bash
make seed
```

This creates sample categories and products so you can test the shop immediately.

---

## 6. Open your bot in Telegram

Send `/start` to your bot. You should see the main menu.

As admin, you have access to `/admin` commands:
- `/addproduct` — add a product interactively
- `/orders` — view all orders
- `/stats` — shop statistics

---

## Running with Docker

```bash
make setup          # create .env if not present
# edit .env
make docker-up      # starts bot + Redis in containers
make docker-down    # stop
```

---

## Development with hot-reload

```bash
make setup
# edit .env
make dev            # docker-compose.dev.yml — rebuilds on every .go change
```

---

## Running with a webhook (production)

1. Get a domain with a valid TLS certificate.
2. Set in `.env`:
   ```env
   APP_ENV=production
   WEBHOOK_URL=https://mybot.example.com/webhook
   TELEGRAM_WEBHOOK_SECRET=<openssl rand -hex 32>
   ```
3. `make docker-up` — the bot registers the webhook automatically on start.

---

## Next steps

- [Environment Variables](environment-variables.md) — full config reference
- [Architecture](architecture.md) — code structure and data flow
- [FAQ](faq.md) — common questions
- [CONTRIBUTING.md](../CONTRIBUTING.md) — how to contribute
