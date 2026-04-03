<p align="center">
  <img src="https://readme-typing-svg.demolab.com?font=Fira+Code&size=28&pause=1000&color=229ED9&center=true&vCenter=true&width=600&lines=Telegram+Shop+Bot;Built+with+Go+%F0%9F%90%B9;Open+Source+%E2%9C%A8" alt="Typing SVG" />
</p>

<p align="center">
  <a href="https://github.com/JumpCodeFrog/telegram-shop-bot/actions/workflows/ci.yml">
    <img src="https://github.com/JumpCodeFrog/telegram-shop-bot/actions/workflows/ci.yml/badge.svg" alt="CI" />
  </a>
  <img src="https://img.shields.io/badge/Go-1.23+-00ADD8?logo=go&logoColor=white" alt="Go version" />
  <img src="https://img.shields.io/badge/SQLite-embedded-003B57?logo=sqlite&logoColor=white" alt="SQLite" />
  <img src="https://img.shields.io/badge/Redis-optional-DC382D?logo=redis&logoColor=white" alt="Redis" />
  <img src="https://img.shields.io/badge/Docker-ready-2496ED?logo=docker&logoColor=white" alt="Docker" />
  <img src="https://img.shields.io/github/license/JumpCodeFrog/telegram-shop-bot?color=green" alt="License" />
</p>

<p align="center">
  Полноценный магазин внутри Telegram — каталог, корзина, оплата Stars и USDT, промокоды, витрина, админка.<br/>
  Один бинарник. Без лишних зависимостей.
</p>

---

## ✨ Что умеет бот

### 🛍️ Для покупателей
- Каталог товаров с категориями и карточками
- Корзина и оформление заказа прямо в Telegram
- Оплата **Telegram Stars** (встроено) и **USDT через CryptoBot** (опционально)
- Промокоды со скидками, лимитами и ограничениями по категориям
- Список желаний — уведомления о снижении цены и появлении товара
- Поиск: `/search <запрос>`
- Реферальная программа с защитой от фрода

### 🔧 Для администратора
- Управление товарами и категориями (добавить / изменить / удалить)
- Управление заказами и статусами
- Промокоды: создание, редактирование, удаление
- Аналитика с выгрузкой в CSV
- Вход: `/admin`

### ⚙️ Инфраструктура
- **SQLite** — встроенная база данных, миграции автоматически
- **Redis** — FSM состояний и кэш (опционально, при отсутствии — fallback в память)
- **Prometheus + Grafana** — метрики из коробки
- **Health check** на `:8080/health`
- Фоновые воркеры: бэкапы, восстановление брошенных корзин, вишлист, CryptoBot polling
- Polling или Webhook режим — выбирается автоматически по конфигу
- Docker с multi-stage сборкой и запуском от non-root пользователя
- i18n: русский и английский из коробки

---

## 🚀 Быстрый старт

### Вариант 1: Docker Compose (рекомендуется)

> Нужен только Docker. Go устанавливать не нужно.

```bash
# 1. Клонируй репозиторий
git clone https://github.com/JumpCodeFrog/telegram-shop-bot.git
cd telegram-shop-bot

# 2. Создай файл конфигурации
cp .env.example .env

# 3. Открой .env и вставь токен бота (обязательно!)
nano .env
# BOT_TOKEN=твой_токен_от_@BotFather

# 4. Запусти
docker compose up -d --build

# 5. Проверь что всё работает
docker compose ps
curl http://127.0.0.1:8080/health
```

Готово! Бот работает. Логи:
```bash
docker compose logs -f bot
```

---

### Вариант 2: Локальный запуск

