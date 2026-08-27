# Architecture

## Overview

Telegram Shop Bot follows **Clean Architecture** with a strict separation between layers.  
Dependencies point inward: `bot` / `webapi` → `shop` / `service` → `storage`.

```
cmd/bot/main.go          Entry point — wires everything, owns the worker group & graceful shutdown
│
├── internal/bot/        Telegram layer (handlers, keyboards, notifications, webhook)
│   ├── handlers.go      Router + dispatch (routeMessage, handleCallback)
│   ├── handlers_start.go     /start, /help, /cancel, 2-column main menu
│   ├── handlers_catalog.go   Catalog browsing, product card (rating, photo gallery)
│   ├── handlers_cart.go      Cart view, add/remove/qty
│   ├── handlers_checkout.go  Promo input, order confirm, payment method keyboard
│   ├── handlers_payment.go   Stars & crypto payment flows, successful_payment
│   ├── handlers_orders.go    Order history
│   ├── handlers_search.go    /search with product buttons
│   ├── handlers_wishlist.go  /wishlist with remove buttons
│   ├── handlers_referral.go  /referral screen (ref: callbacks)
│   ├── handlers_reviews.go   Review flow (review: callbacks, FSM text step)
│   ├── handlers_subs.go      /mysubs + sub: callbacks (cancel via editUserStarSubscription)
│   ├── handlers_support.go   Support, /paysupport, terms
│   ├── handlers_inline.go    Inline mode catalog
│   ├── admin.go         Admin commands (product wizard with photos & subscriptions,
│   │                    /analytics, /export_orders, /reviews, /btnstyle, …)
│   ├── profile.go       Profile with real points/level and progress
│   ├── notify.go        notifyAdmins — DM fan-out or ADMIN_GROUP_ID (+forum topics)
│   ├── styled_keyboard.go    Bot API 9.4 colored buttons + 18 configurable keys
│   ├── bot.go           Bot struct + constructor + Run loop + menu button (web_app)
│   └── webhook.go       Telegram & CryptoBot webhook receivers (idempotent replies)
│
├── internal/webapi/     Mini App REST API (mounted only when WEBAPP_URL is set)
│   ├── auth.go          `Authorization: tma <initData>` HMAC validation (1h TTL)
│   └── handlers.go      /api/me, catalog, products, cart, checkout, photo, i18n
│
├── web/                 Mini App frontend, embedded into the binary
│   ├── embed.go         go:embed of app/
│   └── app/             index.html + app.js + style.css (vanilla JS, no build step)
│
├── internal/shop/       Business logic (no Telegram, no DB details)
│   ├── order.go         OrderService: create, ConfirmPayment → *PaymentOutcome
│   │                    (loyalty points, level-up, referral bonus, cache invalidation)
│   ├── catalog.go       CatalogService: list, search, exchange rate
│   └── cart.go          CartView aggregation
│
├── internal/service/    Cross-cutting services
│   ├── i18n.go          Translation (T / Tf / Dict), tag normalization, en fallback
│   ├── loyalty.go       Loyalty points + levels (CalculateCashback 1–10 %)
│   ├── referral.go      Referral code logic (crypto/rand codes)
│   ├── metrics.go       Prometheus counters / gauges / histograms
│   ├── exchange.go      USD↔Stars rate
│   └── outbound_webhook.go   order.paid / order.delivered events to external URL
│
├── internal/storage/    Data access (SQLite, WAL mode)
│   ├── db.go            Connection (WAL, busy_timeout, foreign_keys) + auto-migrations
│   ├── orders.go        OrderStore (atomic status transitions, stock decrement)
│   ├── products.go      ProductStore
│   ├── cached_products.go    Redis product cache + invalidation
│   ├── catalog.go       CatalogStore
│   ├── cart.go          CartStore
│   ├── user.go          UserStore
│   ├── wishlist.go      WishlistStore
│   ├── promos.go        PromoStore (+CreatePersonal for bound promo codes)
│   ├── referral.go      ReferralStore (AwardFirstPurchase idempotency)
│   ├── reviews.go       ReviewStore (upsert, ProductRating avg/count)
│   ├── product_photos.go     ProductPhotoStore (max 10 per product)
│   ├── subscriptions.go SubscriptionStore (Upsert, ExpireOverdue, DueForReminder)
│   ├── loyalty.go       Loyalty points/levels store
│   ├── analytics.go     Revenue by day, top buyers, promo usage
│   ├── ui_settings.go   UISettingsStore — button style overrides
│   ├── redis.go         Redis-backed FSM store
│   ├── memory_fsm.go    In-memory FSM fallback (no Redis needed)
│   └── migrations/      SQL migration files 001–018 (run in order at startup)
│
├── internal/payment/    Payment provider adapters
│   ├── stars.go         Telegram Stars (validated pre-checkout, subscription_period)
│   └── cryptobot.go     CryptoBot (USDT/TON)
│
├── worker/              Background goroutines (all under one WaitGroup)
│   ├── wishlist.go      Price-drop & back-in-stock notifications
│   ├── loyalty.go       Level-up notifications from Redis Streams (backoff, safe parse)
│   ├── onboarding.go    24-hour onboarding nudge
│   ├── cart_recovery.go Abandoned-cart reminders (i18n) + ActiveCarts/CartsAbandoned metrics
│   ├── subscription.go  Hourly: expire overdue subs, 72h expiry reminders
│   ├── backup.go        Daily VACUUM INTO backups, keeps 7 newest
│   └── polling.go       CryptoBot missed-payment poller (paid invoices only)
│
├── locales/             Translation files — ru, en (fallback), es, de, zh
│
└── internal/config/     Environment variable loading & validation
```

