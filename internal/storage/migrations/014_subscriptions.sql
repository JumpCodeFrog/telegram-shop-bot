-- Stars subscriptions.
-- products.sub_period_days > 0 marks a subscription product (MVP: 30 days).
ALTER TABLE products ADD COLUMN sub_period_days INTEGER NOT NULL DEFAULT 0;

-- status: "active" | "canceled" | "expired"; renewal moves expires_at forward
-- and clears reminded_at (see SubscriptionStore.Upsert).
CREATE TABLE IF NOT EXISTS subscriptions (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id             INTEGER NOT NULL REFERENCES users(id),
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

CREATE INDEX IF NOT EXISTS idx_subscriptions_user_id    ON subscriptions(user_id);
CREATE INDEX IF NOT EXISTS idx_subscriptions_expires_at ON subscriptions(expires_at);
