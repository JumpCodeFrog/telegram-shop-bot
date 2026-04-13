# Roadmap — Telegram Shop Bot

> Полный профессиональный анализ проекта и приоритизированный план развития.  
> Дата анализа: апрель 2026.

---

## 1. Общая оценка проекта

Проект находится на уровне **«крепкий MVP, близкий к production-ready»**. Это одна из наиболее функционально насыщенных реализаций Telegram-магазина на чистом Go: чистая архитектура, FSM, rate-limiting, property-based тесты, Redis Streams, Prometheus — всё это редко встречается у конкурентов в open-source.

При этом есть несколько системных проблем, которые накапливают технический долг и начнут ограничивать масштабирование и сопровождение при росте базы пользователей или команды. Ни одна из них не критична прямо сейчас, но откладывать их нельзя.

**Итоговая оценка:** 7.5 / 10

---

## 2. Сильные стороны

| # | Что именно | Почему важно |
|---|---|---|
| 1 | Чистая слоёная архитектура: `storage → shop/service → bot` | Изменения в одном слое не ломают другие |
| 2 | Все сторы — интерфейсы в `interfaces.go` | Легко подменять реализации и мокать в тестах |
| 3 | FSM с Redis-fallback на in-memory | Бот работает без Redis, не падает при его недоступности |
| 4 | Идемпотентный `UpdateOrderStatus` через `fromStatus` | Защита от двойного списания при параллельных вебхуках |
| 5 | Property-based тесты (`pgregory.net/rapid`) | Покрывают граничные случаи, которые сложно написать вручную |
| 6 | Rate-limiting middleware с per-user token bucket | Защита от спама и DDoS на уровне приложения |
| 7 | Panic recovery middleware | Падение одного хендлера не убивает весь бот |
| 8 | Redis Streams для async loyalty | Правильная асинхронная очередь, не просто горутина |
| 9 | Prometheus + Grafana из коробки | Observability с первого дня |
| 10 | No CGO (modernc.org/sqlite) | Статически слинкованный бинарник, работает в scratch-контейнере |
| 11 | Graceful shutdown через context | Рабочие горутины корректно завершаются по сигналу |
| 12 | Транзакционное снятие stock при оплате | Нет оверселла при конкурентных заказах |
| 13 | Webhook + polling авто-детект | Удобно и для разработки, и для продакшна |
| 14 | i18n с fallback на English | Многоязычность заложена архитектурно |

---

## 3. Слабые стороны и риски

### 3.1. Критические технические риски

**[РИСК-1] handlers.go — 1543 строки, admin.go — 640 строк**

Оба файла — God Objects. Весь Telegram UI-слой сосредоточен в двух файлах. Это:
- Делает невозможным написание изолированных unit-тестов на хендлеры
- Увеличивает вероятность конфликтов в git при параллельной разработке
- Усложняет навигацию и поиск нужной логики

**[РИСК-2] Нет индексов в базе данных**

В `001_init.sql` нет ни одного `CREATE INDEX`. При росте до 10 000+ заказов следующие запросы будут делать full table scan:
- `SELECT * FROM orders WHERE user_id = ?`
- `SELECT * FROM orders WHERE status = ?`
- `SELECT * FROM cart_items WHERE user_id = ?`
- `SELECT * FROM wishlist WHERE user_id = ?`
- `SELECT * FROM loyalty_txs WHERE user_id = ?`

**[РИСК-3] WishlistWatcher спамит пользователей без дедупликации**

`worker/wishlist.go` каждые 30 минут проверяет все записи вишлиста. Если цена упала, уведомление отправляется **каждый тик** без отметки «уже отправлено». Пользователь будет получать одно и то же сообщение бесконечно, пока цена не поднимется обратно.

**[РИСК-4] CryptoBotPolling грузит все оплаченные инвойсы**

`worker/polling.go` вызывает `GetInvoices(ctx, "paid")` без курсора и пагинации. С ростом числа заказов это будет возвращать всё больше данных, большинство из которых уже обработаны. Нет отслеживания последнего обработанного invoice_id.

**[РИСК-5] BalanceStore — мёртвая функция**

