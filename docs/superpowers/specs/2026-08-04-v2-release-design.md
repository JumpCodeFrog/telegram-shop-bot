# Дизайн релиза v2.0.0 — «доработать на максимум»

Дата: 2026-08-04. Статус: на ревью у владельца.
База: v1.2.0 (`main`, 167a367). Целевая версия: **v2.0.0**.

## 1. Цель и границы

**Цель:** один большой релиз, который (а) устраняет все найденные аудитом дефекты, включая
критический баг ложного подтверждения оплаты, (б) доводит до рабочего состояния фичи-витрины
(лояльность, рефералы), (в) добавляет выбранные владельцем новые возможности: отзывы, фото через
бота, мультифото, подписки Stars, лёгкий Mini App, расширенную аналитику, Topics-уведомления,
E2E-тесты.

**Не входит:** ЮKassa/Stripe, внутренний баланс (решение владельца: бонусы баллами и
промокодами), React/Vue-сборка для Mini App, миграция на другую БД.

**Ограничения:** Go 1.24, без CGO, без node-тулчейна; новые зависимости — только при
необходимости и стандартного уровня доверия; всё новое покрывается i18n (5 локалей) и тестами.

## 2. Волна 1 — фундамент (последовательно, до фич)

### 2.1 Критические исправления
| # | Файл | Дефект | Исправление |
|---|---|---|---|
| C1 | `worker/polling.go:47-59` | `GetInvoices("active")` → безусловный `pending→paid`: неоплаченные заказы подтверждаются каждые 30с | Фильтровать `inv.Status == "paid"`; тест воркера на mock-инвойсах |
| C2 | `internal/payment/stars.go:50-72` | Pre-checkout подтверждается без проверок | Проверять: заказ существует, принадлежит юзеру, статус `pending`, `TotalStars` == сумме инвойса; иначе `ok=false` с причиной |
| C3 | `internal/shop/order.go:35-42` | Проверка `Stock > 0` игнорирует количество | `p.Stock >= ci.Quantity`, ошибка `ErrInsufficientStock{Product, Have, Want}` с человеческим сообщением |
| C4 | `cmd/bot/main.go` | Воркеры и HTTP-сервер без WaitGroup; `db.Close()` под работающими | Группа воркеров по скиллу go-concurrency: `wg.Add` до `go`, дренаж с таймаутом 10с и логом имён зависших, `db.Close()` только после дренажа |
| C5 | `internal/storage/db.go` | Нет WAL и busy_timeout при MaxOpenConns(25) | DSN: `_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)` |
| C6 | `worker/loyalty.go:71-75,41-64,98` | Паники type assertion; busy-loop без backoff; игнор XAck | Безопасный разбор полей, экспоненциальный backoff при ошибке Redis (1с→30с), обработка ошибки XAck |
| C7 | `internal/storage/orders.go` + `cached_products.go` | Списание стока мимо кэша — до 1ч устаревший Stock | После успешного `UpdateOrderStatus` инвалидировать кэш затронутых товаров |
| C8 | `internal/bot/webhook.go` | Любая ошибка → 500 → вечные ретраи CryptoBot | `ErrOrderStatusConflict`/`ErrNotFound` → 200 (идемпотентность), прочее → 500; сравнение секрета через `subtle.ConstantTimeCompare` |

### 2.2 Схема БД (миграции 012–015)
- `012_reviews.sql`: `reviews(id, product_id→products, user_id, order_id→orders, rating 1..5, text NULL, created_at; UNIQUE(product_id, user_id))` + индекс `product_id`.
- `013_product_photos.sql`: `product_photos(id, product_id→products ON DELETE CASCADE, file_id TEXT, sort_order INT)`; существующий `photo_url` остаётся как «обложка» (fallback).
- `014_subscriptions.sql`: `products.sub_period_days INT DEFAULT 0` (0 = обычный товар; MVP: 30); `subscriptions(id, user_id, product_id, order_id, telegram_charge_id TEXT, status TEXT paid|canceled|expired, expires_at, created_at, updated_at)` + индексы `user_id`, `expires_at`.
- `015_promos_personal.sql`: `promos.bound_user_id INTEGER NULL` — персональный промокод виден и применим только владельцу (NULL = публичный, поведение не меняется).

