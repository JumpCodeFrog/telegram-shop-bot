# Roadmap — Telegram Shop Bot

> Актуализированный план развития проекта.
> Дата ревизии: 20 сентября 2026 (заменяет анализ от апреля 2026).

---

## 1. Статус проекта

Проект — **production-ready Telegram-магазин** с платёжной матрицей из семи рельсов
(Telegram Stars, CryptoBot, YooKassa ₽, Stripe $, TON, NOWPayments, внутренний баланс),
Mini App, подписками, отзывами, мультифото, i18n на 5 языков, immutable payment ledger
(миграции 017–022) и E2E-регрессией всего buyer journey.

Почти весь технический долг апрельского анализа закрыт (см. §2). Текущая оценка: **9 / 10**.
Программа «Roadmap Zero» (§3) завершена: Stripe, TON+NOWPayments, полное управление
из админки и quality sweep — все четыре стадии смержены. Остался точечный бэклог (§4).

---

## 2. Закрыто с апрельского анализа

| Пункт апрельского roadmap | Статус | Где |
|---|---|---|
| 1.1 Индексы БД | ✅ | миграция `008_add_indexes.sql` |
| 1.2 Дедупликация wishlist-уведомлений | ✅ | миграция `009_wishlist_notif_tracking.sql` |
| 1.3 Хардкод русских строк в webhook | ✅ | i18n-ключи, `b.t(...)` во всех вебхуках |
| 1.4 golangci-lint в CI | ✅ | `.golangci.yml` + workflow step |
| 1.5 HTTP-таймауты + лимит тела | ✅ | `cmd/bot/main.go` (HTTP-сервер: ReadTimeout/WriteTimeout/IdleTimeout), `MaxBytesReader` во всех вебхуках и webapi |
| 2.1 Разбить handlers.go (1543 строки) | ✅ | тематические `handlers_*.go`; handlers.go = 466 строк |
| 2.4 CryptoBot polling cursor | ✅ | `worker/polling.go`: windowed fetcher с continuation (`GetInvoicesWindow`) |
| 2.3 Воркеры на интерфейсы | ✅ | `worker/polling.go` — `InvoiceFetcher`/`PaymentConfirmer`; воркеры покрыты тестами |
| 2.6 Загрузка фото через бот | ✅ | wizard `StepPhoto`, `admin_photos.go` / `admin_products.go` (после Stage-C сплита) |
| 2.7 `updated_at` автообновление | ✅ | миграция `010_orders_updated_at.sql` |
| 2.8 Inline-режим каталога | ✅ | `internal/bot/handlers_inline.go` |
| 3.1 Telegram Mini App | ✅ | `web/app/` + `internal/webapi/` |
| 3.2 Отзывы и рейтинги | ✅ | миграция `012_reviews.sql` |
| 3.3 Мультифото | ✅ | миграция `013_product_photos.sql`, `SendMediaGroup` |
| 3.6 Подписки | ✅ | миграции `014/016`, Stars-рекурренты |
| 3.8 E2E тесты | ✅ | `internal/bot/e2e_test.go` (+ YooKassa E2E) |
| 3.5 Платёжный провайдер: ЮKassa | ✅ | смержено 19.09.2026 (21 коммит, см. CHANGELOG) |

### Переоценённые пункты (решения ревизии)

- **АРЕХ-2 (бизнес-логика в `UpdateOrderStatus`)** — после появления commerce ledger (миграция 017)
  атомарное «status + stock + promo + ledger-факт» в одной транзакции является **намеренным
  дизайном**: это единственный способ гарантировать отсутствие частичного сеттлмента.
  Пункт снят; инвариант задокументирован в `docs/payment-operations.md`.
- **P4 (`price_stars` в products и orders)** — снапшот цены на момент заказа, by design
  (та же семантика, что `orders.total_rub` для RUB). Не дефект.
- **P7** — закрыт миграцией 010.

---

## 3. Программа «Roadmap Zero» (завершена, ночь 19–20.09.2026)

Порядок выполнения — от денежных рельсов к управлению к качеству. Каждый этап —
отдельный SDD-план (`docs/superpowers/plans/`), TDD, ревью на каждую задачу,
локальный мерж в main после финального ревью.

### Этап A — Stripe (USD, международные карты) ✅ (смержен 20.09.2026, ветка `feat/stripe-provider`)