Таблицы `balance_txs`, колонка `balance_usd` в `users`, модель `Transaction`, `BalanceStore` — всё это реализовано в storage, но нигде не вызывается из checkout-флоу. Метод оплаты `PaymentMethodBalance` существует как константа, но не подключён в `onOrderConfirm`. Если запустить «оплату балансом», ничего не произойдёт, но строка в orders создастся.

### 3.2. Архитектурные проблемы

**[АРЕХ-1] 51 вхождение `context.Background()` в обработчиках**

Все хендлеры создают `ctx := context.Background()`. Это означает, что запросы к БД не учитывают таймаут или отмену от родительского контекста (например, пользователь отключился, а запрос к БД продолжается).

**[АРЕХ-2] Бизнес-логика в storage-слое**

`UpdateOrderStatus` в `internal/storage/orders.go` помимо изменения статуса выполняет:
- Декремент stock всех товаров в заказе
- Запись использования промокода
- Обновление счётчика промокода

Это три бизнес-операции в одной SQL-транзакции на уровне хранилища. Правильное место — `OrderService.ConfirmPayment` в `internal/shop/`.

**[АРЕХ-3] Воркеры принимают конкретные типы вместо интерфейсов**

- `OnboardingWorker` требует `*storage.SQLUserStore` (не интерфейс)
- `LoyaltyWorker` требует `*storage.LoyaltyStoreImpl` (не интерфейс)
- `WishlistWatcherWorker` требует `*storage.WishlistStore` (не интерфейс)

Это ломает принцип зависимости от абстракций и не позволяет тестировать воркеры с моками.

**[АРЕХ-4] Хардкод строк на русском в webhook.go**

```go
// internal/bot/webhook.go:75
text := fmt.Sprintf("✅ Оплата заказа #%d прошла успешно!\n\nСпасибо за покупку!", payload.OrderID)
```

Это строка не проходит через i18n. Все уведомления о CryptoBot-оплате всегда на русском, независимо от языка пользователя.

**[АРЕХ-5] `handleCallback` — цепочка if/else без структуры**

Метод `handleCallback` содержит серию `strings.HasPrefix` проверок без паттерна Command/Router. Добавление новой кнопки требует редактирования этого монолита.

### 3.3. Менее критичные проблемы

| # | Проблема | Риск |
|---|---|---|
| P1 | `isAdmin()` — линейный поиск O(N) вместо map lookup | Незначительно, но неаккуратно |
| P2 | Нет лимита на размер HTTP-запроса на эндпоинтах вебхука | DoS через огромное тело |
| P3 | `go vet` в CI, но нет `golangci-lint` | Пропускаются статические ошибки, race conditions |
| P4 | `price_stars` хранится и в `products`, и в `orders` | Рассинхрон при изменении курса после создания заказа |
| P5 | Удаление категории не каскадирует на товары | FK references, но нет `ON DELETE CASCADE` |
| P6 | Referral-коды генерируются через `math/rand` | Формально слабо для security-sensitive контекста |
| P7 | Нет `updated_at` триггера / автообновления в orders | `updated_at` не обновляется при `UpdateOrderStatus` |
| P8 | Нет таймаута на HTTP-сервере | Зависшие соединения не освобождаются |

---

## 4. Сравнение с лучшими open-source решениями

### Топ-5 аналогов (2026)

| Проект | Язык | Stars | Сильные стороны | Где мы лучше |
|---|---|---|---|---|
| **aiogram-shop-bot** | Python | ~3.2k | Mini Apps, широкая экосистема, активное сообщество | Типобезопасность, нет runtime-ошибок, один бинарник |
| **grammY shop** | TypeScript | ~1.8k | Плагин-система, Deno/Node, хорошая документация | Производительность, zero deps, embedded DB |
| **teleshop (Ruby)** | Ruby | ~900 | Отличный UI/UX в боте, ActiveRecord миграции | Память, Docker size, нет runtime |
| **BotShop (Python/aiogram3)** | Python | ~600 | Inline-каталог, тонкая кастомизация | Архитектура, тесты, наблюдаемость |
| **telebot-shop (Go)** | Go | ~200 | Близкая архитектура | Значительно более полный функционал, лучшее тестирование |

### Где мы отстаём от лидеров

