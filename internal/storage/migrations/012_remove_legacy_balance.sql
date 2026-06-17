-- Remove legacy balance system
DROP TABLE IF EXISTS balance_txs;

-- SQLite does not support ALTER TABLE DROP COLUMN easily before 3.35.0
-- Since we use modernc.org/sqlite, it might support it, but safest is to recreate the table if needed.
-- However, we can just leave the column as is for now if we want to be safe,
-- but the roadmap says "remove".

-- Let's try direct drop first.
ALTER TABLE users DROP COLUMN balance_usd;