**Требования:**
- [Go 1.23+](https://go.dev/dl/)
- Токен бота от [@BotFather](https://t.me/BotFather)
- Redis (опционально)

```bash
# 1. Клонируй и настрой
git clone https://github.com/JumpCodeFrog/telegram-shop-bot.git
cd telegram-shop-bot
cp .env.example .env
nano .env   # вставь BOT_TOKEN

# 2. Собери и проверь
go mod tidy
go build ./...
go test ./...

# 3. Проверь окружение (без реального бота)
go run ./cmd/preflight

# 4. Запусти бота
go run ./cmd/bot
```

---

## 🔑 Получить токен бота

1. Открой Telegram и найди [@BotFather](https://t.me/BotFather)
2. Напиши `/newbot`
3. Придумай имя и username для бота
4. Скопируй токен — он выглядит так: `1234567890:ABCdefGHIjklMNOpqrsTUVwxyz`
5. Вставь в `.env` → `BOT_TOKEN=твой_токен`

---

## ⚙️ Конфигурация

Все настройки в файле `.env`. Скопируй `.env.example` для начала.

| Переменная | Обязательна | По умолчанию | Описание |
|---|---|---|---|
| `BOT_TOKEN` | **да** | — | Токен от @BotFather |
| `BOT_USERNAME` | нет | — | @username бота (без @), для реферальных ссылок |
| `CRYPTOBOT_TOKEN` | нет | — | Токен CryptoBot для оплаты USDT |
| `ADMIN_IDS` | нет | — | Telegram ID администраторов через запятую |
| `DB_PATH` | нет | `data/shop.db` | Путь к файлу SQLite |
| `LOG_LEVEL` | нет | `info` | Уровень логов: `debug`, `info`, `warn`, `error` |
| `APP_ENV` | нет | `development` | `production` для JSON-логов |
| `REDIS_ADDR` | нет | `localhost:6379` | Адрес Redis |
| `REDIS_PASSWORD` | нет | — | Пароль Redis |
| `USD_TO_STARS_RATE` | нет | `50` | Stars за 1 USD |
| `WEBHOOK_URL` | нет | — | Публичный URL для webhook-режима |
| `TELEGRAM_WEBHOOK_SECRET` | нет | — | Секрет для верификации webhook |
| `LOCALES_DIR` | нет | `locales` | Путь к папке с переводами |

> 💡 Без `WEBHOOK_URL` бот работает в режиме polling (удобно для разработки).
> Без Redis — автоматически использует хранилище в памяти.

---

## 🌱 Тестовые данные

Заполни базу демо-товарами, категориями и промокодами:

```bash
go run ./cmd/seed
```

Создаст:
- **Категории:** Одежда 👕, Обувь 👟, Аксессуары 🎒
- **Товары:** 6 позиций с ценами в USD и Stars
- **Промокоды:** `WELCOME10` (−10%, 100 использований), `SALE20` (−20%, 50 использований)

Безопасно запускать повторно.

---

## 📋 Команды бота

### Для покупателей

| Команда | Описание |
|---|---|
| `/start` | Главное меню |
| `/catalog` | Каталог товаров |
| `/search <запрос>` | Поиск товаров |
| `/cart` | Корзина |
| `/orders` | История заказов |
| `/profile` | Профиль |
| `/wishlist` | Список желаний |
| `/support` | Поддержка |
| `/paysupport` | Помощь с оплатой |
| `/terms` | Условия использования |
| `/help` | Список команд |
| `/cancel` | Отмена действия |

### Для администратора

| Команда | Описание |
|---|---|
| `/admin` | Панель администратора |

> Добавь свой Telegram ID в `ADMIN_IDS` чтобы получить доступ.

---

## 💳 Оплата

### Telegram Stars
Встроено, работает сразу. Бот отправляет инвойс Stars и автоматически обрабатывает `successful_payment`.
Курс настраивается через `USD_TO_STARS_RATE` (по умолчанию: 50 Stars = $1).

### CryptoBot (USDT)
Опционально. Установи `CRYPTOBOT_TOKEN` для включения.
- Создаёт инвойсы через CryptoBot API
- Фоновый воркер проверяет статус платежей каждые 30 секунд
- Верификация подписи через HMAC-SHA256

---

## 🚢 Деплой в продакшн

### Чеклист

1. Установи настоящий `BOT_TOKEN`
2. Установи `ADMIN_IDS` — свой Telegram ID
3. Выбери режим: polling или webhook
4. Если webhook — `WEBHOOK_URL` должен быть публичным HTTPS-адресом
5. Запусти проверки:

```bash
go run ./cmd/preflight        # env, БД, Redis, webhook
go run ./cmd/telegram-smoke   # проверка токена через Telegram API
```

6. Запусти и проверь:

```bash
docker compose up -d --build
curl http://127.0.0.1:8080/health
curl http://127.0.0.1:8080/metrics
```

### Webhook + nginx

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

### Docker команды

```bash
docker compose up -d --build   # старт / пересборка
docker compose logs -f bot     # логи в реальном времени
docker compose restart bot     # перезапуск бота
docker compose down            # остановка
```

---

## 🏗️ Структура проекта

```
telegram-shop-bot/
├── cmd/
│   ├── bot/               # Точка входа — запуск бота
│   ├── preflight/         # Проверка окружения перед запуском
│   ├── seed/              # Заполнение демо-данными
│   ├── telegram-smoke/    # Smoke-тест Telegram API (нужен токен)
│   └── usability-smoke/   # Smoke-тест пути покупателя (без токена)
├── internal/
│   ├── bot/               # Telegram-хендлеры, middleware, UI
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

## 🛠️ Makefile

```bash
make build      # Собрать все бинарники
make test       # Запустить тесты
make lint       # go vet
make run        # Запустить бота
make seed       # Загрузить демо-данные
make preflight  # Проверить окружение
```

---

## 🌍 Локализация

Переводы хранятся в `locales/`. Сейчас доступны:
- 🇷🇺 `ru.json` — русский (по умолчанию)
- 🇬🇧 `en.json` — английский

Чтобы добавить новый язык — создай `locales/de.json` по образцу существующих.
Язык выбирается автоматически по настройке Telegram у пользователя.

---

## 🔒 Безопасность

- Верификация подписи webhook (HMAC-SHA256) для CryptoBot
- Поддержка секретного токена для Telegram webhook
- Доступ в админку только по явному списку Telegram ID
- Docker-контейнер работает от non-root пользователя
- Токены и секреты не попадают в логи

Нашёл уязвимость? Смотри [SECURITY.md](SECURITY.md).

---

## 🤝 Участие в разработке

1. Форкни репозиторий
2. Создай ветку: `git checkout -b feature/my-feature`
3. Запусти тесты: `go test ./...`
4. Запусти линтер: `go vet ./...`
5. Открой Pull Request

Подробнее в [CONTRIBUTING.md](CONTRIBUTING.md).

---

## 📄 Лицензия

MIT — делай что хочешь. Смотри [LICENSE](LICENSE).