| Возможность | Лидер | У нас |
|---|---|---|
| **Telegram Mini Apps** | aiogram-shop, grammY | ❌ Нет |
| **Inline-режим каталога** | BotShop, grammY | ❌ Нет |
| **Отзывы на товары / рейтинги** | aiogram-shop | ❌ Нет |
| **Мультивалютность** | BotShop | ⚠️ Только USD + Stars |
| **Загрузка фото через бот** | aiogram-shop | ❌ Только URL |
| **CI с линтером** | Большинство | ⚠️ Только go vet |

### Где мы выигрываем

| Возможность | Наш уровень |
|---|---|
| Архитектура | ✅ Значительно чище, чем у всех Python-аналогов |
| Property-based тесты | ✅ Уникально — ни у одного аналога нет `rapid` |
| Observability (Prometheus+Grafana) | ✅ Только у единиц из аналогов |
| Redis Streams для async задач | ✅ Архитектурно правильно |
| Graceful shutdown | ✅ Корректный teardown воркеров |
| Один статический бинарник | ✅ Smallest Docker image среди всех аналогов |

---

## 5. Приоритизированный план улучшения

---

### Этап 1 — Must Have

> Это технический долг, который мешает расти и поддерживать проект.

---

#### 1.1. Добавить индексы в базу данных

**Что:** Добавить новую миграцию `008_add_indexes.sql` с индексами на все горячие запросы.

```sql
CREATE INDEX IF NOT EXISTS idx_orders_user_id ON orders(user_id);
CREATE INDEX IF NOT EXISTS idx_orders_status ON orders(status);
CREATE INDEX IF NOT EXISTS idx_cart_items_user_id ON cart_items(user_id);
CREATE INDEX IF NOT EXISTS idx_wishlist_user_id ON wishlist(user_id);
CREATE INDEX IF NOT EXISTS idx_loyalty_txs_user_id ON loyalty_txs(user_id);
CREATE INDEX IF NOT EXISTS idx_products_category_id ON products(category_id);
CREATE INDEX IF NOT EXISTS idx_products_is_active ON products(is_active);
```

**Почему важно:** При базе в 10k заказов и 1k пользователей без индексов запрос `GetUserOrders` будет делать full scan по всей таблице на каждый `/orders`.  
**Сложность:** Low  
**Эффект:** Снижение latency на запросы к истории заказов, корзине, вишлисту — в 10-100x при росте данных.

---

#### 1.2. Дедупликация уведомлений в WishlistWatcher

**Что:** Добавить в таблицу `wishlist` колонки `price_drop_notified_at` и `back_in_stock_notified_at`. В воркере проверять: если уведомление уже было отправлено в этом «цикле» (цена не менялась), не отправлять повторно. Сбрасывать флаг при изменении цены/стока.

**Почему важно:** Сейчас пользователи будут получать одно и то же сообщение каждые 30 минут бесконечно. Это гарантированный путь к блокировке бота и жалобам.  
**Сложность:** Low  
**Эффект:** Устранение спама. Корректная UX для вишлиста.

---

#### 1.3. Исправить хардкод русских строк в webhook.go

**Что:** Заменить все hardcoded строки на `b.t(lang, "key")`. Для этого нужно получить язык пользователя через `users.GetByTelegramID` и добавить ключи в `ru.json` / `en.json`.

```go
// Сейчас:
text := fmt.Sprintf("✅ Оплата заказа #%d прошла успешно!\n\nСпасибо за покупку!", payload.OrderID)

// Надо:
user, _ := b.users.GetByTelegramID(ctx, order.UserID)
lang := user.LanguageCode
text := fmt.Sprintf(b.t(lang, "payment_success"), payload.OrderID)
```

**Почему важно:** Английский пользователь получает уведомление об оплате на русском — это критический UX-баг.  
**Сложность:** Low  
**Эффект:** Корректная i18n для всех платёжных уведомлений.

---

#### 1.4. Добавить golangci-lint в CI

**Что:** Добавить step в `.github/workflows/ci.yml`:

```yaml
- name: Lint
  uses: golangci/golangci-lint-action@v6
  with:
    version: latest
```

И `.golangci.yml` с минимальным набором: `errcheck`, `govet`, `staticcheck`, `gosimple`, `unused`.

**Почему важно:** `go vet` не ловит неотловленные ошибки (`err` присваивается `_`), гонки данных и многие статические проблемы. В текущем коде есть несколько мест с проигнорированными ошибками.  
**Сложность:** Low  
**Эффект:** Автоматическое обнаружение багов до merge.

