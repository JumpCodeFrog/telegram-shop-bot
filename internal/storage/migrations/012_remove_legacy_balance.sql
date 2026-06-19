-- 012_remove_legacy_balance.sql
-- Drop legacy balance system

DROP TABLE IF EXISTS balance_txs;

-- SQLite doesn't support DROP COLUMN in older versions, but modern ones do.
-- shop_bot uses modernc.org/sqlite which supports it.
ALTER TABLE users DROP COLUMN balance_usd;
