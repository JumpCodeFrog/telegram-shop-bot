# Roadmap: Telegram Shop Bot Evolution
**Цель:** стабилизировать текущий baseline, затем перевести проект к v3 и миграции на `telego`.

Текущий статус:

- runtime всё ещё на `go-telegram-bot-api/v5`
- активные runtime-миграции находятся в `internal/storage/migrations/`
- каталог работ и handoff ведутся в `AGENT_WORK_PLAN.md`

---

## 1. Инженерная база и Наблюдаемость (Engineering & Observability)
*Показать навыки работы с Enterprise-стеком и надежностью.*

- [x] **Structured Logging (slog):** Заменен стандартный логгер на `log/slog`. Настроен вывод в JSON (в `main.go`).
- [x] **Graceful Shutdown & Context Management:** Настроено корректное завершение работы бота и API сервера.
- [x] **Transactional Integrity:** Внедрены строгие транзакции в `UpdateOrderStatus` со списанием остатков товаров.
- [x] **Health Check API:** Добавлен HTTP-сервер (порт 8080) с эндпоинтом `/health`.
- [x] **Prometheus Metrics:** Внедрен сбор метрик (активные корзины, платежи, длительность запросов).
- [x] **Grafana Dashboard:** Создан JSON-конфиг дашборда в `monitoring/`.

---

## 2. Безопасность и Отказоустойчивость (State Management)
*Защита бизнес-логики и надежное хранение состояния.*

- [x] **Redis FSM (Finite State Machine):** Состояние пользователей (админка, промокоды) перенесено в Redis.
- [x] **Redis Caching:** Реализовано кэширование каталога товаров (декоратор `CachedProductStore`).
- [x] **Persistent Notifications (Redis Streams):** `LoyaltyWorker` переведен на использование Redis Streams.
- [x] **Anti-Fraud System:** Добавлена проверка скорости регистраций рефералов в Redis и отложенные бонусы.

---

## 3. Продвинутый функционал Bot API 9.5
*Демонстрация глубокого знания платформы.*

- [ ] **Native Date Rendering:** Использовать `MessageEntity` типа `date_time` (в планах).
- [ ] **Streaming Analytics (sendMessageDraft):** Целевая v3-функция, ещё не реализована в текущем runtime.
- [ ] **VIP Gifts:** Автоматизировать отправку `sendGift` (в планах).
- [ ] **Business Connection Support:** Добавить поддержку (требуется обновление библиотеки `go-telegram-bot-api`).

---

## 4. Продуктовые улучшения и Фоновые задачи
*Повышение конверсии и удобства.*

- [x] **Abandoned Cart Recovery (V2):** Написан воркер, генерирующий персональные промокоды для брошенных корзин (>24ч).
- [x] **Multi-Currency Support:** Динамический пересчет цен USDT <-> Stars на основе курса в ExchangeService.

---

## 5. Инструкции для Gemini CLI / Cursor
При работе над задачами из этого списка:
1. Соблюдать **Clean Architecture** (Storage -> Service -> Handler). (СДЕЛАНО: Рефакторинг на `internal/bot.Bot`)
2. Все новые тексты добавлять в `locales/*.json`.
3. Любые runtime-изменения схемы БД оформлять в виде новых файлов в `internal/storage/migrations/`.
4. Использовать **Dependency Injection**: новые сервисы (Redis, Metrics) должны пробрасываться через конструкторы. (СОБЛЮДЕНО)
5. Не использовать ORM, только сырой SQL (github.com/mattn/go-sqlite3). (СОБЛЮДЕНО)
