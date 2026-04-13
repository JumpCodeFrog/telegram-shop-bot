<p align="center">
  <img src="https://readme-typing-svg.demolab.com?font=Fira+Code&size=30&pause=1000&color=229ED9&center=true&vCenter=true&width=700&lines=🛍️+Telegram+Shop+Bot;Built+with+Go+🐹;Open+Source+✨;Production+Ready+🚀" alt="Typing SVG" />
</p>

<p align="center">
  <a href="https://github.com/JumpCodeFrog/telegram-shop-bot/actions/workflows/ci.yml">
    <img src="https://github.com/JumpCodeFrog/telegram-shop-bot/actions/workflows/ci.yml/badge.svg" alt="CI" />
  </a>
  <img src="https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go&logoColor=white" alt="Go" />
  <img src="https://img.shields.io/badge/SQLite-embedded-003B57?logo=sqlite&logoColor=white" alt="SQLite" />
  <img src="https://img.shields.io/badge/Redis-optional-DC382D?logo=redis&logoColor=white" alt="Redis" />
  <img src="https://img.shields.io/badge/Docker-ready-2496ED?logo=docker&logoColor=white" alt="Docker" />
  <img src="https://img.shields.io/badge/Telegram-Bot_API_v5-26A5E4?logo=telegram&logoColor=white" alt="Telegram" />
  <img src="https://img.shields.io/github/license/JumpCodeFrog/telegram-shop-bot?color=green" alt="License" />
</p>

<p align="center">
  <b>🇬🇧 English</b> · <a href="#-русский">🇷🇺 Русский</a>
</p>

---

## 🇬🇧 English

A full-featured e-commerce bot for Telegram — catalog, cart, Telegram Stars & USDT payments, promo codes, wishlist, loyalty program, referral system, and admin panel. Ships as a single binary. No CGO. No bloat.

### ✨ Features

<table>
<tr>
<td width="50%">

**🛍️ Buyer**
- Product catalog with categories
- Cart & checkout inside Telegram
- **Telegram Stars** payments (built-in)
- **USDT via CryptoBot** (optional)
- Promo codes with category limits
- Wishlist — price drop & restock alerts
- Search: `/search <query>`
- Referral program with anti-fraud

</td>
<td width="50%">

**🔧 Admin**
- Manage products & categories
- Order management & status updates
- Promo code CRUD
- Analytics with CSV export
- **Button style customization** — `/btnstyle` interactive menu to set Primary/Success/Danger/Default per button
- Admin panel: `/admin`

**⚙️ Infrastructure**
- SQLite embedded DB, auto-migrations
- Redis FSM (falls back to in-memory)
- Prometheus + Grafana metrics
- Health check at `:8080/health`
- Polling or Webhook (auto-detected)
- Docker multi-stage, non-root runtime
- i18n: Russian & English out of the box

</td>
</tr>
</table>

---

### 🚀 Quick Start

#### Option 1 — Docker Compose (recommended)

> Only Docker required. No Go installation needed.

```bash
# 1. Clone
git clone https://github.com/JumpCodeFrog/telegram-shop-bot.git
cd telegram-shop-bot

# 2. Configure
cp .env.example .env
# Open .env and set BOT_TOKEN=your_token_from_@BotFather

# 3. Launch
docker compose up -d --build

# 4. Verify
curl http://127.0.0.1:8080/health

# Logs
docker compose logs -f bot
```

#### Option 2 — Local binary