---

#### 1.5. Добавить таймаут на HTTP-сервер и лимит тела запроса

**Что:** В `cmd/bot/main.go` заменить инициализацию HTTP-сервера:

```go
server := &http.Server{
    Addr:         ":8080",
    Handler:      mux,
    ReadTimeout:  10 * time.Second,
    WriteTimeout: 10 * time.Second,
    IdleTimeout:  60 * time.Second,
}
```

И в вебхук-хендлерах добавить `http.MaxBytesReader(w, r.Body, 1<<20)` (1 MB лимит).

**Почему важно:** Без таймаутов медленный/злонамеренный клиент держит соединение вечно, исчерпывая goroutine pool.  
**Сложность:** Low  
**Эффект:** Защита от slow-client DoS.

---

#### 1.6. Довести или задокументировать статус BalanceStore

**Что:** Принять решение: либо подключить оплату балансом в `onOrderConfirm` и `checkout`-флоу, либо убрать мёртвый код (`BalanceStore`, `balance_txs`, `PaymentMethodBalance`, `balance_usd`).

**Почему важно:** Мёртвый код вводит в заблуждение и создаёт ложное ощущение готовой функции. При попытке добавить оплату балансом разработчик потратит время на разбор «как это уже реализовано», обнаружит что ничего не работает.  
**Сложность:** Low (удалить) / High (реализовать)  
**Эффект:** Чистота кодовой базы или новый метод оплаты.

---

### Этап 2 — Should Have

> Важные улучшения для production-уровня и масштабирования.

---

#### 2.1. Разбить handlers.go на тематические файлы

**Что:** Разделить 1543-строчный файл на:

```
internal/bot/
  handlers_catalog.go     # handleCatalog, sendCatalog, onCategorySelected, onProductSelected
  handlers_cart.go        # handleCart, sendCart, onCartAdd, onCartPlus, onCartMinus, onCartDel
  handlers_checkout.go    # onCartCheckout, onOrderConfirm, onPayStars, onPayCrypto, onPromoEnter
  handlers_orders.go      # handleOrders, sendOrders, onOrderCancel
  handlers_search.go      # handleSearch
  handlers_wishlist.go    # handleWishlist, onWishlistToggle
  handlers_profile.go     # handleProfile (уже есть profile.go, слить туда)
  handlers_support.go     # onSupport, onPaySupport, onTerms
  handlers_start.go       # handleStart, handleHelp, handleCancel
  handlers_callback.go    # handleCallback (диспетчер без логики)
  handlers_payment.go     # handlePreCheckout, handleSuccessfulPayment
```

**Почему важно:** При текущем размере любой PR в handlers.go почти гарантированно даёт merge-конфликты. Невозможно быстро найти нужный хендлер.  
**Сложность:** Medium  
**Эффект:** Кардинальное улучшение maintainability. Нулевой риск регрессий при аккуратном переименовании.

---

#### 2.2. Вынести бизнес-логику из UpdateOrderStatus в service-слой

**Что:** Убрать из `storage/orders.go` логику снятия stock, записи промо и обновления счётчиков. Перенести это в `shop.OrderService.ConfirmPayment` с явными вызовами:

```go
func (s *OrderService) ConfirmPayment(ctx context.Context, orderID int64, method, paymentID string) error {
    // 1. Изменить статус (только статус, без бизнес-логики)
    if err := s.orders.SetPaid(ctx, orderID, method, paymentID); err != nil { ... }
    // 2. Снять stock
    if err := s.products.DecrementStock(ctx, items...); err != nil { ... }
    // 3. Записать промо
    if err := s.promos.RecordUsage(ctx, ...); err != nil { ... }
    return nil
}
```

**Почему важно:** Сейчас storage-слой содержит бизнес-правила, что нарушает Clean Architecture и делает невозможным тестирование этой логики без реальной БД.  
**Сложность:** Medium  
**Эффект:** Тестируемость checkout-флоу, явная читаемость бизнес-правил.

---

#### 2.3. Перевести воркеры на интерфейсы

**Что:** Определить минимальные интерфейсы для каждого воркера:

```go
// worker/onboarding.go
type userFinder interface {
    GetNewUsersWithoutOrders(ctx context.Context, minAge, maxAge time.Duration) ([]storage.User, error)
}
```