---

## Data Flow — User places an order

```
User taps "🛒 Cart"
    → bot.handleCart()
    → shop.CartService.Get()          reads CartStore + ProductStore
    → renders cart message + keyboard

User taps "✅ Checkout"
    → bot.onOrderConfirm()
    → shop.OrderService.CreateFromCart()
        → per-item stock check (stock >= quantity, ErrInsufficientStock)
        → storage.OrderStore.CreateOrder()   (transaction)
        → storage.CartStore.ClearCart()
    → renders payment method selection (crypto hidden for subscription products)

User taps "⭐ Pay with Stars"
    → bot.onPayStars()
    → payment.StarsPayment.CreateInvoice()   (raw sendInvoice; subscription_period for subs)
    → Telegram sends pre_checkout_query
    → bot.handlePreCheckout()      validates: order exists, owner, pending, amount matches
    → Telegram sends successful_payment
    → bot.handleSuccessfulPayment()
    → shop.OrderService.ConfirmPaymentReceipt() → *PaymentOutcome
```

### PaymentOutcome pipeline

All three confirmation paths — Stars `successful_payment`, the CryptoBot webhook, and the
CryptoBot polling worker — converge on `OrderService.ConfirmPaymentReceipt`, which validates
the complete provider fact before entering the idempotent atomic settlement boundary
(`ErrOrderStatusConflict` on repeats) and returns a `*PaymentOutcome`:

```
ConfirmPaymentReceipt(ctx, validatedReceipt)
    → OrderStore atomic settlement (pending → paid)    ledger, stock, subscription, promo (one tx)
    → invalidate Redis cache for the ordered products  (no-op without Redis)
    → loyalty: points = CalculateCashback(level, totalUSD) → AddPoints + CheckAndUpgradeLevel
    → referral: first paid order of a referred user?
        → INSERT OR IGNORE referral_awards             (idempotency gate)
        → +100 points to the referrer
        → personal promo REF-XXXXXXXX (−10 %, 30 days, bound_user_id) for the buyer
    → returns PaymentOutcome{Order, PointsAwarded, NewLevel,
                             ReferralReferrer, ReferrerPoints, NewUserPromo}

Bot layer (NotifyPaymentOutcome):
    → receipt to the buyer, points / level-up messages
    → referral bonus message to the referrer, welcome promo to the buyer
    → notifyAdmins(AdminEventOrderPaid, …)             group topic or DM fan-out
    → subscription order → atomic order/payment/subscription settlement
      (immutable product/period snapshot, charge id, expires_at)
```

---

## Data Flow — CryptoBot payment

```
User taps "💎 Pay with Crypto"
    → bot.onPayCrypto()
    → payment.CryptoBotPayment.CreateInvoice()
    → CryptoBot sends webhook POST /cryptobot-webhook
    → bot.handleCryptoBotWebhook()      constant-time secret check
    → shop.OrderService.ConfirmPaymentReceipt() → outcome messages (see above)
    → idempotent repeats answer HTTP 200 (no retry storms)

Fallback (missed webhook):
    worker.CryptoBotPollingWorker  (every 30s)
    → fetches paid invoices via bounded CryptoBot API windows
    → confirms ONLY invoices with status == "paid"
    → same ConfirmPaymentReceipt + outcome pipeline as the webhook
```

---

## Data Flow — Mini App

```
User taps the bot's menu button (web_app, set when WEBAPP_URL is configured)
    → GET /app/            embedded static frontend
    → frontend sends Authorization: tma <initData> to /api/*
    → webapi.Authenticator validates HMAC (secret = HMAC_SHA256("WebAppData", bot_token),
      auth_date TTL 1h)
    → GET /api/me | /api/catalog | /api/products | /api/cart …
    → POST /api/checkout {method: stars|crypto, promo?}
        → shop.OrderService.CreateFromCart()   (same service as the bot)
        → Stars: raw createInvoiceLink (+subscription_period)  → {invoice_link}
        → crypto: CryptoBot pay URL
    → Telegram.WebApp.openInvoice(link) — payment then flows through the
      normal successful_payment / webhook confirmation pipeline
```

---

## Database Schema (simplified)