### 2.3 Контракты для параллельных волн (фиксируются в волне 1)
**Платёжный пайплайн.** Все три пути подтверждения (Stars, вебхук, поллинг) уже сходятся в
`OrderService.ConfirmPayment`. Расширяем сигнатуру:

```go
type PaymentOutcome struct {
    Order            *storage.Order
    PointsAwarded    int64
    NewLevel         string // непустой = уровень вырос
    ReferralReferrer int64  // != 0 → рефереру начислены баллы ReferrerPoints
    ReferrerPoints   int64
    NewUserPromo     string // непустой = новичку выпущен персональный промокод
}
func (s *OrderService) ConfirmPayment(ctx, orderID int64, method, paymentID string) (*PaymentOutcome, error)
```
Начисления — синхронно в той же цепочке (SQLite быстрый; Redis Streams остаётся транспортом
только для уже существующего уведомления об апгрейде уровня, при недоступном Redis — прямой
вызов). Отправка сообщений пользователю/админам — ответственность bot-слоя по `PaymentOutcome`
(bot не импортируется из shop, цикла нет).

**Уведомления админам.** Новый хелпер `bot.notifyAdmins(ctx, kind AdminEvent, text)`:
`kind ∈ {OrderNew, OrderPaid, OrderDelivered}`. При заданных `ADMIN_GROUP_ID` (+опц.
`TOPIC_ORDERS_NEW/PAID/DELIVERED`) шлёт в топик супергруппы, иначе — в личку каждому админу
(текущее поведение). Все вызовы `sendToAdmins`-подобного кода переводятся на хелпер.

**REST API Mini App.** Префикс `/api/`, JSON, авторизация через заголовок
`Authorization: tma <initData>` (валидация HMAC по спеке Telegram, secret = HMAC_SHA256("WebAppData", bot_token), TTL `auth_date` 1 час):
- `GET  /api/me` → профиль, язык, баллы;
- `GET  /api/catalog` → категории; `GET /api/products?category=&page=` ; `GET /api/products/{id}` (+рейтинг, фото);
- `GET/POST/DELETE /api/cart` (позиции, количество);
- `POST /api/checkout {method: stars|crypto, promo?}` → `{invoice_link}` (Stars: `createInvoiceLink`; crypto: CryptoBot pay URL) — заказ создаётся тем же `OrderService.CreateFromCart`.
Фото товара для web: эндпоинт `GET /api/photo/{file_id}` проксирует `getFile` (краткоживущий кэш URL).

### 2.4 Гигиена (та же волна, механическое)
Удалить: `internal/service/payment*.go` (4 файла заглушек с `VerifyPayment→true`), дубликат
`AdminOnly` (оставить один, НЕ подключённый удалить вместе с его тестом либо подключить — решение:
удалить `internal/bot/middleware/admin.go`, проверка прав остаётся в хендлерах), корневой
`migration/`, 5 мёртвых balance-ключей из локалей и строку `$0.00` из профиля, остальные 12
мёртвых i18n-ключей. `gofmt -w` всего репо. i18n: нормализация тега (`ru-RU`→`ru`,
`zh-hans`→`zh`) в `I18nService.T/Tf`; дефолт при пустом языке — `en` (как обещает документация).

## 3. Волна 2 — фичи (параллельно, по доменам)

### 3.1 Лояльность + рефералы (оживление)
- Начисление: `points = CalculateCashback(level, totalUSD)` (существующая формула 1–10%) в
  `ConfirmPayment`; запись в `loyalty_txs`; `CheckAndUpgradeLevel`.
- Реферальный бонус при **первой** оплаченной покупке приглашённого: рефереру
  `bonusReferrerPts` (100) баллов + уведомление; новичку — персональный промокод (‑10%,
  одноразовый, `bound_user_id`, срок 30 дней) + уведомление. Идемпотентность: бонус помечается в
  `referrals` (existing table) полем/статусом, начисляется ровно один раз.
