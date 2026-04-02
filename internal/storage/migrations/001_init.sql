CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    telegram_id   INTEGER UNIQUE NOT NULL,
    username      TEXT,
    first_name    TEXT,
    language_code TEXT    DEFAULT 'en',
    is_premium    BOOLEAN DEFAULT 0,
    balance_usd   REAL    DEFAULT 0,
    loyalty_pts   INTEGER DEFAULT 0,
    loyalty_level TEXT    DEFAULT 'bronze',
    referral_code TEXT    UNIQUE,
    referred_by   INTEGER REFERENCES users(id),
    created_at    DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS categories (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL,
    emoji           TEXT,
    custom_emoji_id TEXT,
    sort_order      INTEGER DEFAULT 0,
    is_active       BOOLEAN DEFAULT 1
);

CREATE TABLE IF NOT EXISTS products (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    category_id INTEGER REFERENCES categories(id),
    name        TEXT NOT NULL,
    description TEXT,
    photo_url   TEXT,
    price_usd   REAL NOT NULL,
    price_stars INTEGER NOT NULL DEFAULT 0,
    stock       INTEGER DEFAULT 0,
    is_digital  BOOLEAN DEFAULT 0,
    digital_content TEXT,
    is_active   BOOLEAN DEFAULT 1,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS cart_items (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL,
    product_id  INTEGER REFERENCES products(id),
    quantity    INTEGER DEFAULT 1,
    added_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
    recovery_sent_at DATETIME,
    UNIQUE(user_id, product_id)
);

CREATE TABLE IF NOT EXISTS orders (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id         INTEGER NOT NULL,
    total_usd       REAL,
    total_stars     INTEGER,
    payment_method  TEXT,
    payment_id      TEXT,
    status          TEXT DEFAULT 'pending',
    discount_pct    INTEGER DEFAULT 0,
    promo_code      TEXT,
    updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS order_items (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    order_id    INTEGER REFERENCES orders(id),
    product_id  INTEGER REFERENCES products(id),
    quantity    INTEGER,
    price_usd   REAL,
    product_name TEXT
);

CREATE TABLE IF NOT EXISTS balance_txs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER REFERENCES users(id),
    amount_usd  REAL NOT NULL,
    type        TEXT NOT NULL,
    ref_id      TEXT,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS loyalty_txs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER REFERENCES users(id),
    pts         INTEGER NOT NULL,
    reason      TEXT,
    ref_id      TEXT,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS achievements (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    code        TEXT UNIQUE NOT NULL,
    title       TEXT,
    pts_reward  INTEGER DEFAULT 0
);

CREATE TABLE IF NOT EXISTS user_achievements (
    user_id         INTEGER REFERENCES users(id),
    achievement_id  INTEGER REFERENCES achievements(id),
    earned_at       DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, achievement_id)
);

CREATE TABLE IF NOT EXISTS referral_stats (
    user_id         INTEGER PRIMARY KEY REFERENCES users(id),
    total_referrals INTEGER DEFAULT 0,
    total_earned    REAL    DEFAULT 0
);

CREATE TABLE IF NOT EXISTS wishlist (
    user_id         INTEGER REFERENCES users(id),
    product_id      INTEGER REFERENCES products(id),
    price_at_added  REAL,
    stock_at_added  INTEGER,
    added_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, product_id)
);

CREATE TABLE IF NOT EXISTS promo_codes (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    code          TEXT UNIQUE NOT NULL,
    discount      INTEGER DEFAULT 0,
    max_uses      INTEGER,
    used_count    INTEGER DEFAULT 0,
    expires_at    DATETIME,
    category_id   INTEGER REFERENCES categories(id),
    is_active     BOOLEAN DEFAULT 1,
    created_at    DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS promo_usages (
    promo_id    INTEGER NOT NULL REFERENCES promo_codes(id),
    user_id     INTEGER NOT NULL,
    order_id    INTEGER NOT NULL REFERENCES orders(id),
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (promo_id, user_id)
);