Аналогично для `LoyaltyWorker` и `WishlistWatcherWorker`.

**Почему важно:** Сейчас воркеры невозможно покрыть тестами без поднятия реальной SQLite БД. Это нарушает принцип зависимости от абстракций.  
**Сложность:** Low  
**Эффект:** Тесты для воркеров без реальной БД.

---

#### 2.4. Исправить CryptoBotPolling — добавить курсор

**Что:** Хранить в Redis (или in-memory при старте) `last_processed_invoice_id`. В каждом тике запрашивать только инвойсы с ID > последнего обработанного.

Альтернатива проще: запрашивать `active` инвойсы (их всегда мало), а не `paid`.

**Почему важно:** При 1000+ оплаченных заказах текущий подход будет возвращать и обрабатывать весь исторический список каждые 30 секунд.  
**Сложность:** Low  
**Эффект:** O(new_invoices) вместо O(all_invoices).

---

#### 2.5. Заменить context.Background() на propagated context

**Что:** Передавать контекст из Telegram update в хендлеры. Создать вспомогательную функцию с таймаутом:

```go
func handlerCtx() (context.Context, context.CancelFunc) {
    return context.WithTimeout(context.Background(), 30*time.Second)
}
```

Долгосрочно — проброс контекста из polling/webhook loop.

**Почему важно:** Запросы к БД не имеют таймаута. Один медленный запрос занимает goroutine навсегда.  
**Сложность:** Medium  
**Эффект:** Корректная отмена запросов, защита от зависших DB-соединений.

---

#### 2.6. Добавить загрузку фото товаров через бот

**Что:** В диалоге добавления товара (`StepPhoto`) поддержать отправку фото напрямую в чат (не только URL). Получать `file_id` через `GetFile`, сохранять его как `photo_url`.

**Почему важно:** Все аналоги поддерживают загрузку фото. Требование вводить URL вручную — серьёзный барьер для нетехничных администраторов.  
**Сложность:** Medium  
**Эффект:** Существенное улучшение UX для admin-панели.

---

#### 2.7. Добавить `updated_at` автообновление

**Что:** Добавить SQLite-триггер или явное обновление `updated_at` в `UpdateOrderStatus`:

```sql
UPDATE orders SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE ...
```

**Почему важно:** Текущий `updated_at` устанавливается только при создании (`DEFAULT CURRENT_TIMESTAMP`). Аналитика по времени обработки заказов невозможна.  
**Сложность:** Low  
**Эффект:** Корректная аналитика времени жизни заказов.

---

#### 2.8. Inline-режим каталога

**Что:** Реализовать обработчик `update.InlineQuery` для поиска товаров через inline-запрос `@botname товар`. Возвращать `InlineQueryResultArticle` с карточками товаров.

**Почему важно:** Позволяет делиться товарами в любом чате. Все топовые аналоги это поддерживают. В README упомянуто как будущая функция.  
**Сложность:** Medium  
**Эффект:** Вирусный рост — пользователи шарят товары друзьям.

---

### Этап 3 — Nice-to-Have

> Развитие продукта, конкурентные преимущества.

---

#### 3.1. Telegram Mini App (Web App)

**Что:** Разработать React/Vue Mini App для каталога и корзины. Backend API уже есть — нужен REST-слой поверх существующих сервисов.

**Почему важно:** В 2025-2026 Mini Apps стали стандартом для серьёзных Telegram-магазинов. Большинство топовых конкурентов уже перешли. Возможности UX несравнимо богаче inline-кнопок.  
**Сложность:** High  
**Эффект:** Качественный скачок в UX. Выход на уровень топовых решений.

---

#### 3.2. Система отзывов и рейтингов

**Что:** После перевода заказа в `delivered` — запрашивать оценку (1-5 ⭐) и опциональный отзыв. Хранить в отдельной таблице `reviews`. Показывать средний рейтинг на карточке товара.

**Почему важно:** Социальное доказательство. Увеличивает конверсию. Есть у aiogram-shop и большинства e-commerce платформ.  
**Сложность:** Medium  
**Эффект:** Повышение доверия к товарам, рост конверсии.

---

#### 3.3. Несколько фото на товар (медиагруппы)

**Что:** Заменить `photo_url TEXT` на отдельную таблицу `product_photos (product_id, file_id, sort_order)`. Отправлять через `SendMediaGroup`.

