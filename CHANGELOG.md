# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

---

## [Unreleased] — v1.2.0

### Admin: Button Style Customization

- **`/btnstyle` command** — new admin command that opens an interactive inline menu showing all 12 configurable button keys with their current style indicators (🔵🟢🔴⬜). Tapping any button opens a style picker with four options (Primary, Success, Danger, Default); the change is applied immediately and persisted to SQLite.
- **`button_styles` table** (migration `011_button_styles.sql`) — stores per-button style overrides as `key TEXT PRIMARY KEY, style TEXT`. Automatically created on first startup via the existing migration system.
- **`UISettingsStore` interface** (`internal/storage/ui_settings.go`) — `GetButtonStyle`, `SetButtonStyle`, `ListButtonStyles` methods backed by `SQLUISettingsStore`.
- **In-memory style cache** (`Bot.uiStyles sync.Map`) — loaded from DB once at startup via `reloadButtonStyles(ctx)`. Single-key updated immediately when admin changes a style — no restart needed.
- **`styledBtn(key, text, data, defaultStyle)` method** — all 12 semantic buttons across the UI now resolve their color through this helper. If no override is stored for a key, the default style (same as before) is used transparently. Nil-safe for test fixtures.
- **Button key constants** (`BtnKeyMenuCatalog`, `BtnKeyMenuCart`, `BtnKeyMenuOrders`, `BtnKeyMenuProfile`, `BtnKeyMenuSupport`, `BtnKeyProductAdd`, `BtnKeyProductWish`, `BtnKeyCartCheckout`, `BtnKeyCartRemove`, `BtnKeyPayStars`, `BtnKeyPayCrypto`, `BtnKeyPayCancel`) — defined in `styled_keyboard.go`; each maps to a human-readable label via `ButtonKeyLabel()`.
- **`StyleEmoji()` helper** — returns 🔵/🟢/🔴/⬜ for a given `ButtonStyle`; used in both the admin list view and the inline style picker.
- **Callback routing** — three new admin callback prefixes handled in `handleCallback`: `admin:btnlist`, `admin:btnpick:<key>`, `admin:setstyle:<key>:<style>`.

### UX / Navigation

- **Bot API 9.4 colored buttons** (`styled_keyboard.go`) — full support for `style` field in inline keyboard buttons via raw API calls. `BtnPrimary` (blue), `BtnSuccess` (green), `BtnDanger` (red) used across all screens.
- **"🏠 Menu" button everywhere** — all screens (catalog, product, cart, checkout, orders, profile, support, terms, payment) now have a persistent "go to main menu" button via `back:menu` callback.
- **Main menu redesign** — catalog button (primary/blue), cart button (success/green) for visual hierarchy.
- **Category & product lists** — primary-style buttons for categories and products; back/menu nav row on every screen.
- **Checkout flow** — confirm order button is success/green; cancel order is danger/red; pay Stars is primary/blue; pay crypto is success/green.
- **`sendMainMenu` helper** — extracted from `handleStart`; reused for `/start`, callback `back:menu`, and any screen's "🏠" button.
- **`setChatMenuButton`** — sets the persistent "/" commands button in the Telegram input bar at bot startup.
- **`SetMyCommands`** — registers `/start`, `/catalog`, `/cart`, `/orders`, `/search`, `/profile`, `/support` in the Telegram commands menu.
- **Smooth photo transitions** — `onProductSelected` now uses raw `editMessageMedia` API with styled keyboard; falls back to `sendPhoto` for new messages.
- **`toast()` helper** — non-blocking `answerCallbackQuery` popups for cart-add and wishlist-toggle confirmations (no blocking alert).
- **`ForceReply` for promo input** — promo code entry uses `ForceReply` with placeholder `PROMO123`.
- **Payment button amounts** — Stars and crypto payment buttons show the actual amount: `⭐ Pay Stars (100 ⭐)`, `💎 Pay Crypto ($1.50)`.

### Performance

- **DB indexes** — added 7 indexes on frequently queried columns (`orders.user_id`, `orders.status`, `order_items.order_id`, `cart_items.user_id`, `products.category`, `wishlist.user_id`, `users.ref_code`) via migration `008_add_indexes.sql`. Significant speedup on medium/large datasets.

### Bug Fixes

- **Wishlist notification dedup** — each price-drop and back-in-stock notification is now sent exactly once per event cycle. Added `price_drop_notified_at` / `back_in_stock_notified_at` columns (migration `009_wishlist_notif_tracking.sql`) and 4 new store methods (`MarkPriceDropNotified`, `ClearPriceDropNotified`, `MarkBackInStockNotified`, `ClearBackInStockNotified`).
- **CryptoBot polling** — fixed `GetInvoices` call: was polling `"paid"` (entire history, grows unboundedly); now polls `"active"` (only outstanding invoices that may have been paid but missed via webhook).
- **Order `updated_at`** — `UpdateOrderStatus` and `CancelOrder` now set `updated_at = CURRENT_TIMESTAMP` on every status transition. Migration `010_orders_updated_at.sql` adds the column and backfills it from `created_at`.

### Internationalisation

- **i18n coverage** — removed all hardcoded Russian strings from bot handlers:
  - Stars payment receipt (`stars_receipt`)
  - Admin notification on Stars payment (`admin_order_paid_stars`)
  - Admin notification on crypto payment (`admin_order_paid_crypto`)
  - Loyalty level-up message (`loyalty_level_up`)
  - VIP gift notification (`loyalty_vip_gift`)
