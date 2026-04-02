-- 001_init.down.sql
-- Compatibility snapshot only.
-- The active runtime schema is embedded from `internal/storage/migrations/`.

DROP TABLE IF EXISTS promo_usages;
DROP TABLE IF EXISTS referral_stats;
DROP TABLE IF EXISTS wishlist;
DROP TABLE IF EXISTS promo_codes;
DROP TABLE IF EXISTS user_achievements;
DROP TABLE IF EXISTS achievements;
DROP TABLE IF EXISTS loyalty_txs;
DROP TABLE IF EXISTS balance_txs;
DROP TABLE IF EXISTS order_items;
DROP TABLE IF EXISTS orders;
DROP TABLE IF EXISTS cart_items;
DROP TABLE IF EXISTS products;
DROP TABLE IF EXISTS categories;
DROP TABLE IF EXISTS users;
