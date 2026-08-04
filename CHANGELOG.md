# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

---

## [2.0.0] — 2026-08-04

### ⚠️ Breaking Changes

- **`OrderService.ConfirmPayment` signature** — was `ConfirmPayment(ctx, orderID, method, paymentID) error`, now returns `(*shop.PaymentOutcome, error)`. The outcome reports everything that happened during confirmation (points awarded, level-up, referral bonus, personal promo issued); sending user-facing messages based on it is the bot layer's job. All three confirmation paths (Stars `successful_payment`, CryptoBot webhook, CryptoBot polling) were updated.
- **Worker constructors changed:**
  - `NewCryptoBotPollingWorker(crypto, orders PaymentConfirmer, notify func(ctx, *shop.PaymentOutcome), interval)` — the poller now confirms through `OrderService` (same loyalty/referral/cache side effects as webhooks) and reports outcomes via callback instead of writing to the order store directly.
  - `NewCartRecoveryWorker(bot, cart, promos, users, i18n, metrics, interval, …)` — gained `*service.I18nService` (reminders in the user's language) and `*service.MetricsService` (gauges).
  - `NewLoyaltyWorker(store, svc, rdb, bot, i18n, users)` — gained a `storage.UserStore`: the notification language is read from the DB instead of hardcoded `"ru"`.
- **Default language is now `en`** — an empty or unknown Telegram `language_code` falls back to English (previously Russian), as the docs always promised. Language tags are normalized to their primary subtag (`ru-RU` → `ru`, `zh-hans-CN` → `zh`).
- **WAL journal mode** — the SQLite DSN now sets `journal_mode=WAL`, `busy_timeout=5000`, `foreign_keys=1`. The data directory gains `*.db-wal` / `*.db-shm` files (gitignored); naive `cp shop.db` backups are no longer safe — use the built-in `VACUUM INTO` backups (see Reliability).
- **Dead code removed** — `internal/service/payment*.go` stub adapters (`VerifyPayment` → always `true`), the unused duplicate `internal/bot/middleware/admin.go`, the root `migration/` directory, and 17 dead i18n keys (including the balance leftovers and the `$0.00` line in the profile).

### Critical Fixes

- **False payment confirmation in the CryptoBot polling worker** — the 30-second poller fetched `active` invoices and unconditionally flipped orders `pending → paid`, confirming *unpaid* orders. Now only invoices with `status == "paid"` are processed; the rest are logged at debug level.
- **Pre-checkout approved without validation** — Stars `pre_checkout_query` was always answered `ok=true`. Now the referenced order must exist, belong to the paying user, still be `pending`, and its `TotalStars` must equal the invoice amount — otherwise the checkout is declined with a localized reason.
- **Stock check ignored quantity** — `CreateFromCart` only required `stock > 0`; ordering 5 of an item with 1 in stock passed. Now `stock >= quantity` is enforced per item and violations fail with `*shop.ErrInsufficientStock{ProductName, Have, Want}` and a human message (`error_insufficient_stock`).
- **Graceful shutdown** — every background goroutine (workers + HTTP server) is registered in a named worker group; shutdown stops updates, shuts the HTTP server down, drains workers for up to 10 s (stuck workers are logged by name) and only then closes the DB.
- **Loyalty worker hardening** — no more type-assertion panics on malformed Redis Stream messages (garbage is logged and XAck'ed, not retried forever); `XReadGroup` failures back off exponentially 1 s → 30 s; `XAck` errors are logged.
- **Stale product cache after payment** — confirming a payment now invalidates the cached products of the order (`ProductCacheInvalidator`; no-op without Redis). Previously the cached stock could stay wrong for up to an hour.
- **CryptoBot webhook retry storm** — idempotent repeats (`ErrOrderStatusConflict` / `ErrNotFound`) now return HTTP 200 instead of 500, so CryptoBot stops re-delivering forever; the webhook secret is compared with `subtle.ConstantTimeCompare`.
- **Subscriptions were never persisted** (found by the E2E suite) — migration `014` declared `subscriptions.user_id REFERENCES users(id)`, but the code stores the buyer's *Telegram* ID there (same convention as `orders.user_id`), so with `foreign_keys=1` every real insert failed. Migration `016_subscriptions_user_fk.sql` rebuilds the table without the bogus FK, keeping data and indexes.
- **Referral link never attached the referrer** (found by the E2E suite) — `ReferralStore.SetReferrer` ran `UPDATE users … WHERE id = ?` with a Telegram ID, so `referred_by` was never set. It now addresses the user by `telegram_id`; the first referrer wins, and repeated `/start` deep links no longer inflate `referral_stats`.

### Loyalty & Referrals (now actually working)

- **Cashback on every paid order** — `ConfirmPayment` awards `CalculateCashback(level, totalUSD)` points (1–10 % by level), records the transaction and runs `CheckAndUpgradeLevel`; the buyer gets `loyalty_points_awarded` / `loyalty_level_up` messages.
- **Referral bonus on the first paid order** of an invited user: the referrer receives **100 points** (`referral_bonus_referrer`), the newcomer receives a personal one-off promo code `REF-XXXXXXXX` (−10 %, 30 days, `referral_welcome_promo`). Idempotent even under concurrent confirmations: `referral_awards` keyed by `referred_user_id` + `INSERT OR IGNORE`.
- **Personal promo codes** — `promo_codes.bound_user_id`: a bound code is visible and applicable only to its owner (NULL = public, behavior unchanged).
- **`/referral` screen** (also `ref:open` callback and menu/profile button) — personal link `t.me/<bot>?start=ref_<code>`, invited count, total points earned, a "Share" button (`switch_inline_query`).
- **Profile** shows real points/level and progress to the next level; referral codes are generated with `crypto/rand`.

### UX / Main Menu

- **2-column main menu**: [🛍 Catalog | 🔍 Search], [🛒 Cart | ❤️ Wishlist], [📦 Orders | 👤 Profile], [🎁 Referral | 🆘 Support], [📄 Terms].
- **`/search`** — every hit is a button opening the product card; Back | Menu row on all branches (results, empty, hint).
- **`/wishlist`** — each item is a product button plus a ✖ remove button; an empty wishlist offers a catalog button.
- **6 new configurable button keys** — `menu_search`, `menu_wishlist`, `menu_referral`, `menu_terms`, `catalog_category`, `catalog_product` (categories and product lists are now styled independently of the catalog menu button); `/btnstyle` now manages 18 keys, and `product_wish` is actually applied on the product card.
- **Navigation fixes** — `back:` targets `wishlist` and `search`; Back from orders/support/terms leads to the menu; `handleCallback` is nil-safe for callbacks without an attached message.
- **`SetMyCommands`** now registers `/mysubs`, `/wishlist` and `/referral` too.

### Reviews & Ratings

- **Rating request after delivery** — once an admin marks an order delivered, the buyer gets a 1–5 ⭐ row (`review:<orderID>:<rating>`), then an optional free-text step (FSM, with Skip).
- **Only verified buyers** can rate (a delivered order with the product is required); one review per product per user — re-rating replaces via upsert.
- **Product card** shows `⭐ 4.7 (12)` when reviews exist and a "Reviews" button (`review:list:<productID>`) with the last 3 texts.
- **`/reviews` (admin)** — the 10 most recent reviews with delete buttons (`review:del:<id>`).

### Product Photos

- **Admin wizard accepts photos as Telegram messages** (largest `PhotoSize` is stored as `file_id`); URL input remains an alternative; up to **10 photos** per product; edit mode manages the photo list with per-photo delete buttons.
- **Gallery on the product card** — one photo renders as before; several photos are sent as a media group with the card (and its buttons) as a separate message. Inline mode uses the first photo.

### Stars Subscriptions (recurring)

- **30-day subscription products** — a product with `sub_period_days=30` is sold as a recurring Telegram Stars subscription: the invoice is sent via raw `sendInvoice` with `subscription_period=2592000` (tgbotapi v5 does not know the field). Subscription products are payable with Stars only; crypto is hidden at checkout and rejected with `sub_stars_only`.
- **`/mysubs` command** — lists active subscriptions with expiry dates; each has a cancel button (`sub:cancel:<id>`) that calls raw `editUserStarSubscription{is_canceled:true}` and marks the row `canceled` locally (access remains until the paid period ends).
- **Subscription bookkeeping** — `successful_payment` for a subscription order upserts a `subscriptions` row; `expires_at` comes from the raw update's `subscription_expiration_date` (webhook mode), falling back to now + 30 days when Telegram's field is unavailable (polling mode).
- **`worker/subscription.go`** — hourly worker: marks overdue subscriptions `expired` and sends a one-shot `sub_expiring_soon` reminder 72h before expiry (`MarkReminded` only after a successful send, so failed reminders are retried).
- **Admin wizard** — the add-product dialog got a final step: regular product vs. 30-day subscription.
- ⚠️ **Recurring payments require live verification** («требует проверки в бою»): renewal `successful_payment` updates, `subscription_expiration_date` delivery, and `editUserStarSubscription` behavior cannot be exercised against the real Bot API from tests.

### Mini App + REST API

- **`web/app/`** — a vanilla-JS Mini App embedded into the binary (no build step): catalog → product card (photos, price, rating) → cart (+/−/remove) → checkout via `Telegram.WebApp.openInvoice` (Stars) or `openLink` (crypto). Theme from `themeParams`, strings served by the bot (`GET /api/i18n?lang=`).
- **REST API `/api/*`** (`internal/webapi/`) — `GET /api/me`, `GET /api/catalog`, `GET /api/products?category=&page=`, `GET /api/products/{id}` (with rating and photos), `GET/POST/DELETE /api/cart`, `POST /api/checkout {method: stars|crypto, promo?}` → `{invoice_link}`, `GET /api/photo/{file_id}` (proxies `getFile`). JSON errors are `{"error":"<i18n key>"}`; request bodies are capped at 64 KB.
- **Authentication** — `Authorization: tma <initData>` validated per the Telegram spec (secret = HMAC-SHA256(key="WebAppData", msg=botToken); sorted data-check-string; `auth_date` TTL 1 hour), covered by the official test vector.
- **Opt-in via `WEBAPP_URL`** — when set (must be public HTTPS), `/app` (static) and `/api/` are mounted on the existing :8080 server and the bot's menu button becomes a `web_app` button. Without it nothing is mounted and the bot works exactly as before (a warning is logged).

### Analytics & Metrics

- **`/analytics`** — 14-day revenue chart with text bars (`▇`), top-10 buyers (total spent, order count) and a promo-code report (uses, total discount); fully localized via `admin_*` keys.
- **`/export_orders [from] [to]`** — CSV export now accepts an optional date range (`/export_orders 2026-01-01 2026-02-01`); format errors are reported in a human way.
- **Live Prometheus metrics** — `OrdersCreated` incremented in `CreateFromCart`, `ActiveCarts` gauge recomputed by the cart-recovery worker each tick, `CartsAbandoned` incremented when a reminder is sent; new panels in `monitoring/grafana_dashboard.json`.

### Admin Notifications via Topics

- **`notifyAdmins(ctx, kind, text)`** (`internal/bot/notify.go`) — with `ADMIN_GROUP_ID` configured, order events (new / paid / delivered) go to the supergroup, optionally routed into forum topics via `TOPIC_ORDERS_NEW` / `TOPIC_ORDERS_PAID` / `TOPIC_ORDERS_DELIVERED`; without it, the old behavior remains (DM to every admin).
- Group notifications use English i18n keys (`admin_order_new`, `admin_order_paid_*`); the last hardcoded Russian admin texts are gone.

### Internationalisation

- **All 5 locales complete** — `ru`, `en`, `es`, `de`, `zh` now have full key parity, including the admin panel (`admin_*` keys, admin's own `language_code` is respected) and worker messages (cart recovery, loyalty level-up in the recipient's DB language). The parity test covers all 5 locales plus a reverse "key used in code exists in ru.json" check.
- Truncated es/de/zh translations (terms, onboarding, promo, error, paysupport texts) were completed properly, not machine-stubbed.

### Reliability & Ops

- **Backups without the sqlite3 CLI** — the daily backup runs `VACUUM INTO 'backups/shop_YYYYMMDD_HHMMSS.db'` on the live connection pool, so it works in scratch Docker images; rotation keeps the **7 newest** files.
- **HTTP server & workers** — one shared HTTP server on :8080 serves `/health`, `/metrics`, Telegram/CryptoBot webhooks, and (opt-in) `/app` + `/api/`; everything shuts down in order (see Critical Fixes).

### Testing

- Property test (rapid): double/concurrent `ConfirmPayment` of one order ⇒ exactly one stock decrement, one points award, one `referral_awards` row.
- Unit tests: polling worker (paid/active/mixed invoices), all four pre-checkout rejections + happy path, Mini App `initData` validation against the official Telegram vector, i18n tag normalization, backup rotation, review/subscription stores, WAL pragma.
- End-to-end scenarios (`internal/bot`, mock Bot API): full purchase flow with points and review, referral first-purchase bonus (and no second bonus), subscription payment bookkeeping.

### Migrations summary

| File | Description |
|------|-------------|
| `012_reviews.sql` | `reviews` table — rating 1..5, optional text, `UNIQUE(product_id, user_id)` |
| `013_product_photos.sql` | `product_photos` — Telegram `file_id` gallery, `ON DELETE CASCADE` |
| `014_subscriptions.sql` | `products.sub_period_days` + `subscriptions` table with indexes |
| `015_referral_awards.sql` | `promo_codes.bound_user_id` (personal promos) + `referral_awards` idempotency table |
| `016_subscriptions_user_fk.sql` | Rebuilds `subscriptions` without the incorrect `user_id → users(id)` FK (column holds Telegram IDs) |

---

## [1.2.0]

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
