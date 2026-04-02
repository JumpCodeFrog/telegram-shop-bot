# Дизайн: Telegram Shop Bot v3.0 — полный рерайт

**Дата:** 2026-03-30
**Статус:** утверждён
**Стек:** Go, SQLite, Docker, telego (Bot API 9.5)

---

## 1. Контекст

Существующий MVP (`gotgshop`) реализует базовый магазин: каталог, корзина, оплата Stars/USDT, промокоды, аналитика, админка. Текущая библиотека `go-telegram-bot-api/v5` не поддерживает Bot API 9.x.

**Решение:** полный рерайт на `mymmrac/telego` по ТЗ v3.0. Бизнес-логика старого кода не переносится — пишется заново со значительно расширенной схемой БД.

---

## 2. Архитектура

### 2.1 Структура пакетов

```
cmd/bot/main.go

internal/
  config/config.go
  migration/
    001_init.sql          — users, categories, products, cart_items
    002_orders.sql        — orders, order_items, balance_txs
    003_loyalty.sql       — loyalty_txs, achievements, user_achievements
    004_referral.sql      — referral_stats
    005_wishlist.sql      — wishlist
    006_promos.sql        — promo_codes
  storage/
    db.go
    user.go / product.go / category.go
    order.go / cart.go / balance.go
    loyalty.go / referral.go / wishlist.go
    promo.go / achievement.go
  service/
    user.go / catalog.go / cart.go
    payment.go / balance.go
    loyalty.go / referral.go
    wishlist.go / promo.go
  bot/
    handler/
      start.go / catalog.go / cart.go
      payment.go / profile.go / orders.go
      balance.go / loyalty.go / referral.go
      wishlist.go / search.go / admin.go
    middleware/
      auth.go / ratelimit.go / logger.go / topics.go
    keyboard/
      catalog.go / cart.go / profile.go / admin.go
  worker/
    loyalty_engine.go
    wishlist_watcher.go
    notification.go
    cryptobot_polling.go
    broadcast.go
```

### 2.2 Dependency flow

```
handler → service → storage
worker  → service → storage
worker  → notification channel → Telegram
```

Telegram-вызовы только в `handler/` и `worker/`. Сервисный слой не знает о Telegram.

### 2.3 Middleware chain

```
Auth → RateLimit(30/мин) → Logger → TopicsRouter → AdminOnly
```

`Auth` middleware при каждом update: `UpsertUser` + синхронизация `is_premium` (Bot API 9.4).

---

## 3. База данных

### 3.1 Схема (полная, из ТЗ v3.0)

```sql
-- migration 001
CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    telegram_id   INTEGER UNIQUE NOT NULL,
    username      TEXT,
    first_name    TEXT,
    is_premium    BOOLEAN DEFAULT 0,
    balance_usd   REAL    DEFAULT 0,
    loyalty_pts   INTEGER DEFAULT 0,
    loyalty_level TEXT    DEFAULT 'bronze',
    referral_code TEXT    UNIQUE,
    referred_by   INTEGER REFERENCES users(id),
    created_at    DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE categories (
    id              INTEGER PRIMARY KEY,
    name            TEXT NOT NULL,
    emoji           TEXT,
    custom_emoji_id TEXT,   -- Bot API 9.4
    sort_order      INTEGER DEFAULT 0,
    is_active       BOOLEAN DEFAULT 1
);

CREATE TABLE products (
    id          INTEGER PRIMARY KEY,
    category_id INTEGER REFERENCES categories(id),
    name        TEXT NOT NULL,
    description TEXT,
    photo_url   TEXT,
    price_usd   REAL NOT NULL,
    stock       INTEGER DEFAULT 0,
    is_active   BOOLEAN DEFAULT 1,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE cart_items (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER REFERENCES users(id),
    product_id INTEGER REFERENCES products(id),
    quantity   INTEGER DEFAULT 1,
    added_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, product_id)
);

-- migration 002
CREATE TABLE orders (
    id             INTEGER PRIMARY KEY,
    user_id        INTEGER REFERENCES users(id),
    status         TEXT DEFAULT 'pending',
    total_usd      REAL NOT NULL,
    discount_usd   REAL DEFAULT 0,
    paid_from_bal  REAL DEFAULT 0,
    pts_earned     INTEGER DEFAULT 0,
    payment_method TEXT,   -- usdt|stars|balance
    payment_id     TEXT UNIQUE,  -- invoice_id (идемпотентность)
    promo_code     TEXT,
    created_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at     DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE order_items (
    id           INTEGER PRIMARY KEY,
    order_id     INTEGER REFERENCES orders(id),
    product_id   INTEGER REFERENCES products(id),
    product_name TEXT,
    quantity     INTEGER,
    price_usd    REAL
);

CREATE TABLE balance_txs (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER REFERENCES users(id),
    amount_usd REAL NOT NULL,
    type       TEXT NOT NULL,  -- topup_usdt|topup_stars|order_payment|refund|referral_bonus
    ref_id     TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- migration 003
CREATE TABLE loyalty_txs (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER REFERENCES users(id),
    pts        INTEGER NOT NULL,
    reason     TEXT,   -- order|referral|achievement|level_up|vip_gift
    ref_id     TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE achievements (
    id         INTEGER PRIMARY KEY,
    code       TEXT UNIQUE NOT NULL,
    title      TEXT,
    pts_reward INTEGER DEFAULT 0
);

CREATE TABLE user_achievements (
    user_id        INTEGER REFERENCES users(id),
    achievement_id INTEGER REFERENCES achievements(id),
    earned_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, achievement_id)
);

-- migration 004
CREATE TABLE referral_stats (
    user_id         INTEGER PRIMARY KEY REFERENCES users(id),
    total_referrals INTEGER DEFAULT 0,
    total_earned    REAL    DEFAULT 0
);

-- migration 005
CREATE TABLE wishlist (
    user_id         INTEGER REFERENCES users(id),
    product_id      INTEGER REFERENCES products(id),
    price_at_added  REAL,
    stock_at_added  INTEGER,
    added_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, product_id)
);

-- migration 006
CREATE TABLE promo_codes (
    id           INTEGER PRIMARY KEY,
    code         TEXT UNIQUE NOT NULL,
    discount_pct INTEGER DEFAULT 0,
    discount_usd REAL    DEFAULT 0,
    max_uses     INTEGER,
    used_count   INTEGER DEFAULT 0,
    expires_at   DATETIME,
    is_active    BOOLEAN DEFAULT 1
);
```

