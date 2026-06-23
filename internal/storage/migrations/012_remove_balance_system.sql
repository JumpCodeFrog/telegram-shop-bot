-- Remove legacy balance system
DROP TABLE IF EXISTS balance_txs;

-- SQLite doesn't support DROP COLUMN in all versions,
-- but modernc.org/sqlite (which this project uses) supports it if it's based on a recent enough SQLite.
-- Let's try to use ALTER TABLE DROP COLUMN.
ALTER TABLE users DROP COLUMN balance_usd;