**Почему важно:** Для одежды, обуви и аксессуаров одно фото — серьёзное ограничение. Конкуренты поддерживают галерею.  
**Сложность:** Medium  
**Эффект:** Лучшая презентация товаров.

---

#### 3.4. Расширенная аналитика и дашборд

**Что:** Добавить в admin-панель:
- Графики продаж по дням/неделям (уже есть `GetRevenueByDays`, нужна визуализация в боте или Grafana)
- Топ-пользователи по выручке
- Отчёт по промокодам (использование, экономия)
- Экспорт в CSV уже есть — добавить фильтр по датам

**Сложность:** Medium  
**Эффект:** Осмысленное управление магазином на основе данных.

---

#### 3.5. Подключение второго платёжного провайдера (ЮKassa / Stripe)

**Что:** Реализовать `PaymentProvider` интерфейс (уже определён в `service/payment.go`) для ЮKassa или Stripe. Интерфейс уже проектировался под это расширение.

**Сложность:** High  
**Эффект:** Оплата рублями через карты — критически важно для российской аудитории.

---

#### 3.6. Подписки и периодические платежи

**Что:** Добавить тип товара `subscription` с периодом (`monthly`, `yearly`). Автоматическое выставление инвойса через Stars recurring payments (Telegram добавил в 2025).

**Сложность:** High  
**Эффект:** Recurring revenue. Новый бизнес-юкейс.

---

#### 3.7. Нотификации об изменении статуса заказа через Telegram Topics

**Что:** Поддержка форумных топиков (Supergroup Topics) для группировки уведомлений администраторов по типу (новые заказы, оплаченные, доставленные).

**Сложность:** Low  
**Эффект:** Удобство для команд с несколькими администраторами.

---

#### 3.8. Автоматизированные E2E тесты

**Что:** Расширить `cmd/usability-smoke` до полноценного E2E-фреймворка с использованием mock Telegram API (уже есть `NewWithAPI`). Покрыть: старт → каталог → корзина → промокод → checkout → оплата Stars.

**Сложность:** Medium  
**Эффект:** Регрессионная защита для всего buyer journey.

---

## Сводная таблица приоритетов

| # | Задача | Этап | Сложность | Эффект |
|---|---|---|---|---|
| 1.1 | DB indexes | Must Have | Low | 🔥 Высокий |
| 1.2 | Wishlist dedup | Must Have | Low | 🔥 Высокий |
| 1.3 | i18n в webhook | Must Have | Low | 🔥 Высокий |
| 1.4 | golangci-lint в CI | Must Have | Low | 📈 Средний |
| 1.5 | HTTP таймауты | Must Have | Low | 🔥 Высокий |
| 1.6 | BalanceStore решение | Must Have | Low/High | 📈 Средний |
| 2.1 | Разбить handlers.go | Should Have | Medium | 📈 Средний |
| 2.2 | Бизнес-логика из storage | Should Have | Medium | 📈 Средний |
| 2.3 | Воркеры на интерфейсы | Should Have | Low | 📈 Средний |
| 2.4 | CryptoBot polling cursor | Should Have | Low | 📈 Средний |
| 2.5 | Context propagation | Should Have | Medium | 📈 Средний |
| 2.6 | Загрузка фото в боте | Should Have | Medium | 🔥 Высокий |
| 2.7 | updated_at в orders | Should Have | Low | Low |
| 2.8 | Inline-режим каталога | Should Have | Medium | 🔥 Высокий |
| 3.1 | Telegram Mini App | Nice-to-Have | High | 🚀 Очень высокий |
| 3.2 | Отзывы и рейтинги | Nice-to-Have | Medium | 📈 Средний |
| 3.3 | Мультифото | Nice-to-Have | Medium | 📈 Средний |
| 3.4 | Расширенная аналитика | Nice-to-Have | Medium | 📈 Средний |
| 3.5 | ЮKassa / Stripe | Nice-to-Have | High | 🚀 Очень высокий |
| 3.6 | Подписки | Nice-to-Have | High | 🚀 Очень высокий |
| 3.7 | Topics нотификации | Nice-to-Have | Low | Low |
| 3.8 | E2E тесты | Nice-to-Have | Medium | 📈 Средний |
