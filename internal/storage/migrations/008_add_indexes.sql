-- Performance indexes for hot query paths.
CREATE INDEX IF NOT EXISTS idx_orders_user_id       ON orders(user_id);
CREATE INDEX IF NOT EXISTS idx_orders_status        ON orders(status);
CREATE INDEX IF NOT EXISTS idx_cart_items_user_id   ON cart_items(user_id);
CREATE INDEX IF NOT EXISTS idx_wishlist_user_id     ON wishlist(user_id);
CREATE INDEX IF NOT EXISTS idx_loyalty_txs_user_id  ON loyalty_txs(user_id);
CREATE INDEX IF NOT EXISTS idx_products_category_id ON products(category_id);
CREATE INDEX IF NOT EXISTS idx_products_is_active   ON products(is_active);
