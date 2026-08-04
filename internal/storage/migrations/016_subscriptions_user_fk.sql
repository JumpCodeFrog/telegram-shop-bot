-- subscriptions.user_id stores the buyer's Telegram ID (same convention as
-- orders.user_id and every bot-layer call site), not an internal users.id.
-- Migration 014 declared REFERENCES users(id), so with foreign_keys(1) every
-- real-world insert failed. SQLite cannot drop a constraint in place —
-- rebuild the table without the users FK, keeping data and indexes.
CREATE TABLE subscriptions_fixed (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id             INTEGER NOT NULL,
    product_id          INTEGER NOT NULL REFERENCES products(id),
    order_id            INTEGER REFERENCES orders(id),
    telegram_charge_id  TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL DEFAULT 'active',
    expires_at          DATETIME NOT NULL,
    reminded_at         DATETIME,
    created_at          DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at          DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, product_id)
);

INSERT INTO subscriptions_fixed (id, user_id, product_id, order_id, telegram_charge_id,
                                 status, expires_at, reminded_at, created_at, updated_at)
SELECT id, user_id, product_id, order_id, telegram_charge_id,
       status, expires_at, reminded_at, created_at, updated_at
FROM subscriptions;

DROP TABLE subscriptions;
ALTER TABLE subscriptions_fixed RENAME TO subscriptions;

CREATE INDEX IF NOT EXISTS idx_subscriptions_user_id    ON subscriptions(user_id);
CREATE INDEX IF NOT EXISTS idx_subscriptions_expires_at ON subscriptions(expires_at);
