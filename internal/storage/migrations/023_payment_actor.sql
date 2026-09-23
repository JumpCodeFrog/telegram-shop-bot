-- Roadmap 4.15: durable actor attribution on the immutable payment ledger.
-- NULL means "not recorded" (all pre-4.15 rows); new rows carry the ingress
-- identity (webhook:<provider> / worker:<provider> / admin:<tgID> / CLI --actor).
-- No backfill: the ledger is append-only and history is not rewritten.
ALTER TABLE payment_events
    ADD COLUMN actor TEXT NULL CHECK (actor IS NULL OR length(actor) BETWEEN 1 AND 128);
ALTER TABLE payment_anomalies
    ADD COLUMN actor TEXT NULL CHECK (actor IS NULL OR length(actor) BETWEEN 1 AND 128);
