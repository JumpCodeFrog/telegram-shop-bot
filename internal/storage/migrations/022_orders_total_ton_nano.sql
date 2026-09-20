-- Orders paid via TON record their converted nanoTON total so the immutable
-- ledger can validate TON captures. Stars, crypto, RUB and balance orders
-- keep 0.
ALTER TABLE orders ADD COLUMN total_ton_nano INTEGER NOT NULL DEFAULT 0;