- UI: `/referral` (ссылка `t.me/<bot>?start=ref_<code>`, счётчик приглашённых, начисленные баллы),
  кнопка «🎁 Бонус за друга» в меню и профиле. `GenerateCode` → `crypto/rand`.
- Профиль показывает реальные баллы/уровень и прогресс до следующего уровня.

### 3.2 UX главного меню и экранов (по референсу `ref/Вставленное изображение.png`)
- Меню 2-колоночное: [🛍 Каталог|🔍 Поиск], [🛒 Корзина|❤️ Избранное], [📦 Заказы|👤 Профиль],
  [🎁 Бонус за друга|🆘 Поддержка], [📄 Условия]. Цвета через существующий styledBtn; новые ключи
  кнопок добавить в `/btnstyle` (`menu_search`, `menu_wishlist`, `menu_referral`, `menu_terms`).
- `/search`: результаты — кнопки-товары (открывают карточку), ряд Назад|Меню на всех ветках.
- `/wishlist`: каждая позиция — кнопка товара + «✖ убрать»; пустой список — кнопка в каталог.
- `/btnstyle`: `product_wish` реально применяется на карточке; у категорий свой ключ
  `catalog_category`, у списка товаров — `catalog_product` (сейчас всё красится ключом каталога).
- `onBack`: цели `wishlist`, `search`; «Назад» с заказов/поддержки/условий ведёт в меню.
- `cb.Message` nil-guard в `handleCallback` (`handlers.go:148`).

### 3.3 Отзывы и рейтинги
- После `SetDelivered` бот шлёт «Оцените заказ» с рядом 1–5⭐ (`review:<orderID>:<rating>`), затем
  опционально текст (FSM-шаг, «Пропустить»).
- Карточка товара: `⭐ 4.7 (12)`; кнопка «Отзывы» — последние 3 текста.
- Ограничения: только купивший, один отзыв на товар (UNIQUE), редактирование заменой.
- Админ: `/reviews` — последние отзывы, кнопка удаления.
- Storage: `ReviewStore` interface (`Create/Upsert`, `GetProductRating` (avg,count),
  `ListByProduct`, `Delete`) + кэш рейтинга не нужен (SQLite + индекс достаточно).

### 3.4 Фото товаров
- Мастер добавления/редактирования: `StepPhoto` принимает фото сообщением (берём максимальный
  `PhotoSize.FileID`), URL остаётся альтернативой; «Готово» после 1–10 фото.
- Карточка: одно фото — как сейчас; несколько — `sendMediaGroup` + карточка с кнопками отдельным
  сообщением. Inline-режим использует первое фото.

### 3.5 Подписки (Stars recurring)
- Товар с `sub_period_days=30` продаётся только за Stars: `sendInvoice`/`createInvoiceLink` с
  `subscription_period=2592000`.
- `successful_payment`: если `is_recurring`/`is_first_recurring` — upsert в `subscriptions`
  (charge_id, `subscription_expiration_date`), продление двигает `expires_at`.
- `/mysubs`: список активных, кнопка «Отменить» → `editUserStarSubscription(is_canceled=true)`
  (raw-вызов, как уже сделано для styled-кнопок; tgbotapi v5 метода не знает).
- Воркер `subscription.go`: раз в час — пометить истёкшие (`expired`) и за 3 дня до истечения
  прислать напоминание (одноразово, флаг в строке подписки).
- Админ-мастер товара: вопрос «обычный/подписка (30 дней)».

### 3.6 Mini App
- `web/app/` (embed в бинарник): `index.html` + `app.js` + `style.css`, vanilla JS,
  `telegram-web-app.js` SDK; темизация из `themeParams`; язык из `initDataUnsafe.user.language_code`
  (строки — с сервера, `GET /api/i18n`).
- Экраны: каталог (категории→товары), карточка (фото, цена, рейтинг), корзина (+/-/удалить),
  checkout → `Telegram.WebApp.openInvoice(link)` для Stars / `openLink` для crypto.
- Кнопка меню бота: `setChatMenuButton` → web_app на `WEBAPP_URL` (`config`, по умолчанию
  `WEBHOOK_URL`-хост + `/app`). Без HTTPS-домена приложение просто не включается (лог-warning).
