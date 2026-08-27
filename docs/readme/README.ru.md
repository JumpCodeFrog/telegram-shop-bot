## 🇷🇺 Русский

<p align="center">
  <a href="../../README.md">🇬🇧 English</a>
</p>

Полноценный интернет-магазин внутри Telegram — каталог, корзина, оплата Stars и USDT, подписки Stars, отзывы и рейтинги, промокоды, вишлист, рабочие программа лояльности и рефералка, встроенный Mini App и панель администратора. 5 языков из коробки. Один бинарник. **Одна команда с мастером, два ответа, без ручной правки конфига.**

### ✨ Возможности

<table>
<tr>
<td width="50%">

**🛍️ Покупателям**
- Каталог товаров с категориями и фотогалереями
- Корзина и оформление заказа в Telegram
- Оплата **Telegram Stars** (встроено)
- **Подписки Stars** — регулярные 30-дневные товары, управление через `/mysubs`
- Оплата **USDT через CryptoBot** (опционально)
- **Mini App** — полноценная витрина внутри Telegram (включается через `WEBAPP_URL`)
- **Отзывы и рейтинги** — 1–5 ⭐ после доставки, средняя оценка на карточке товара
- Промокоды с ограничениями по категориям + персональные одноразовые коды
- Список желаний — уведомления о снижении цены и появлении товара
- Поиск: `/search <запрос>`
- **Программа лояльности** — кэшбэк баллами 1–10 %, уровни
- **Реферальная программа** — ссылка `/referral`; 100 баллов рефереру, промокод −10 % другу

</td>
<td width="50%">

**🔧 Администраторам**
- Управление товарами и категориями (фото прямо из Telegram, до 10 на товар)
- Заказы и изменение статусов
- Управление промокодами
- Модерация отзывов: `/reviews`
- Аналитика: `/analytics` (график выручки за 14 дней, топ покупателей, отчёт по промо), CSV-выгрузка с фильтром дат
- **Уведомления в Topics** — события заказов в супергруппу / топики форума (`ADMIN_GROUP_ID`)
- **Настройка цветов кнопок** — `/btnstyle` интерактивное меню: Primary/Success/Danger/Default для каждой кнопки
- Вход: `/admin`

**⚙️ Инфраструктура**
- SQLite встроенная БД (WAL), миграции автоматически
- Redis FSM (fallback в память при отсутствии)
- Автоматические ежедневные бэкапы (`VACUUM INTO`, хранятся 7) — бинарь sqlite3 не нужен
- Graceful shutdown всех воркеров
- Prometheus + Grafana метрики
- Health check на `:8080/health`
- Polling или Webhook (выбор по конфигу)
- Docker multi-stage, non-root пользователь
- i18n: 🇷🇺 🇬🇧 🇪🇸 🇩🇪 🇨🇳 — 5 языков из коробки

</td>
</tr>
</table>

---

### 🚀 Быстрый старт

> Сверка Stars и разбор спорных платежей работают без скрытых записей:
> `make reconcile-stars` только читает, а `make payment-review PROVIDER=stars`
> показывает редактированный inbox. Любая запись требует preview,
> `--apply` и точный `--confirm-order`. Команды: [payment operations](../payment-operations.md).

