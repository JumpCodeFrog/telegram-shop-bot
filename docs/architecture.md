# Architecture

## Overview

Telegram Shop Bot follows **Clean Architecture** with a strict separation between layers.  
Dependencies point inward: `bot` → `shop/service` → `storage`.

```
cmd/bot/main.go          Entry point — wires everything together
│
├── internal/bot/        Telegram layer (handlers, keyboards, webhook)
│   ├── handlers.go      Router + dispatch
│   ├── handlers_*.go    Themed handler files (catalog, cart, payment, …)
│   ├── admin.go         Admin-only commands
│   ├── bot.go           Bot struct + constructor + Run loop
│   └── webhook.go       CryptoBot webhook receiver
│
├── internal/shop/       Business logic (no Telegram, no DB details)
│   ├── order.go         OrderService: create, confirm payment, cancel
│   ├── catalog.go       CatalogService: list, search, exchange rate
│   └── cart.go          CartView aggregation
│
├── internal/service/    Cross-cutting services
│   ├── i18n.go          Translation (T / Tf)
│   ├── loyalty.go       Loyalty points + levels
│   ├── metrics.go       Prometheus counters / histograms
│   ├── payment*.go      Payment adapters (Stars, CryptoBot)
│   ├── exchange.go      USD↔Stars rate
│   └── referral.go      Referral code logic
│
├── internal/storage/    Data access (SQLite)
│   ├── db.go            Connection + auto-migrations
│   ├── orders.go        OrderStore
│   ├── products.go      ProductStore (with Redis cache)
│   ├── catalog.go       CatalogStore
│   ├── cart.go          CartStore
│   ├── user.go          UserStore
│   ├── wishlist.go      WishlistStore
│   ├── promo.go         PromoStore
│   ├── ui_settings.go   UISettingsStore — button style overrides (admin-configurable)
│   ├── fsm.go           FSMStore (Redis-backed state machine)
│   └── migrations/      SQL migration files (run in order at startup)
│
├── internal/payment/    Payment provider adapters
│   ├── stars.go         Telegram Stars
│   └── cryptobot.go     CryptoBot (USDT/TON)
│
├── worker/              Background goroutines
│   ├── wishlist.go      Price-drop & back-in-stock notifications
│   ├── loyalty.go       Loyalty level-up checks
│   ├── onboarding.go    24-hour onboarding nudge
│   └── polling.go       CryptoBot missed-payment poller
│
├── locales/             Translation files
│   ├── ru.json
│   └── en.json
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
        → storage.OrderStore.CreateOrder()   (transaction)
        → storage.CartStore.ClearCart()
    → renders payment method selection

User taps "⭐ Pay with Stars"
    → bot.onPayStars()
    → payment.StarsPayment.CreateInvoice()   (Telegram Invoice API)
    → Telegram sends pre_checkout_query
    → bot.handlePreCheckout()                (answers OK)
    → Telegram sends successful_payment
    → bot.handleSuccessfulPayment()
    → shop.OrderService.ConfirmPayment()
        → storage.OrderStore.UpdateOrderStatus()
            → decrements product stock
            → records promo usage
            (all in one DB transaction)
    → notifies admins
```

---

## Data Flow — CryptoBot payment

```
User taps "💎 Pay with Crypto"
    → bot.onPayCrypto()
    → payment.CryptoBotPayment.CreateInvoice()
    → CryptoBot sends webhook POST /cryptobot-webhook
    → bot.handleCryptoBotWebhook()
    → shop.OrderService.ConfirmPayment()
    → notifies user + admins

Fallback (missed webhook):
    worker.CryptoBotPollingWorker  (every 30s)
    → checks active invoices via CryptoBot API
    → calls ConfirmPayment for any paid-but-unprocessed invoices
```

---

## Database Schema (simplified)

```
users           id, telegram_id, username, first_name, language_code,
                loyalty_pts, loyalty_level, referral_code, referred_by

categories      id, name, description

products        id, category_id, name, description, price_usd, price_stars,
                stock, photo_url, is_digital, digital_content, is_active

orders          id, user_id, status, total_usd, total_stars,
                payment_method, payment_id, discount_pct, promo_code,
                created_at, updated_at

order_items     id, order_id, product_id, product_name, quantity, price_usd

cart_items      id, user_id, product_id, quantity

wishlist        user_id, product_id, price_at_added, stock_at_added,
                price_drop_notified_at, back_in_stock_notified_at

promo_codes     id, code, discount (%), expires_at, max_uses, used_count,
                category_id

promo_usages    promo_id, user_id, order_id

button_styles   key, style    — admin-configured button color overrides (Bot API 9.4)
```

---

## Button Style Customization

All inline keyboard buttons that carry semantic meaning have an admin-configurable style override stored in `button_styles`.

```
Admin runs /btnstyle
    → sendBtnStyleList() renders an overview of all 12 button keys + current style emoji
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
| `menu_cart` | 🛒 Корзина | primary |
| `menu_orders` | 📦 Заказы | default |
| `menu_profile` | 👤 Профиль | default |
| `menu_support` | 🆘 Поддержка | default |
| `product_add` | 🛒 Добавить в корзину | success |
| `product_wish` | ❤️ Вишлист | default |
| `cart_checkout` | ✅ Оформить заказ | success |
| `cart_remove` | 🗑 Удалить | danger |
| `pay_stars` | ⭐ Telegram Stars | primary |
| `pay_crypto` | 💎 Crypto | success |
| `pay_cancel` | ❌ Отмена | danger |

---

## Key Design Decisions

| Decision | Reason |
|----------|--------|
| SQLite | Zero-ops, sufficient for single-instance bots with thousands of users |
| Redis for FSM | Avoids polluting the DB with ephemeral dialog state; TTL cleanup is free |
| Polling by default | No public URL needed for development; webhook is opt-in via `WEBHOOK_URL` |
| Auto-migrations at startup | No manual `migrate` step — safe for Docker restarts |
| `ErrOrderStatusConflict` | Makes `ConfirmPayment` idempotent — safe under concurrent webhooks + polling |
| Atomic stock decrement in DB transaction | Prevents overselling even under concurrent requests |