- Сервер: маршруты `/app` (static) и `/api/*` на существующем HTTP-сервере :8080.

### 3.7 Аналитика + метрики
- `/analytics`: график выручки 14 дней текстовыми барами (`▇`), топ-10 покупателей (сумма,
  количество заказов), отчёт по промокодам (использования, суммарная скидка), CSV с фильтром
  `/export_orders 2026-01-01 2026-02-01` (без аргументов — как сейчас).
- Метрики: `OrdersCreated.Inc()` в `CreateFromCart`; `ActiveCarts` — gauge, пересчёт воркером
  cart_recovery каждый тик; `CartsAbandoned.Inc()` там же при рассылке напоминания; новые панели
  в `monitoring/grafana_dashboard.json`.

### 3.8 Topics-уведомления
- Config: `ADMIN_GROUP_ID` (int64), `TOPIC_ORDERS_NEW`, `TOPIC_ORDERS_PAID`,
  `TOPIC_ORDERS_DELIVERED` (int, опциональны — без них в группу без топика).
- `notifyAdmins` (контракт 2.3) + перевод существующих трёх точек уведомлений на него.

### 3.9 i18n-достройка
- Новые ключи всех фич — во все 5 локалей (ru — эталон, полный перевод, не машинная заглушка).
- Достроить усечённые переводы es/de/zh (terms_text, onboarding, promo_*, error_*).
- Убрать хардкод русского: `handlers_checkout.go:190` (новый заказ админам),
  `handlers_payment.go:209` (язык админа — `en` дефолт + ключ), `worker/cart_recovery.go:78`,
  `worker/loyalty.go:87` (язык получателя из БД). Админка переводится тоже (ключи `admin_*`),
  язык админа — его `language_code`; дефолт при неизвестном языке — `en`.
- Тест паритета ключей расширить на все 5 локалей + обратная проверка мёртвых ключей.

### 3.10 Бэкапы
- `VACUUM INTO` вместо внешнего `sqlite3` (работает в scratch-Docker), ротация: хранить последние
  7, удалять старше. Тест на ротацию.

## 4. Волна 3 — интеграция и релиз

- **E2E** (`internal/bot` + mock API из `telegram-smoke`): start→каталог→корзина→промо→checkout→
  Stars-оплата→подтверждение→отзыв; сценарий подписки; сценарий реферала (два юзера).
- `go test -race ./...` зелёный локально и в CI (ubuntu-runner, CGO есть).
- Grafana/README/docs/architecture.md/CHANGELOG обновлены; README-таблица команд полная.
- Версия: `v2.0.0`, тег и push — владелец (goreleaser отработает по тегу).

## 5. Тестовая стратегия

- Юнит: ReviewStore, SubscriptionStore, referral-идемпотентность (двойной ConfirmPayment ⇒ один
  бонус), initData-валидация (вектор из спеки Telegram), нормализация i18n-тегов, ротация бэкапов,
  polling-воркер (paid/active/mixed), pre-checkout отказы.
- Конкурентность: `-race` на пакеты shop/storage/worker; тест «ConfirmPayment дважды параллельно
  ⇒ ровно одно списание и одно начисление» (property-тест на rapid — в духе проекта).
- E2E — см. волну 3. UI-строки — тест паритета локалей.

## 6. Риски

| Риск | Смягчение |
|---|---|
| Recurring Stars нельзя проверить без живого бота | Изолировать за raw-API интерфейсом, покрыть mock-тестами; пометить в CHANGELOG как «требует проверки в бою» |
| Mini App требует HTTPS-домен | Фича деградирует: без `WEBAPP_URL` кнопка не ставится, бот полностью работает |
| Параллельные волны конфликтуют в bot.go/handlers.go | Контракты и точки расширения фиксируются в волне 1; общие файлы правит один агент на волну |
| WAL меняет файловый набор БД (-wal/-shm) | Обновить .gitignore/.dockerignore/docs, бэкап через VACUUM INTO это учитывает |
| 500-часовой объём — регрессии | E2E + race + существующие тесты в волне 3, ничего не мержится без зелёного прогона |
