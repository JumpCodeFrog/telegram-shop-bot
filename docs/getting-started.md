# Getting Started

Run your own **Telegram Shop Bot** with one guided command.

## Fastest path

### Requirements

- A bot token from [@BotFather](https://t.me/BotFather)
- Your numeric Telegram user ID

Prebuilt releases need no Go, Make, or Docker. Building from source requires [Go 1.24+](https://go.dev/dl/), Git, and Make. Redis is optional; if it is unavailable, the bot uses its in-memory state store.

### 1. Create the bot

1. Open [@BotFather](https://t.me/BotFather).
2. Send `/newbot` and follow the prompts.
3. Copy the token.
4. Optional but recommended for sharing products in any chat: `/setinline` → select the bot → set a placeholder such as `Search products`.

### 2. Configure, verify, and run

Download a release for your system from [GitHub Releases](https://github.com/JumpCodeFrog/telegram-shop-bot/releases). Both Windows and Linux builds offer `amd64` for Intel/AMD processors and `arm64` for ARM processors. Windows archives are available from v3.0.1.

Download `checksums.txt` from the same release and [verify the archive's SHA-256](release-verification.md) before extracting it.

**Windows:** download the matching Windows ZIP and extract the entire archive, including `locales`. Open PowerShell in the extracted directory containing `telegram-shop-bot.exe`:

```powershell
.\telegram-shop-bot.exe quickstart
```

**Linux (`amd64` or `arm64`):** extract the matching `.tar.gz` archive, keep `locales` beside the executable, and open a terminal in the extracted directory:

```bash
./telegram-shop-bot quickstart
```

**From source:**

```bash
git clone https://github.com/JumpCodeFrog/telegram-shop-bot.git
cd telegram-shop-bot
make quickstart
```

The setup asks two questions:

1. BotFather token — hidden while typing in an interactive terminal.
2. Your numeric Telegram user ID — used for admin access.

To find the ID, send any message to [@userinfobot](https://t.me/userinfobot). The ID is not your phone number or `@username`.

It then performs a read-only Telegram `getMe` check, derives `BOT_USERNAME`, creates `.env` without overwriting an existing file (`0600` on Unix), validates SQLite migrations and optional Redis, and starts the bot. It does not seed products.

### 3. Open the shop

Open `https://t.me/<your_bot_username>` and send `/start`.

Stop the bot with `Ctrl+C`. For later starts and checks, use the binary commands below. From a source checkout, the Make equivalents are:

```bash
make doctor   # configuration, SQLite, Redis, Telegram, webhook status
make run      # start an already configured shop
make seed     # optional demo products; never run automatically
```

## Direct binary commands

Run commands from the extracted directory so the bot can find `.env` and `locales`.

**Windows PowerShell:**

```powershell
.\telegram-shop-bot.exe doctor   # configuration and service checks
.\telegram-shop-bot.exe version  # installed version
.\telegram-shop-bot.exe run      # start an already configured shop
```

**Linux:**

```bash
./telegram-shop-bot init       # first-time configuration only
./telegram-shop-bot doctor
./telegram-shop-bot version
./telegram-shop-bot run
```

For payment inspection on Linux (on Windows, use `.\telegram-shop-bot.exe` with the same arguments):

```bash
./telegram-shop-bot reconcile-stars  # read-only aggregate Stars ledger check
./telegram-shop-bot payment-review help
```

`telegram-shop-bot` without arguments remains an alias for `run`.

Payment recovery is deliberately two-step: list or ingest a trusted provider
fact, inspect the read-only preview, then repeat with `--apply` and an exact
`--confirm-order`. See [Payment operations](payment-operations.md) for complete
copy-paste examples and exit codes.

## Docker Compose

Docker Compose builds the current checkout locally:

```bash
cp .env.example .env
# Set BOT_TOKEN and ADMIN_IDS in .env.
docker compose up -d --build
curl http://127.0.0.1:8080/health
docker compose logs -f bot
```

Stop it with `docker compose down`.

## Production webhook mode

1. Put port 8080 behind a public HTTPS domain.
2. Set:

   ```env
   APP_ENV=production
   WEBHOOK_URL=https://shop.example.com
   TELEGRAM_WEBHOOK_SECRET=<random secret>
   ```

3. Run `./telegram-shop-bot doctor` on Linux or `.\telegram-shop-bot.exe doctor` on Windows before starting.
4. Start the service and verify `/health` and `/metrics`.

`WEBHOOK_URL` is the public base URL. Telegram posts to `<WEBHOOK_URL>/telegram-webhook`; CryptoBot, when enabled, uses `<WEBHOOK_URL>/cryptobot-webhook`; YooKassa, when enabled, uses `<WEBHOOK_URL>/yookassa-webhook` (register it in the YooKassa merchant cabinet); Stripe, when enabled, uses `<WEBHOOK_URL>/stripe-webhook` (register it in the Stripe dashboard and copy its signing secret into `STRIPE_WEBHOOK_SECRET`); NOWPayments, when enabled, uses `<WEBHOOK_URL>/nowpayments-webhook` (register it as the IPN callback URL in the NOWPayments dashboard). TON is the exception: it has no webhook — a polling worker settles on-chain transfers, so TON works without `WEBHOOK_URL` too.

## Next steps

- [Environment Variables](environment-variables.md)
- [Architecture](architecture.md)
- [FAQ](faq.md)
- [Contributing](../CONTRIBUTING.md)