```
users            id, telegram_id, username, first_name, language_code,
                 loyalty_pts, loyalty_level, referral_code, referred_by

categories       id, name, description

products         id, category_id, name, description, price_usd, price_stars,
                 stock, photo_url, is_digital, digital_content, is_active,
                 sub_period_days            — >0 marks a Stars subscription product

product_photos   id, product_id (CASCADE), file_id, sort_order   — up to 10 per product

orders           id, user_id, status, total_usd, total_stars,
                 payment_method, payment_id, discount_pct, promo_code,
                 created_at, updated_at

order_items      id, order_id, product_id, product_name, quantity, price_usd

cart_items       id, user_id, product_id, quantity

wishlist         user_id, product_id, price_at_added, stock_at_added,
                 price_drop_notified_at, back_in_stock_notified_at

promo_codes      id, code, discount (%), expires_at, max_uses, used_count,
                 category_id, bound_user_id   — non-NULL = personal one-user code

promo_usages     promo_id, user_id, order_id

reviews          id, product_id, user_id, order_id, rating 1..5, text, created_at
                 UNIQUE(product_id, user_id) — re-rating replaces via upsert

subscriptions    id, user_id, product_id, order_id, telegram_charge_id,
                 status (active|canceled|expired), expires_at, reminded_at,
                 created_at, updated_at, UNIQUE(user_id, product_id)

referral_awards  referred_user_id PK, referrer_id, points, promo_code, created_at
                 — INSERT OR IGNORE makes the first-purchase bonus idempotent

button_styles    key, style    — admin-configured button color overrides (Bot API 9.4)
```

---

## Button Style Customization

All inline keyboard buttons that carry semantic meaning have an admin-configurable style override stored in `button_styles`.

```
Admin runs /btnstyle
    → sendBtnStyleList() renders an overview of all 18 button keys + current style emoji
    → Admin taps a button → sendBtnStylePicker() shows 4 style options
    → Admin picks a style → onAdminSetStyle()
        → UISettingsStore.SetButtonStyle(ctx, key, style)   (SQLite upsert)
        → Bot.uiStyles.Store(key, style)                    (in-memory cache update)
        → Returns to overview via sendBtnStyleList()

On every keyboard render:
    b.styledBtn(BtnKeyXxx, text, data, defaultStyle)
        → looks up key in b.uiStyles (sync.Map, O(1))
        → returns button with admin style, or defaultStyle if not set
```

**Button keys and their default styles:**

| Key | Label | Default |
|-----|-------|---------|
| `menu_catalog` | 🛍 Каталог | primary |
| `menu_search` | 🔍 Поиск | default |
| `menu_cart` | 🛒 Корзина | primary |
| `menu_wishlist` | ❤️ Избранное | default |
| `menu_orders` | 📦 Заказы | default |
| `menu_profile` | 👤 Профиль | default |
| `menu_referral` | 🎁 Бонус за друга | success |
| `menu_support` | 🆘 Поддержка | default |
| `menu_terms` | 📄 Условия | default |
| `catalog_category` | 🗂 Категория в каталоге | primary |
| `catalog_product` | 🛍 Товар в списке | primary |
| `product_add` | 🛒 Добавить в корзину | success |
| `product_wish` | ❤️ Вишлист | default |
| `cart_checkout` | ✅ Оформить заказ | success |
| `cart_remove` | 🗑 Удалить | danger |
| `pay_stars` | ⭐ Telegram Stars | primary |
| `pay_crypto` | 💎 Crypto | success |
| `pay_cancel` | ❌ Отмена | danger |

---

## Internationalisation

Five locales ship with the bot: `ru`, `en`, `es`, `de`, `zh` — full key parity is enforced
by a test, including the admin panel (`admin_*` keys) and worker messages.

- Telegram language tags are normalized to the primary subtag: `ru-RU` → `ru`, `zh-hans-CN` → `zh`.
- Empty or unknown language falls back to **`en`**.
- The Mini App fetches its dictionary from `GET /api/i18n?lang=` (English-merged copy).

---

## Key Design Decisions

| Decision | Reason |
|----------|--------|
| SQLite (WAL, busy_timeout) | Zero-ops, safe under a 25-connection pool; WAL avoids writer starvation |
| Redis for FSM (optional) | Ephemeral dialog state with free TTL cleanup; in-memory fallback keeps the bot fully functional without Redis |
| Polling by default | No public URL needed for development; webhook is opt-in via `WEBHOOK_URL` |
| Auto-migrations at startup | No manual `migrate` step — safe for Docker restarts |
| `ErrOrderStatusConflict` | Makes `ConfirmPayment` idempotent — safe under concurrent webhooks + polling |
| Atomic stock decrement in DB transaction | Prevents overselling even under concurrent requests |
| `PaymentOutcome` returned, messages sent by the bot layer | `shop` stays Telegram-free; all three payment paths share one side-effect pipeline |
| `referral_awards` PK + `INSERT OR IGNORE` | First-purchase referral bonus is awarded exactly once, even under races |
| `VACUUM INTO` backups on the live connection | Consistent snapshots without the sqlite3 CLI — works in scratch Docker images |
| Mini App behind `WEBAPP_URL` | Telegram requires HTTPS; without the variable the feature degrades cleanly to “off” |
| Default language `en` | Matches the documented behavior; `ru` remains the translation reference locale |