- Checkout Sessions (hosted page, redirect — как YooKassa), raw HTTP без SDK.
- Подписанные вебхуки `Stripe-Signature` (HMAC-SHA256, `t.v1` схема, constant-time compare) —
  подпись доверенная, re-fetch не обязателен (в отличие от YooKassa).
- Provider key `'stripe'` (уже принят DB CHECK миграцией 020); валюта USD, scale 2 —
  конвертация не нужна (магазин USD-нативный).
- Полный контур: бот (кнопка + handler + роутер), `/stripe-webhook`, webapi
  `method=stripe`, payment-review/doctor, локали ×5, E2E, docs, CHANGELOG.
- Payer-факты Stripe без Telegram-идентичности (PayerID 0) → предикат
  `invalidProviderCapturePayer` обобщается и ужесточается до `== 0`
  (carry-over из финального ревью YooKassa-ветки).

### Этап B — Крипто-расширение (TON + NOWPayments) ✅ (смержен 20.09.2026, ветка `feat/crypto-expansion`)

- **TON** — нативная для Telegram крипта: прямые переводы на адрес магазина,
  идентификация по memo=order_id, подтверждение polling-воркером через публичный API
  (patron `worker/polling.go`), wallet deeplink `ton://transfer/...`. Валюта TON,
  scale 9 (nanoton); курс `USD_PER_TON` из env со снапшотом в заказе.
- **NOWPayments** — один интеграционный адаптер → 300+ монет (BTC/ETH/USDT/…):
  hosted invoice (redirect), IPN-вебхук с HMAC-SHA512 (`x-nowpayments-sig`,
  сортированный payload). Валюта счёта USD, scale 2 (конвертацию берёт на себя провайдер).
- Миграция 021 — одно расширение ledger CHECK: `+ 'ton', 'nowpayments', 'balance'`
  (batch, чтобы не пересобирать таблицы дважды).
- Полный контур для каждого: бот, вебхук/воркер, webapi, ops, локали, E2E, docs.

### Этап C — Полное управление из админки ✅ (смержен 20.09.2026, ветка `feat/admin-management`)

- **Разбивка `admin.go` (1094 строки)** на тематические файлы по образцу handlers_*:
  products/categories, photo-wizard, orders, analytics, promos, btn-styles, payments.
- **Платёжная очередь в боте**: заказы `needs_review` + аномалии (`payment_anomalies`)
  списком, карточка факта, resolve-действия (settle после re-check / refund-recorded /
  dismiss) — тот же контракт, что `make payment-review`, но из Telegram UI.
- **Управление заказами**: фильтры по статусу, смена статусов, карточка заказа с
  платёжным фактом.
- **Пользователи и баланс**: список/поиск, начисление/списание баланса админом
  (wiring существующего `BalanceStore` — закрывает РИСК-5 решением «реализовать»),
  **оплата балансом** в checkout (provider `'balance'`, синхронный сеттлмент).
- **Статус провайдеров** в админке (doctor-lite: какие рельсы сконфигурированы).

### Этап D — Quality sweep ✅ (закрыт 20.09.2026, ветка `chore/quality-sweep`)

- ✅ `context.Background()` → propagated/timeout context в bot-хендлерах: 54 сайта →
  per-handler 30s `b.handlerCtx()`, `r.Context()` в вебхуках, ctx-factory в
  auth-middleware; осталось 4 задокументированных lifetime-critical корня в `bot.go`.
- ✅ Carry-over из YooKassa-ledger (quality-батч): ветки GetPayment покрыты;
  `url.Parse` вместо `HasPrefix("https://")` (shared `isHTTPSURL`, RFC-корректный
  uppercase-scheme); shared-хелпер миграционных тестов (−501 строка дублей);
  replay-leg `payment_state` re-assert; восстановлены attempts-count asserts (×4,
  `payment_receipt_test.go`); `drained()` проверен — живой (9 callsites), не тронут;
  quarantine-ветки invalid-receipt + 500-on-quarantine-failure покрыты для всех
  подписанных рельсов; mock-пины `GET /v3/payments/{id}` (method+path).
  Out-of-stock на bot-слое не вошёл → §4 (4.8).