### 3.2 Статусы заказа

`pending → paid → processing → shipped → done | cancelled`

### 3.3 Уровни лояльности

| Уровень | Порог | Кэшбэк | +Premium |
|---------|-------|--------|---------|
| bronze  | 0     | 1%     | 2%      |
| silver  | 200   | 2%     | 3%      |
| gold    | 500   | 5%     | 6%      |
| vip     | 1000  | 10%    | 11%     |

---

## 4. Сервисный слой

### 4.1 Ключевые сервисы и методы

**UserService**
- `UpsertUser(params)` — создание/обновление + генерация `referral_code` (base62, 6 символов) при регистрации
- `GetProfile(userID)` — агрегированный профиль с балансом, pts, уровнем, статистикой заказов

**CartService**
- `Add(userID, productID, qty)` — upsert, проверка stock
- `Get(userID)` — `CartView` с расчётом промокода, списания с баланса, итога к оплате, предварительного кэшбэка
- `Clear(userID)`

**PaymentService**
- `CreateUSDTInvoice(userID, orderID)` → CryptoBot API
- `ConfirmPayment(invoiceID)` — атомарная транзакция:
  1. `BEGIN` → списание `balance_usd` → создание order → `balance_txs` → `COMMIT`
  2. `LoyaltyService.AwardPts` → `CheckLevelUp`
  3. `LoyaltyService.CheckAchievements`
  4. `ReferralService.AwardReferralBonus` (если первая покупка реферала)
  5. Уведомление через `NotificationWorker`

**LoyaltyService**
- `AwardPts(userID, pts, reason)` — запись в `loyalty_txs`, обновление `users.loyalty_pts`
- `CheckLevelUp(userID)` — если порог достигнут: обновить уровень, +50 pts бонус, уведомление; если VIP → `sendGift` через NotificationWorker (fallback: pts)
- `CheckAchievements(userID, event)` — проверка всех 9 достижений; дубликаты исключены через `PRIMARY KEY`
- `AwardAchievement(userID, code)` — начисление pts по достижению

**ReferralService**
- `ApplyReferral(newUserID, code)` — проверка `code != self`, запись `referred_by`, начисление $1 новому пользователю
- `AwardReferralBonus(referrerID, buyerID)` — $2 + 100 pts рефереру при первой оплаченной покупке

**WishlistService**
- `Toggle(userID, productID)` — add/remove, сохраняет `price_at_added`, `stock_at_added`
- `CheckBuyerAchievement(userID, productID)` — проверяет `wishlist_buyer`

**BalanceService**
- `Topup(userID, amount, type, refID)` — запись в `balance_txs`, обновление `users.balance_usd`
- `Deduct(userID, amount, orderID)` — атомарно в рамках `PaymentService.ConfirmPayment`
- `GetHistory(userID, limit)` — последние N транзакций

**PromoService**
- `Validate(code, userID, total)` — проверки: активен, не истёк, не превышен лимит, не использован этим пользователем
- `Apply(code, total)` — расчёт скидки (pct приоритет над fixed)
- `Redeem(code)` — `used_count++`

### 4.2 Порядок скидок в корзине

1. Промокод (% или фиксированная сумма)
2. Списание с внутреннего баланса (до 100% суммы)
3. Остаток → внешний провайдер (USDT/Stars)

---

## 5. Bot-слой (telego, Bot API 9.5)

### 5.1 Стилизованные кнопки (Bot API 9.4)