Получи токен у [@BotFather](https://t.me/BotFather), затем выполни:

```bash
git clone https://github.com/JumpCodeFrog/telegram-shop-bot.git && cd telegram-shop-bot
make quickstart
```

Встроенный мастер спросит только токен BotFather и твой числовой Telegram ID. Он проверит бота через Telegram, сам определит username, запишет `.env` без перезаписи существующего файла (`0600` на Unix), проверит SQLite и необязательный Redis, после чего запустит магазин. В интерактивном терминале токен скрыт и нигде не печатается.

Не знаешь числовой ID? Отправь любое сообщение [@userinfobot](https://t.me/userinfobot); ID — это не номер телефона и не `@username`.

Опционально: включи Inline Mode командой `/setinline` в @BotFather, чтобы делиться товарами каталога в любом чате. `doctor` подскажет, если режим выключен.

```bash
make doctor     # понятный отчёт по настройке
make run        # запуск без мастера
```

**Требования:** [Go 1.24+](https://go.dev/dl/) и токен бота. Redis необязателен: без него бот использует in-memory fallback.

#### Docker Compose

> Нужен только Docker. Текущий checkout собирается локально.

```bash
git clone https://github.com/JumpCodeFrog/telegram-shop-bot.git
cd telegram-shop-bot
cp .env.example .env
# Укажи BOT_TOKEN и ADMIN_IDS в .env, затем:
docker compose up -d --build
curl http://127.0.0.1:8080/health
docker compose logs -f bot
```

---

### 🔑 Получить токен бота

1. Открой Telegram → найди **[@BotFather](https://t.me/BotFather)**
2. Напиши `/newbot`
3. Придумай имя и username для бота
4. Скопируй токен: `1234567890:ABCdefGHIjklMNOpqrsTUVwxyz`
5. Вставь его по запросу `make quickstart` (или задай `BOT_TOKEN` в `.env` для Docker)

---

### ⚙️ Конфигурация

| Переменная | Обязательна | По умолчанию | Описание |
|---|---|---|---|
| `BOT_TOKEN` | **да** | — | Токен от @BotFather |
| `BOT_USERNAME` | нет | — | @username бота (без @), для реферальных ссылок |
| `ADMIN_IDS` | нет | — | Telegram ID администраторов через запятую |
| `CRYPTOBOT_TOKEN` | нет | — | Токен CryptoBot для USDT оплаты |
| `DB_PATH` | нет | `data/shop.db` | Путь к файлу SQLite |
| `LOG_LEVEL` | нет | `info` | `debug` / `info` / `warn` / `error` |
| `APP_ENV` | нет | `development` | `production` для JSON-логов |
| `REDIS_ADDR` | нет | `localhost:6379` | Адрес Redis |
| `REDIS_PASSWORD` | нет | — | Пароль Redis |
| `USD_TO_STARS_RATE` | нет | `50` | Stars за 1 USD |
| `WEBHOOK_URL` | нет | — | Публичный HTTPS URL для webhook |
| `TELEGRAM_WEBHOOK_SECRET` | с `WEBHOOK_URL` | — | Сильный секрет webhook (`openssl rand -hex 32`), обязателен в любом `APP_ENV` |
| `LOCALES_DIR` | нет | `locales` | Путь к папке переводов |
| `WEBAPP_URL` | нет | — | Публичный HTTPS URL Mini App; пусто = Mini App выключен |
| `ADMIN_GROUP_ID` | нет | — | ID супергруппы для уведомлений админам (вместо личек) |
| `TOPIC_ORDERS_NEW` | нет | — | ID топика форума для новых заказов |
| `TOPIC_ORDERS_PAID` | нет | — | ID топика для оплаченных заказов |
| `TOPIC_ORDERS_DELIVERED` | нет | — | ID топика для доставленных заказов |
| `OUTBOUND_WEBHOOK_URL` | нет | — | Внешний URL для событий `order.paid` / `order.delivered` |
| `OUTBOUND_WEBHOOK_SECRET` | нет | — | Значение заголовка `X-Webhook-Secret` исходящих вебхуков |

> 💡 Без `WEBHOOK_URL` — режим polling (удобно для разработки).
> Без Redis — автоматически используется хранилище в памяти.

---

### 📋 Команды бота

| Команда | Описание |
|---|---|
| `/start` | Главное меню |
| `/catalog` | Каталог товаров |
| `/search <запрос>` | Поиск товаров |
| `/cart` | Корзина |
| `/orders` | История заказов |
| `/mysubs` | Подписки Stars (с кнопками отмены) |
| `/profile` | Профиль и статус лояльности |
| `/referral` | Реферальная ссылка, приглашённые и начисленные баллы |
| `/wishlist` | Список желаний |
| `/support` | Поддержка |
| `/paysupport` | Помощь с оплатой |
| `/terms` | Условия использования |
| `/help` | Список команд |
| `/cancel` | Отмена действия |

**Команды администратора** *(нужен ID в `ADMIN_IDS`)*:

| Команда | Описание |
|---|---|
| `/admin` | Панель администратора |
| `/addproduct` | Добавить товар (пошаговый мастер, фото и подписки поддерживаются) |
| `/editproduct <id> <поле> <значение>` | Редактировать товар |
| `/deleteproduct <id>` | Удалить товар |
| `/addcategory <название>` | Добавить категорию |
| `/editcategory` / `/deletecategory` / `/listcategories` | Управление категориями |
| `/addpromo` / `/listpromos` / `/deletepromo` | Управление промокодами |
| `/orders_all` | Все заказы |
| `/setdelivered <id>` | Отметить заказ доставленным (запускает запрос отзыва) |
| `/reviews` | Последние отзывы с кнопками удаления |
| `/analytics` | График выручки, топ покупателей, отчёт по промокодам |
| `/export_orders [от] [до]` | CSV-выгрузка, опциональный диапазон дат |
| `/btnstyle` | Настройка цветов кнопок |

---

### 💳 Оплата

**Telegram Stars** — встроено, работает сразу. Курс задаётся через `USD_TO_STARS_RATE` (по умолчанию: 50 Stars = $1).

**CryptoBot (USDT)** — опционально. Установи `CRYPTOBOT_TOKEN` для включения.
Фоновый воркер проверяет статус платежей каждые 30 секунд. Подпись верифицируется через HMAC-SHA256.

**Подписки Stars** — товар, созданный как «подписка на 30 дней», продаётся регулярным платежом Stars (`subscription_period=2592000`). Подписки оплачиваются только Stars; управление — через `/mysubs`.

---

### 📱 Mini App

Лёгкая веб-витрина (vanilla JS, встроена в бинарник — без сборки), открывается прямо в Telegram: каталог, карточки с фото и рейтингом, корзина, оформление через `openInvoice` (Stars) или CryptoBot.

1. Выведи порт 8080 бота за публичный **HTTPS**-домен (Telegram требует HTTPS для Web Apps).
2. Установи `WEBAPP_URL=https://shop.example.com/app` в `.env`.
3. Перезапусти — кнопка меню бота станет Mini App, статика доступна на `/app/`, REST API на `/api/*` (авторизация по Telegram `initData` с проверкой HMAC).

> Без `WEBAPP_URL` Mini App и его API вообще не монтируются — бот работает как раньше.

---

### 🌱 Тестовые данные

```bash
go run ./cmd/seed
```

Создаст категории (Одежда, Обувь, Аксессуары), 6 товаров и промокоды `WELCOME10` (−10%) и `SALE20` (−20%). Безопасно запускать повторно.

---

### 🚢 Деплой в продакшн

```bash
# 1. Установи BOT_TOKEN и ADMIN_IDS в .env (или выполни `make init`)
# 2. Единая проверка: конфиг, БД, Redis, Telegram и webhook
make doctor

# 3. Запуск
docker compose up -d --build
curl http://127.0.0.1:8080/health
curl http://127.0.0.1:8080/metrics
```

<details>
<summary>Webhook + nginx конфигурация</summary>

```nginx
server {
    listen 443 ssl;
    server_name shop.example.com;

    ssl_certificate     /etc/letsencrypt/live/shop.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/shop.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }
}
```

```env
WEBHOOK_URL=https://shop.example.com
TELEGRAM_WEBHOOK_SECRET=случайная-строка
```

Telegram будет отправлять POST на `https://shop.example.com/telegram-webhook`. Если включён CryptoBot, укажи ему `https://shop.example.com/cryptobot-webhook`.

</details>

---

### 🏗️ Структура проекта

```
telegram-shop-bot/
├── cmd/
│   ├── bot/               # Точка входа — запуск бота
│   ├── preflight/         # Проверка окружения перед запуском
│   ├── seed/              # Загрузка демо-данных
│   ├── telegram-smoke/    # Smoke-тест через Telegram API
│   └── usability-smoke/   # Smoke-тест пути покупателя (без токена)
├── internal/
│   ├── bot/               # Хендлеры, клавиатуры, уведомления, webhook
│   ├── config/            # Загрузка конфигурации
│   ├── payment/           # Адаптеры Stars (включая подписки) и CryptoBot
│   ├── service/           # Сквозные сервисы (i18n, лояльность, метрики)
│   ├── shop/              # Каталог, корзина, заказы (пайплайн PaymentOutcome)
│   ├── storage/           # SQLite-сторы, Redis кэш/FSM, миграции
│   └── webapi/            # REST API Mini App (авторизация по initData)
├── web/app/               # Фронтенд Mini App (встроен в бинарник, vanilla JS)
├── locales/               # Переводы (ru, en, es, de, zh)
├── worker/                # Фоновые воркеры
├── monitoring/            # Grafana dashboard JSON
├── Dockerfile
├── docker-compose.yml
├── Makefile
└── .env.example
```

---

### 🛠️ Makefile

```bash
make build      # Собрать все бинарники
make test       # Запустить тесты
make lint       # go vet
make quickstart # Мастер init + doctor + run
make init       # Создать .env по двум ответам
make doctor     # Единая проверка окружения и сервисов
make run        # Запустить бота
make seed       # Загрузить демо-данные
make preflight  # Прежняя локальная/offline-проверка
make reconcile-stars # Read-only сверка реестра Telegram Stars
make payment-review  # Список спорных Stars-фактов (PROVIDER=crypto для CryptoBot)
```

---

### 🔒 Безопасность

- Верификация подписи webhook (HMAC-SHA256) для CryptoBot
- Поддержка секретного токена для Telegram webhook
- Доступ в админку только по явному списку Telegram ID
- Docker-контейнер работает от non-root пользователя
- Токены и секреты не попадают в логи

Нашёл уязвимость? Смотри [SECURITY.md](../../SECURITY.md).

---

### 🌍 Локализация

Переводы в `locales/`. Доступны:
- 🇷🇺 `ru.json` — русский
- 🇬🇧 `en.json` — английский (по умолчанию при неизвестном языке)
- 🇪🇸 `es.json` — испанский
- 🇩🇪 `de.json` — немецкий
- 🇨🇳 `zh.json` — китайский

Чтобы добавить новый язык — создай `locales/<код>.json` по образцу `en.json`. Язык выбирается автоматически по настройкам Telegram (`language_code` нормализуется: `ru-RU` → `ru`); Mini App и админка тоже переведены.

---

### 🤝 Участие в разработке

1. Форкни репозиторий
2. Создай ветку: `git checkout -b feature/my-feature`
3. Запусти тесты: `go test ./...`
4. Запусти линтер: `go vet ./...`
5. Открой Pull Request

Подробнее в [CONTRIBUTING.md](../../CONTRIBUTING.md).

---

### 📄 Лицензия

MIT — делай что хочешь. Смотри [LICENSE](../../LICENSE).
