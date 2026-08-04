-- Product reviews left by buyers: rating 1..5 with optional text.
-- One review per (product, user); editing replaces via upsert.
CREATE TABLE IF NOT EXISTS reviews (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    product_id  INTEGER NOT NULL REFERENCES products(id),
    user_id     INTEGER NOT NULL,
    order_id    INTEGER REFERENCES orders(id),
    rating      INTEGER NOT NULL CHECK (rating BETWEEN 1 AND 5),
    text        TEXT,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(product_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_reviews_product_id ON reviews(product_id);