Все клавиатуры генерируются в `bot/keyboard/`. Константы стилей:
- `StylePositive` — основные действия (Оплатить, Добавить в корзину, Повторить заказ)
- `StyleDestructive` — деструктивные (Очистить корзину, Отмена)
- `StyleDefault` — нейтральные (навигация, toggles, пагинация)

### 5.2 sendMessageDraft (Bot API 9.3)

Используется в двух местах:
1. **Оплата USDT** — стриминг статусов: `⏳ Создаём счёт...` → `⏳ Ожидаем оплату...` → `✅ Оплата получена!`
2. **Аналитика** — постепенный вывод разделов отчёта

Реализация: `SendMessageDraft(final: false)` на каждом шаге, `final: true` в конце.
Fallback: если telego возвращает `APIError` о неподдерживаемом методе → `SendMessage` + `EditMessageText`.

### 5.3 date_time entity (Bot API 9.5)

Используется в чеке заказа, истории заказов и уведомлениях о смене статуса.
Telegram рендерит в таймзоне пользователя — timezone на сервере не хранится.

```go
tg.MessageEntity{Type: "date_time", Offset: offset, Length: length}
```

### 5.4 is_premium (Bot API 9.4)

Auth middleware: `update.Message.From.IsPremium` → `UpsertUser` → если первый вход с Premium → `AwardAchievement("premium_user")`.

### 5.5 sendGift (Bot API 9.3)

При достижении VIP: `bot.SendGift(userTelegramID, cfg.VIPGiftID, ...)`.
Fallback: `AwardPts(cfg.VIPGiftFallbackPts)` если `APIError`.

---

## 6. Workers

| Worker | Тип | Интервал |
|--------|-----|---------|
| LoyaltyEngine | синхронный вызов из service | по событию |
| WishlistWatcher | ticker | 30 мин |
| NotificationWorker | `chan Notification` buf=100 | постоянно |
| CryptoBotPolling | ticker | 30 сек |
| BroadcastWorker | горутина с rate limit | по запросу |

`Notification` структура:
```go
type Notification struct {
    UserTelegramID int64
    Text           string
    Entities       []tg.MessageEntity
    ReplyMarkup    interface{}
}
```

---

## 7. Конфигурация

Новые переменные vs текущего `.env.example`:

```env
# Существующие
BOT_TOKEN=
CRYPTOBOT_TOKEN=
CRYPTOBOT_WEBHOOK_SECRET=
ADMIN_IDS=
DB_PATH=./data/shop.db
LOG_LEVEL=info
WEBHOOK_URL=
TELEGRAM_WEBHOOK_URL=
TELEGRAM_WEBHOOK_SECRET=

# Новые в v3.0
REFERRAL_BONUS_USD=2.0
REFERRAL_NEW_USER_BONUS_USD=1.0
PAGINATION_SIZE=5
WISHLIST_CHECK_INTERVAL=30m
POLLING_INTERVAL=30s
PREMIUM_CASHBACK_BONUS=1
PREMIUM_WELCOME_PTS=50
VIP_GIFT_ID=
VIP_GIFT_FALLBACK_PTS=200
TOPICS_MODE=false
TOPIC_ID_CATALOG=
TOPIC_ID_CART=
TOPIC_ID_PROFILE=
TOPIC_ID_ORDERS=
```

---

## 8. Обработка ошибок

| Ситуация | Поведение |
|---|---|
| Товар закончился при оформлении | `ErrProductOutOfStock` → уведомление пользователю |
| Двойная оплата | `UNIQUE(payment_id)` → `ErrDuplicate` → idempotent ignore |
| Реферал от себя | `ReferralService` проверяет `code != self` |
| `sendGift` недоступен | `APIError` → fallback `AwardPts(VIPGiftFallbackPts)` |
| `sendMessageDraft` не поддерживается | `APIError` → `SendMessage` + `EditMessageText` |
| Premium статус изменился | Auth middleware при каждом update |
| Рассылка упала mid-loop | `continue` + log, не прерывает broadcast |
| Stock недостаточен при повторе заказа | Добавляет доступное количество + предупреждение |
| `sendGift` — нет Premium у владельца | Fallback pts |

---

## 9. Тестирование

- **Unit-тесты** — `service/` через мок-интерфейсы storage
- **Integration-тесты** — `storage/` на реальном SQLite in-memory
- **Property-тесты** (`pgregory.net/rapid`) — корзина (скидки, итоги), начисление pts, реферальные бонусы
- **Bot handlers** — не тестируются (слишком дорого при низкой ценности)

---

## 10. Достижения (9 штук)

| Код | Описание | Pts |
|-----|----------|-----|
| `first_order` | Первый заказ | 50 |
| `orders_5` | 5 заказов | 100 |
| `orders_10` | 10 заказов | 200 |
| `big_spender` | Потрачено > $500 | 300 |
| `crypto_payer` | Первая оплата USDT | 30 |
| `referral_first` | Первый приглашённый | 75 |
| `referral_5` | 5 приглашённых | 250 |
| `wishlist_buyer` | Купил из wishlist | 20 |
| `premium_user` | Вход с Telegram Premium | 50 |

Засеиваются в БД миграцией 003.
