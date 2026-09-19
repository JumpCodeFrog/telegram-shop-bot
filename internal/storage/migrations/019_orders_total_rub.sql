-- Orders paid via YooKassa record their converted RUB total so the immutable
-- ledger can validate RUB card captures. Stars and crypto orders keep 0.
ALTER TABLE orders ADD COLUMN total_rub REAL DEFAULT 0;