**Requirements:** [Go 1.24+](https://go.dev/dl/) · Bot token from [@BotFather](https://t.me/BotFather)

```bash
git clone https://github.com/JumpCodeFrog/telegram-shop-bot.git
cd telegram-shop-bot
cp .env.example .env        # set BOT_TOKEN

go mod tidy
go test ./...               # run tests
go run ./cmd/preflight      # check env (no real bot needed)
go run ./cmd/bot            # start
```

---

### 🔑 Get a Bot Token

1. Open Telegram → search **[@BotFather](https://t.me/BotFather)**
2. Send `/newbot`
3. Choose a name and username
4. Copy the token: `1234567890:ABCdefGHIjklMNOpqrsTUVwxyz`
5. Paste into `.env` → `BOT_TOKEN=your_token`

---

### ⚙️ Configuration

| Variable | Required | Default | Description |
|---|---|---|---|
| `BOT_TOKEN` | **yes** | — | Token from @BotFather |
| `BOT_USERNAME` | no | — | Bot @username (without @), for referral links |
| `ADMIN_IDS` | no | — | Comma-separated Telegram IDs of admins |
| `CRYPTOBOT_TOKEN` | no | — | CryptoBot token for USDT payments |
| `DB_PATH` | no | `data/shop.db` | SQLite database path |
| `LOG_LEVEL` | no | `info` | `debug` / `info` / `warn` / `error` |
| `APP_ENV` | no | `development` | `production` for JSON logs |
| `REDIS_ADDR` | no | `localhost:6379` | Redis address |
| `REDIS_PASSWORD` | no | — | Redis password |
| `USD_TO_STARS_RATE` | no | `50` | Telegram Stars per 1 USD |
| `WEBHOOK_URL` | no | — | Public HTTPS URL for webhook mode |
| `TELEGRAM_WEBHOOK_SECRET` | no | — | Webhook verification secret |
| `LOCALES_DIR` | no | `locales` | Path to translations folder |

> 💡 No `WEBHOOK_URL` → polling mode (great for development).  
> No Redis → automatic fallback to in-memory state.

---

### 📋 Bot Commands

| Command | Description |
|---|---|
| `/start` | Main menu |
| `/catalog` | Browse products |
| `/search <query>` | Search products |
| `/cart` | Your cart |
| `/orders` | Order history |
| `/profile` | Your profile & loyalty status |
| `/wishlist` | Your wishlist |
| `/support` | Contact support |
| `/paysupport` | Payment help |
| `/terms` | Terms of service |
| `/help` | List of commands |
| `/cancel` | Cancel current action |
| `/admin` | Admin panel *(admins only)* |
| `/btnstyle` | Customize button colors *(admins only)* |

---

### 💳 Payments

**Telegram Stars** — built-in, works out of the box. Rate is configurable via `USD_TO_STARS_RATE` (default: 50 Stars = $1).

**CryptoBot (USDT)** — optional. Set `CRYPTOBOT_TOKEN` to enable.  
Background worker polls payment status every 30 seconds. Signatures verified via HMAC-SHA256.

---

### 🌱 Seed Demo Data

```bash
go run ./cmd/seed
```

Creates categories (Clothing, Shoes, Accessories), 6 products, and promo codes `WELCOME10` (−10%) and `SALE20` (−20%). Safe to run multiple times.

---

### 🚢 Production Deployment

```bash
# 1. Set real BOT_TOKEN and ADMIN_IDS in .env
# 2. Run checks
go run ./cmd/preflight       # validates env, DB, Redis, webhook
go run ./cmd/telegram-smoke  # validates token via Telegram API

# 3. Start
docker compose up -d --build
curl http://127.0.0.1:8080/health
curl http://127.0.0.1:8080/metrics
```

<details>
<summary>Webhook + nginx config</summary>

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
WEBHOOK_URL=https://shop.example.com/webhook/telegram
TELEGRAM_WEBHOOK_SECRET=random-secret-string
```

</details>

---

### 🏗️ Project Structure

```
telegram-shop-bot/
├── cmd/
│   ├── bot/               # Entrypoint — start the bot
│   ├── preflight/         # Pre-launch env check
│   ├── seed/              # Load demo data
│   ├── telegram-smoke/    # Smoke test via Telegram API
│   └── usability-smoke/   # Buyer flow smoke test (no token)
├── internal/
│   ├── bot/               # Handlers, middleware, UI
│   ├── config/            # Configuration loading
│   ├── payment/           # Stars & CryptoBot adapters
│   ├── service/           # Business logic
│   ├── shop/              # Catalog, cart, orders
│   └── storage/           # SQLite / Redis, migrations
├── locales/               # Translations (ru.json, en.json)
├── worker/                # Background workers
├── monitoring/            # Grafana dashboard JSON
├── Dockerfile
├── docker-compose.yml
├── Makefile
└── .env.example
```

---

### 🛠️ Makefile

```bash
make build      # Build all binaries
make test       # Run tests
make lint       # go vet
make run        # Start the bot
make seed       # Load demo data
make preflight  # Check environment
```

---

### 🔒 Security

- Webhook signature verification (HMAC-SHA256) for CryptoBot
- Secret token support for Telegram webhooks
- Admin access restricted to explicit Telegram ID list
- Docker container runs as non-root user
- Tokens and secrets never appear in logs

Found a vulnerability? See [SECURITY.md](SECURITY.md).

---

### 🤝 Contributing

1. Fork the repo
2. Create a branch: `git checkout -b feature/my-feature`
3. Run tests: `go test ./...`
4. Run linter: `go vet ./...`
5. Open a Pull Request

See [CONTRIBUTING.md](CONTRIBUTING.md) for details.

---

### 📄 License

MIT — do whatever you want. See [LICENSE](LICENSE).

---
---

## 🇷🇺 Русский

<p align="center">
  <a href="#-english">🇬🇧 Switch to English</a>
</p>

Полноценный интернет-магазин внутри Telegram — каталог, корзина, оплата Stars и USDT, промокоды, витрина, программа лояльности, реферальная система и панель администратора. Один бинарник. Без CGO. Без лишнего.

### ✨ Возможности

<table>
<tr>
<td width="50%">

**🛍️ Покупателям**
- Каталог товаров с категориями
- Корзина и оформление заказа в Telegram
- Оплата **Telegram Stars** (встроено)
- Оплата **USDT через CryptoBot** (опционально)
- Промокоды с ограничениями по категориям
- Список желаний — уведомления о снижении цены и появлении товара
- Поиск: `/search <запрос>`
- Реферальная программа с защитой от фрода

</td>
<td width="50%">

**🔧 Администраторам**
- Управление товарами и категориями
- Заказы и изменение статусов
- Управление промокодами
- Аналитика и выгрузка в CSV
- **Настройка цветов кнопок** — `/btnstyle` интерактивное меню: Primary/Success/Danger/Default для каждой кнопки
- Вход: `/admin`

**⚙️ Инфраструктура**
- SQLite встроенная БД, миграции автоматически
- Redis FSM (fallback в память при отсутствии)
- Prometheus + Grafana метрики
- Health check на `:8080/health`
- Polling или Webhook (выбор по конфигу)
- Docker multi-stage, non-root пользователь
- i18n: русский и английский из коробки

</td>
</tr>
</table>

---

### 🚀 Быстрый старт

#### Вариант 1 — Docker Compose (рекомендуется)

> Нужен только Docker. Go устанавливать не нужно.

```bash
# 1. Клонируй репозиторий
git clone https://github.com/JumpCodeFrog/telegram-shop-bot.git
cd telegram-shop-bot

# 2. Создай файл конфигурации
cp .env.example .env
# Открой .env и установи BOT_TOKEN=твой_токен_от_@BotFather

# 3. Запусти
docker compose up -d --build

# 4. Проверь
curl http://127.0.0.1:8080/health

# Логи
docker compose logs -f bot
```

#### Вариант 2 — Локальный запуск

**Требования:** [Go 1.24+](https://go.dev/dl/) · Токен от [@BotFather](https://t.me/BotFather)

```bash
git clone https://github.com/JumpCodeFrog/telegram-shop-bot.git
cd telegram-shop-bot
cp .env.example .env        # установи BOT_TOKEN

go mod tidy
go test ./...               # запустить тесты
go run ./cmd/preflight      # проверить окружение (без реального бота)
go run ./cmd/bot            # запустить
```

---

### 🔑 Получить токен бота

1. Открой Telegram → найди **[@BotFather](https://t.me/BotFather)**
2. Напиши `/newbot`
3. Придумай имя и username для бота
4. Скопируй токен: `1234567890:ABCdefGHIjklMNOpqrsTUVwxyz`
5. Вставь в `.env` → `BOT_TOKEN=твой_токен`

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
| `TELEGRAM_WEBHOOK_SECRET` | нет | — | Секрет для верификации webhook |
| `LOCALES_DIR` | нет | `locales` | Путь к папке переводов |

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
| `/profile` | Профиль и статус лояльности |
| `/wishlist` | Список желаний |
| `/support` | Поддержка |
| `/paysupport` | Помощь с оплатой |
| `/terms` | Условия использования |
| `/help` | Список команд |
| `/cancel` | Отмена действия |
| `/admin` | Панель администратора *(только для админов)* |
| `/btnstyle` | Настройка цветов кнопок *(только для админов)* |

---

### 💳 Оплата

**Telegram Stars** — встроено, работает сразу. Курс задаётся через `USD_TO_STARS_RATE` (по умолчанию: 50 Stars = $1).

**CryptoBot (USDT)** — опционально. Установи `CRYPTOBOT_TOKEN` для включения.  
Фоновый воркер проверяет статус платежей каждые 30 секунд. Подпись верифицируется через HMAC-SHA256.

---

### 🌱 Тестовые данные

```bash
go run ./cmd/seed
```

Создаст категории (Одежда, Обувь, Аксессуары), 6 товаров и промокоды `WELCOME10` (−10%) и `SALE20` (−20%). Безопасно запускать повторно.

---

### 🚢 Деплой в продакшн

```bash
# 1. Установи BOT_TOKEN и ADMIN_IDS в .env
# 2. Проверки
go run ./cmd/preflight       # env, БД, Redis, webhook
go run ./cmd/telegram-smoke  # проверка токена через Telegram API

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
WEBHOOK_URL=https://shop.example.com/webhook/telegram
TELEGRAM_WEBHOOK_SECRET=случайная-строка
```

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
│   ├── bot/               # Хендлеры, middleware, UI
│   ├── config/            # Загрузка конфигурации
│   ├── payment/           # Адаптеры Stars и CryptoBot
│   ├── service/           # Бизнес-логика
│   ├── shop/              # Каталог, корзина, заказы
│   └── storage/           # SQLite / Redis, миграции
├── locales/               # Переводы (ru.json, en.json)
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
make run        # Запустить бота
make seed       # Загрузить демо-данные
make preflight  # Проверить окружение
```

---

### 🔒 Безопасность

- Верификация подписи webhook (HMAC-SHA256) для CryptoBot
- Поддержка секретного токена для Telegram webhook
- Доступ в админку только по явному списку Telegram ID
- Docker-контейнер работает от non-root пользователя
- Токены и секреты не попадают в логи

Нашёл уязвимость? Смотри [SECURITY.md](SECURITY.md).

---

### 🌍 Локализация

Переводы в `locales/`. Доступны:
- 🇷🇺 `ru.json` — русский (по умолчанию)
- 🇬🇧 `en.json` — английский

Чтобы добавить новый язык — создай `locales/de.json` по образцу. Язык выбирается автоматически по настройкам Telegram.

---

### 🤝 Участие в разработке

1. Форкни репозиторий
2. Создай ветку: `git checkout -b feature/my-feature`
3. Запусти тесты: `go test ./...`
4. Запусти линтер: `go vet ./...`
5. Открой Pull Request

Подробнее в [CONTRIBUTING.md](CONTRIBUTING.md).

---

### 📄 Лицензия

MIT — делай что хочешь. Смотри [LICENSE](LICENSE).
