-- Upgrade databases that already applied 017: provider occurrence timestamps
-- are part of the immutable signed fact, just like amount and provider IDs.
DROP TRIGGER IF EXISTS payment_attempts_identity_no_update;
CREATE TRIGGER payment_attempts_identity_no_update
BEFORE UPDATE OF order_id, provider, external_id, payer_id, amount_minor, currency, scale, occurred_at
ON payment_attempts BEGIN
    SELECT RAISE(ABORT, 'payment_attempt identity is immutable');
END;

DROP TRIGGER IF EXISTS refunds_identity_no_update;
CREATE TRIGGER refunds_identity_no_update
BEFORE UPDATE OF order_id, provider, external_id, payment_external_id, payer_id,
                 amount_minor, currency, scale, completed_at
ON refunds BEGIN
    SELECT RAISE(ABORT, 'refund identity is immutable');
END;