- Added `Tf(lang, key, args...)` helper to `I18nService` for format-string keys.
- Crypto payment webhook now resolves the buyer's language before sending the confirmation message.

### Code Quality

- **Handlers split** — monolithic `handlers.go` (1543 → 319 lines) split into 9 themed files:
  - `handlers_start.go` — `/start`, `/cancel`, `/help`
  - `handlers_catalog.go` — catalog browsing, category/product selection
  - `handlers_cart.go` — cart view, add/remove/qty changes, checkout
  - `handlers_checkout.go` — promo input, order confirm, payment method keyboard
  - `handlers_payment.go` — Stars and crypto payment flows, pre-checkout
  - `handlers_orders.go` — order history
  - `handlers_search.go` — `/search` command
  - `handlers_wishlist.go` — wishlist toggle and view
  - `handlers_support.go` — support and terms pages
  - `handlers_inline.go` — inline mode catalog (new)
  - `handlers.go` — routing core only (`route`, `routeMessage`, `handleCallback`, `send`, helpers)
- **Worker interfaces** — `OnboardingWorker` and `WishlistWatcherWorker` now depend on minimal local interfaces (`onboardingUserStore`, `wishlistStore`) instead of concrete `*storage.SQL*` types. Decouples workers from storage implementation.
- **Inline catalog** (`handlers_inline.go`) — added `update.InlineQuery` branch in `route()`; `handleInlineQuery` returns up to 20 matching active in-stock products as `InlineQueryResultCachedPhoto` (with photo) or `InlineQueryResultArticleHTML` (without). `AllowedUpdates` in polling config updated to include `inline_query`.
- **Context propagation** — all `context.Background()` calls in handlers replaced with `handlerCtx()` (30 s timeout). Added `handlerCtx()` helper in `bot.go`.
- **Dead code removed** — deleted `internal/storage/balance.go` (internal balance/USD top-up feature, unused). Removed `Transaction` type and `PaymentMethodBalance` constant from `models.go` and `ui_text.go`.
- **LoyaltyWorker** — constructor now receives `*service.I18nService`; level-up messages use i18n instead of hardcoded Russian.

### Security & Reliability

- **HTTP server timeouts** — added `ReadTimeout: 10s`, `WriteTimeout: 10s`, `IdleTimeout: 60s` to the webhook HTTP server.
- **Request body limit** — `http.MaxBytesReader` (1 MB) applied in both webhook handlers to prevent oversized payload attacks.

### CI / Tooling

- **golangci-lint** — added `.golangci.yml` (errcheck, govet, staticcheck, gosimple, unused, ineffassign, misspell) and a `lint` job to `.github/workflows/ci.yml`.
- **Coverage reporting** — `go test -coverprofile=coverage.out` runs in CI; `coverage.out` uploaded as build artifact.
- **goreleaser** — added `.goreleaser.yml`; Linux amd64/arm64 binaries + Docker image pushed to `ghcr.io` on `v*` tag push via `.github/workflows/release.yml`.

### Open-Source Developer Experience

- **`.env.example`** fully rewritten with all variables, section headers, and inline comments. Covers bot, payment, security, Redis, monitoring, outbound webhook, and localisation settings.
- **Hot-reload dev environment** — `docker-compose.dev.yml` + `Dockerfile.dev` + `.air.toml` for instant live-reload during development with `make dev`.
- **`make setup`** — bootstrap target: copies `.env.example` → `.env`, creates `data/` and `backups/` directories.
- **Documentation** (`docs/`):
  - `getting-started.md` — step-by-step setup guide (local, Docker, Telegram configuration)
  - `environment-variables.md` — reference for every environment variable with defaults and descriptions
  - `faq.md` — common questions for users and contributors
  - `architecture.md` — component diagram, request flow, data model overview (Mermaid-compatible ASCII)
- **Localisation additions** — `locales/es.json` (Spanish), `locales/de.json` (German), `locales/zh.json` (Chinese). All keys fully translated. Bot auto-selects locale from the user's Telegram `LanguageCode`.
- **`CONTRIBUTING.md`** rewritten — quick-start flow, hot-reload guide, how to add a new locale, code style rules, PR checklist.
- **GitHub Issue Templates** (`.github/ISSUE_TEMPLATE/`):
  - `bug_report.md` — structured bug report form
  - `feature_request.md` — feature / improvement proposal form
  - `question.md` — help & question form

### Integrations

- **Outbound webhooks** (`internal/service/outbound_webhook.go`) — fire `order.paid` and `order.delivered` events to an external URL. Configurable via `OUTBOUND_WEBHOOK_URL` and `OUTBOUND_WEBHOOK_SECRET` env vars. Requests include `X-Webhook-Secret` header and a JSON payload with order metadata. Async (non-blocking). Triggered after Stars payment, CryptoBot payment confirmation, and admin "set delivered" action.

### Migrations summary

| File | Description |
|------|-------------|
| `008_add_indexes.sql` | 7 performance indexes |
| `009_wishlist_notif_tracking.sql` | Notification dedup columns on wishlist |
| `010_orders_updated_at.sql` | `updated_at` column on orders |

---

## [1.1.0] — 2025-05-27

- Production-ready improvements (see git tag `v1.1.0`)

## [1.0.0] — Initial public release

- Initial public release of Telegram Shop Bot
