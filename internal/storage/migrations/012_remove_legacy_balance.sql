-- Remove legacy balance system.
-- SQLite doesn't support DROP COLUMN in older versions (pre-3.35.0).
-- Since modernc.org/sqlite is modern, we can use ALTER TABLE DROP COLUMN.

DROP TABLE IF EXISTS balance_txs;

-- If DROP COLUMN is not supported, we'd need to recreate the table.
-- But modernc.org/sqlite (v1.34+) supports it.
ALTER TABLE users DROP COLUMN balance_usd;