- ✅ Text-pass: «USD→Stars» комментарии в `exchange.go` (все три rail'а),
  провайдер-разделы в `docs/faq.md`, квалификатор поверхности «кнопка скрыта при
  rate=0» (теперь правда и для Mini App), architecture.md notification truth,
  `.env.example` плейсхолдеры опустошены.
- ✅ Webapp follow-up: `total_rub`/`total_ton_nano` + per-rail `*_enabled` в
  cartJSON; условные кнопки yookassa/stripe/ton/nowpayments в Mini App.
- ✅ Generic ingress CLI: `payment-review ingest-provider` для
  yookassa/stripe/ton/nowpayments (preview → `--apply --confirm-order`); memo-less
  TON теперь разрешим оператором (docs §7).
- ✅ `math/rand` → `crypto/rand` в реферальных кодах (P6) — верифицировано:
  уже было закрыто ранее, оба использования на `crypto/rand`.
- Post-merge пункт (перетегировать `webhook_missing_payment_id`) и остатки свипа → §4.

---

## 4. Бэклог после программы

| # | Задача | Сложность | Эффект |
|---|---|---|---|
| 4.1 | ~~Topics-нотификации~~ ✅ уже реализовано (аудит 21.09: `internal/bot/notify.go` — AdminEvent + topicFor + threadID) | — | — |
| 4.2 | ~~Глубокая аналитика~~ ✅ уже реализовано (аудит 21.09: TopBuyers/PromoUsage/GetPaymentMethodStats в `storage/analytics.go` + экран /analytics; диапазон дат в /export_orders) | — | — |
| 4.3 | Coinbase Commerce / BTCPay — **отложено решением**: NOWPayments покрывает 300+ монет (включая всё, что дал бы Coinbase); BTCPay — self-hosted ниша. Вернуться при явном спросе | — | — |
| 4.4 | ✅ Рефанды из админки: `/refund <order_id> [amount]` с two-tap confirm — Stars (refundStarPayment, только полные), Stripe (/v1/refunds по payment_intent), YooKassa (/v3/refunds), баланс (кредит); crypto/TON/NOWPayments — вручную (нет API), команда показывает инструкцию и ничего не выполняет. Запись в ledger через IngestProviderRefund (сначала провайдер → потом реестр), детерминированные idempotency-ключи делают re-run после сбоя записи безопасным. **Без авто-триггеров** (оператор решает) — закрыто 21.09.2026, план `docs/superpowers/plans/2026-09-21-admin-refunds.md` (адаптеры `6b581ae`, bot-флоу `6dfb6d3` + фикс `a19c01c`; объём: только админ-инициированные возвраты, один бот-возврат на заказ, записывающий CLI для ручных рельсов — follow-up) | High | Высокий |
| 4.5 | ✅ Poller потерянных вебхуков YooKassa (сейчас покрыто ручным payment-review) — закрыто 21.09.2026, план `docs/superpowers/plans/2026-09-20-yookassa-poller.md` (ListPayments-адаптер `87e48c0`, worker+wiring `108b7d2`: 60s тик, 24h окно, курсорная пагинация 50/страница с капом 20 страниц + warn об усечении, идемпотентные реплеи, out-of-stock-карантин как в вебхук-пути) | Medium | Средний |
| 4.6 | ✅ Перетегировать factless-envelope аномалию `webhook_parse_failure` → `webhook_missing_payment_id` (осознанно post-merge: reason-строки зафиксированы тестовыми пинами свипа) — закрыто 20.09.2026, коммит `ee7f926` (ретег + пин `TestYooKassaWebhookFactlessEnvelopeRecordsMissingPaymentID`) | Low | Low |
| 4.7 | ✅ Прагматичный объём — root-context derivation: `handlerCtx` выводится из process-lifetime корня (`SetRootContext`, в main — `signal.NotifyContext`), shutdown-отмена доходит до in-flight DB-работы хендлеров; 30s per-handler bound сохранён, nil-fallback оставляет существующие тестовые конструкторы без изменений — закрыто 21.09.2026, ветка `chore/ctx-attribution` (полный chain-рефакторинг `func(tgbotapi.Update)` + per-update ctx/trace — остаток в 4.14) | Medium | Средний |
| 4.8 | ✅ Покрытие ветки `out_of_stock_after_charge` на bot-слое (4 вебхука + Stars `successful_payment`; storage-уровень покрыт, bot-уровень — нет) — закрыто 20.09.2026, коммит `ee7f926` (5 bot-level legs, mutation-verified) | Low | Средний |
| 4.9 | ✅ Double-guard для CLI TON-settle: правило `>=` живёт только в launcher (`providerCaptureSettleable`); при его дрейфе нет downstream-гейта против underpay (webhook-путь защищён shop-слоем) — закрыто 20.09.2026, коммит `10459c4` (правило single-sourced в storage `validatePaymentFact`; launcher-гейт остался безвредным дублем) | Low | Средний |
| 4.10 | ✅ Actionable ошибки amount-mismatch в `ingest-provider` для card-рельсов (сейчас общий «local preview failed» — `validatePaymentFact` падает до quarantine-классификации) — закрыто 20.09.2026, коммит `10459c4` (sentinel-ошибки → сообщения с fact-vs-order суммами; exit-коды не изменились) | Low | Low |
| 4.11 | ✅ Pin `payment_state` в bot-уровневом replay-тесте YooKassa-вебхука (`TestYooKassaWebhookReplayIsIdempotent`; storage-уровень уже покрыт) — закрыто 20.09.2026, коммит `ee7f926` (пин `payment_state=settled`) | Low | Low |
| 4.12 | ✅ `ConvertUSDToNanoTON`: теоретический division-overflow residual (`usd=1e200` при `rate=1e-200`; rate — operator-configured, реальной конфигурации не существует) — закрыто 20.09.2026, план `docs/superpowers/plans/2026-09-20-backlog-followups.md` (NaN/±Inf-guard квотиента + test leg) | Low | Low |
| 4.13 | ✅ Лёгкий объём — log-атрибуция + docs, без миграции: settle-успехи четырёх провайдер-вебхуков, Stars `successful_payment` и трёх поллеров пишут структурированное поле `actor` (`webhook:<provider>` / `worker:<provider>`), success-лог `/refund` — `admin:<tgID>` (совпадает с его durable audit-строкой); новый раздел Operation attribution (docs/payment-operations.md §12). CLI actor-аудиты уже были (`internal/launcher/payment_ingress.go`) — закрыто 21.09.2026, ветка `chore/ctx-attribution` (durable actor-колонка в ledger-таблицах — остаток в 4.15) | Medium | Low |
| 4.14 | ✅ Per-update ctx + trace через весь middleware/handler chain: chain type `func(ctx context.Context, update tgbotapi.Update)`; один per-update ctx (trace id crypto/rand hex 16 + единый 30s budget) выводится на каждом ingress (`newUpdateCtx`: polling dispatch, polling barrier, telegram webhook — root `r.Context()`, `HandleUpdate`); все ~79 сайтов `b.handlerCtx()` в 24 файлах заменены параметром (staged bridge G1–G5), `handlerCtx()` удалён; `Auth` без ctx-фабрики; Logging/Recover + 13 payment-critical логов (Stars settle/renewal/quarantine + оба barrier) несут `trace_id`/`update_id` (`loggerFor`); pin'ы: unit/middleware/e2e (quarantine-log), -race чистый — закрыто 22.09.2026, merge `c67e66e` (spec `docs/superpowers/specs/2026-09-22-update-ctx-trace-design.md`; fleet-wide `loggerFor`-sweep — follow-up в HANDOFF §6) | Large | Средний |
| 4.15 | Durable actor-колонка в immutable ledger-таблицах для webhook/worker settles (остаток 4.13: сейчас — только log-атрибуция `actor=webhook:<provider>` / `actor=worker:<provider>`, docs §12; CLI и `/refund` уже пишут durable `payment_ingress_audits`). Sketch и open questions (миграция 023, nullable actor без backfill, explicit-param vs ctx-extraction) — spec §9; ЗАВИСИТ от 4.14; свой brainstorm перед планом | Medium | Low |

---

## 5. Инварианты, которые нельзя ломать (для всех будущих планов)

1. **Деньги в minor units** на каждой границе; одна формула конверсии на валюту;
   снапшот курса в заказе при создании (env-изменение не перепраисывает заказы).
2. **Settlement только через проверенный факт**: подписанный вебхук (crypto/Stripe/NOWPayments)
   или авторитетный re-fetch (YooKassa) или on-chain подтверждение (TON). Тело неподписанного
   вебхука — только идентификаторы.
3. **Ledger immutable**: факты не правятся — только quarantine + resolution.
4. **Один provider-ключ = одна миграция CHECK**; app-слой принимает только реализованные
   провайдеры.
5. **Подписки — только Stars** на всех поверхностях (бот, webapi, storage).
6. При отключённом провайдере все checkout-поверхности **байт-идентичны** текущим.
