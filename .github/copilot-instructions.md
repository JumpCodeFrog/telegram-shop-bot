# Copilot Instructions — Telegram Shop Bot

## Build, Test & Lint

```bash
go build ./...          # build all packages
go test ./...           # run all tests
go test ./internal/storage/...   # run tests for a single package
go vet ./...            # lint
gofmt -w <file>         # format before committing

make run                # start the bot (go run ./cmd/bot)
make seed               # load demo data into the DB
make preflight          # validate env, DB, Redis, webhook config
```

Property-based tests use `pgregory.net/rapid`. Storage tests use `:memory:` SQLite — no external services needed.

## Architecture

The bot is a single binary (`cmd/bot`). Dependency injection flows from `cmd/bot/main.go` down into `internal/bot.New(...)`.

```
cmd/bot          → entry point; wires config, DB, FSM, Redis, metrics, starts bot
internal/
  config/        → env-var loading (godotenv + os.Getenv)
  storage/       → SQLite DB, store interfaces, Redis & in-memory FSM, models, migrations
  shop/          → CatalogService, CartService, OrderService (domain logic over storage)
  service/       → I18nService, MetricsService, ReferralService, ExchangeService, payment helpers
  payment/       → StarsPayment and CryptoBotPayment adapters
  bot/           → update routing, command handlers, keyboard builders, middleware chain
    middleware/  → AdminOnly, Auth (separate package)
worker/          → background goroutines: CartRecoveryWorker, BackupWorker, CryptoBot polling
locales/         → ru.json, en.json (flat key→string maps)
migration/       → reference SQL; actual embedded migrations live in internal/storage/migrations/
```

**Update routing** (`internal/bot/handlers.go`): `route()` dispatches to `routeMessage()` or `handleCallback()`. Multi-step admin dialogs (e.g., add-product) check FSM state *before* command dispatch, so in-progress dialogs intercept plain messages.

**FSM (Finite State Machine)**: Multi-step dialogs store per-user state via `storage.FSMStore`. Redis is the primary backend (`RedisFSMStore`); `MemoryFSMStore` is the automatic fallback when Redis is unavailable. Both implement the same interface.

**Transport**: Polling vs. webhook is auto-detected at startup — set `WEBHOOK_URL` in `.env` for webhook mode, leave blank for polling.

**Product caching**: `CachedProductStore` wraps `SQLProductStore` with an optional Redis layer (1 h TTL).

## Key Conventions

**Module name**: `shop_bot` (not the repo name).

**Layer placement**:
- Business/domain logic → `internal/service/` or `internal/shop/`
- DB reads/writes → `internal/storage/`
- Telegram message/keyboard rendering → `internal/bot/`
- Payment provider integration → `internal/payment/`

**Error handling**: wrap with `fmt.Errorf("context: %w", err)`. Sentinel errors are declared in `internal/storage/db.go`: `ErrNotFound`, `ErrOrderStatusConflict`, `ErrProductOutOfStock`, `ErrEmptyCart`.

**Logging**: `log/slog` throughout. No third-party logger.

**Storage interfaces**: all stores are defined as interfaces in `internal/storage/interfaces.go`. Use these interfaces in function signatures, not the concrete types.

**Order status transitions**: `pending → paid → delivered`; `pending → cancelled`. `UpdateOrderStatus` enforces `fromStatus` to prevent races — always provide the expected current status.

**i18n**: call `i18n.T(languageCode, "key")` where `languageCode` comes from `tgbotapi.User.LanguageCode`. Falls back to `"en"` automatically. Add new keys to *both* `locales/ru.json` and `locales/en.json`; the `TestBotLocaleFilesCoverAllTranslationKeys` test enforces coverage.

**No CGO**: SQLite is `modernc.org/sqlite` (pure Go). Do not introduce cgo dependencies.

**Middleware signature**: `func(handler func(tgbotapi.Update)) func(tgbotapi.Update)`. Chain with `bot.Chain(...)` helpers in `middleware.go`.

**Comments**: English only, and only where genuinely clarifying. Prefer self-documenting names.

## Стиль работы

Отвечай на **русском языке**. Ты — опытный senior Go-разработчик и личный технический помощник. Мы пишем код вместе, весь код принадлежит только мне.

1. **Никогда** не упоминай ИИ, Copilot, GPT, LLM, "я предлагаю", "я думаю", "как ИИ", и не указывай, что код сгенерирован. Говори исключительно как соавтор-человек: "Давай добавим...", "Лучше сделать так...", "Я бы здесь изменил на...".

2. Перед любым изменением кратко опиши, что именно собираемся сделать и почему.

3. Предлагай код только после явного согласия или когда попрошу "напиши".

4. Если предлагаешь рефакторинг — показывай diff-стиль или конкретные участки, которые меняем.

5. При создании git-коммитов автор — **только я**. Никаких `Co-authored-by` и упоминания сторонних авторов в сообщениях коммитов.
