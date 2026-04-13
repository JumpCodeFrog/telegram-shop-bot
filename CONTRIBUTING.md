# Contributing

## Requirements

- Go 1.24+
- Redis 7+ (or Docker)
- Docker (optional)

## Quick Start

```bash
make setup    # copies .env.example → .env, creates data/ directories
# edit .env: set BOT_TOKEN and ADMIN_IDS
make run      # start the bot
```

## Development

```bash
make test           # run all tests
make coverage       # tests + coverage report
make build          # compile all binaries
make lint           # go vet
make seed           # load demo products into the database
make preflight      # health check (no live token needed)
make dev            # hot-reload via Docker + Air
```

## Docker

```bash
make docker-up      # start bot + Redis in containers
docker compose logs -f bot
make docker-down
```

## Adding a New Language

Translations live in `locales/<lang_code>.json` where `lang_code` is an IETF tag
(e.g. `fr`, `pt`, `ja`). The bot automatically picks the file that matches the user's
Telegram language setting.

Steps:
1. Copy the English translation as a starting point:
   ```bash
   cp locales/en.json locales/fr.json
   ```
2. Translate all values (keys must stay in English and unchanged).
3. Verify JSON is valid:
   ```bash
   python3 -m json.tool locales/fr.json > /dev/null && echo "OK"
   ```
4. Run the bot locally and send `/start` with your Telegram language set to the new locale.
5. Open a pull request — translations are warmly welcomed!

> **Note:** Do not rename or add new keys — the bot falls back to `en` for any missing key.

## Environment

Copy `.env.example` to `.env` and fill in the required values.  
See [`docs/environment-variables.md`](docs/environment-variables.md) for a full reference.

```bash
cp .env.example .env
```

## Code Style

- Run `gofmt -w` on changed files before committing.
- All new code should have tests where practical.
- Follow the existing package structure:
  - Business logic → `internal/service/` or `internal/shop/`
  - Storage access → `internal/storage/`
  - Telegram handlers → `internal/bot/`
  - Background jobs → `worker/`
- Keep all locale files in sync when adding new i18n keys.

## Submitting Changes

1. Fork the repository.
2. Create a feature branch.
3. Run `make test` — must be green.
4. Open a pull request against `main`.

