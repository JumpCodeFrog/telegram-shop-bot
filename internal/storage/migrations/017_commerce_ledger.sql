-- Wave 1: keep the legacy orders.status projection while introducing
-- independent commerce state axes and an append-only payment timeline.
ALTER TABLE orders ADD COLUMN order_state TEXT NOT NULL DEFAULT 'placed'
    CHECK (order_state IN ('placed', 'cancelled', 'completed'));
ALTER TABLE orders ADD COLUMN payment_state TEXT NOT NULL DEFAULT 'pending'
    CHECK (payment_state IN ('pending', 'settled', 'partially_refunded', 'refunded', 'cancelled', 'needs_review'));
ALTER TABLE orders ADD COLUMN fulfillment_state TEXT NOT NULL DEFAULT 'unfulfilled'
    CHECK (fulfillment_state IN ('unfulfilled', 'fulfilled'));
-- Immutable recurring-invoice snapshot. Settlement must not depend on a
-- product that an administrator may edit after the invoice was issued.
ALTER TABLE orders ADD COLUMN subscription_product_id INTEGER REFERENCES products(id);
ALTER TABLE orders ADD COLUMN subscription_period_days INTEGER NOT NULL DEFAULT 0
    CHECK (subscription_period_days >= 0);

UPDATE orders
SET order_state = CASE status
        WHEN 'cancelled' THEN 'cancelled'
        WHEN 'delivered' THEN 'completed'
        ELSE 'placed'
    END,
    payment_state = CASE status
        WHEN 'paid' THEN 'settled'
        WHEN 'delivered' THEN 'settled'
        WHEN 'cancelled' THEN 'cancelled'
        ELSE 'pending'
    END,
    fulfillment_state = CASE status
        WHEN 'delivered' THEN 'fulfilled'
        ELSE 'unfulfilled'
    END;

CREATE TABLE order_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    order_id    INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    event_type  TEXT NOT NULL,
    from_state  TEXT NOT NULL DEFAULT '',
    to_state    TEXT NOT NULL DEFAULT '',
    metadata    TEXT NOT NULL DEFAULT '{}',
    occurred_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- A legacy subscription row is stronger evidence than the current catalog:
-- administrators may have edited the product after the original invoice.
UPDATE orders
SET subscription_product_id = (
        SELECT s.product_id FROM subscriptions s
        WHERE s.order_id = orders.id
    ),
    subscription_period_days = COALESCE(NULLIF((
        SELECT p.sub_period_days
        FROM subscriptions s JOIN products p ON p.id = s.product_id
        WHERE s.order_id = orders.id
    ), 0), 30)
WHERE (SELECT COUNT(*) FROM subscriptions s WHERE s.order_id = orders.id) = 1;

-- Backfill only orders that unambiguously contain one subscription unit.
UPDATE orders
SET subscription_product_id = (
        SELECT oi.product_id
        FROM order_items oi JOIN products p ON p.id = oi.product_id
        WHERE oi.order_id = orders.id AND oi.quantity = 1 AND p.sub_period_days > 0
    ),
    subscription_period_days = (
        SELECT p.sub_period_days
        FROM order_items oi JOIN products p ON p.id = oi.product_id
        WHERE oi.order_id = orders.id AND oi.quantity = 1 AND p.sub_period_days > 0
    )
WHERE subscription_product_id IS NULL
  AND (SELECT COUNT(*) FROM order_items oi WHERE oi.order_id = orders.id) = 1
  AND (SELECT COUNT(*)
       FROM order_items oi JOIN products p ON p.id = oi.product_id
       WHERE oi.order_id = orders.id AND oi.quantity = 1 AND p.sub_period_days > 0) = 1;

-- A legacy replacement order against an active or canceled-but-unexpired
-- entitlement must not reach pre-checkout. The provider charge would be real
-- even though automatic activation must reject it.
UPDATE orders
SET payment_state = 'needs_review'
WHERE status = 'pending'
  AND subscription_product_id IS NOT NULL
  AND EXISTS (
      SELECT 1 FROM subscriptions s
      WHERE s.user_id = orders.user_id
        AND s.product_id = orders.subscription_product_id
        AND s.status IN ('active', 'canceled')
        AND s.expires_at > CURRENT_TIMESTAMP
  );

INSERT INTO order_events (order_id, event_type, from_state, to_state, metadata)
SELECT id, 'subscription.entitlement_conflict_quarantined', 'pending', 'needs_review',
       '{"source":"migration_017"}'
FROM orders
WHERE status = 'pending'
  AND subscription_product_id IS NOT NULL
  AND payment_state = 'needs_review'
  AND EXISTS (
      SELECT 1 FROM subscriptions s
      WHERE s.user_id = orders.user_id
        AND s.product_id = orders.subscription_product_id
        AND s.status IN ('active', 'canceled')
        AND s.expires_at > CURRENT_TIMESTAMP
  );

-- Old databases may already contain duplicate unpaid recurring orders. Keep
-- the oldest reservation payable and quarantine the rest instead of failing
-- the whole atomic migration.
UPDATE orders
SET payment_state = 'needs_review'
WHERE id IN (
    SELECT duplicate.id
    FROM orders duplicate
    WHERE duplicate.status = 'pending'
      AND duplicate.subscription_product_id IS NOT NULL
      AND EXISTS (
          SELECT 1 FROM orders keeper
          WHERE keeper.user_id = duplicate.user_id
            AND keeper.subscription_product_id = duplicate.subscription_product_id
            AND keeper.status = 'pending'
            AND keeper.id < duplicate.id
      )
);

INSERT INTO order_events (order_id, event_type, from_state, to_state, metadata)
SELECT id, 'subscription.duplicate_quarantined', 'pending', 'needs_review',
       '{"source":"migration_017"}'
FROM orders
WHERE status = 'pending'
  AND subscription_product_id IS NOT NULL
  AND payment_state = 'needs_review'
  AND EXISTS (
      SELECT 1 FROM orders keeper
      WHERE keeper.user_id = orders.user_id
        AND keeper.subscription_product_id = orders.subscription_product_id
        AND keeper.status = 'pending'
        AND keeper.id < orders.id
  );

-- The index closes concurrent insert races for new payable reservations. A
-- quarantined needs_review order is also checked explicitly by CreateOrder.
CREATE UNIQUE INDEX idx_orders_pending_subscription
ON orders(user_id, subscription_product_id)
WHERE subscription_product_id IS NOT NULL
  AND status = 'pending'
  AND payment_state = 'pending';

CREATE TABLE payment_attempts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
	order_id    INTEGER NOT NULL REFERENCES orders(id),
	provider    TEXT NOT NULL CHECK (provider IN ('stars', 'crypto')),
	external_id TEXT NOT NULL,
	payer_id    INTEGER NOT NULL DEFAULT 0,
    amount_minor INTEGER NOT NULL CHECK (amount_minor >= 0),
    currency    TEXT NOT NULL,
    scale       INTEGER NOT NULL CHECK (scale BETWEEN 0 AND 9),
    status      TEXT NOT NULL CHECK (status IN ('observed', 'succeeded', 'needs_review')),
    entitlement_expires_at DATETIME,
    occurred_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(provider, external_id)
);

CREATE TABLE payment_events (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    order_id           INTEGER NOT NULL REFERENCES orders(id),
    payment_attempt_id INTEGER REFERENCES payment_attempts(id),
    provider           TEXT NOT NULL CHECK (provider IN ('stars', 'crypto')),
    event_kind         TEXT NOT NULL CHECK (event_kind IN ('captured', 'refunded', 'chargeback', 'identity_conflict')),
    external_id        TEXT NOT NULL,
    amount_minor       INTEGER NOT NULL CHECK (amount_minor >= 0),
    currency           TEXT NOT NULL,
    scale              INTEGER NOT NULL CHECK (scale BETWEEN 0 AND 9),
    disposition        TEXT NOT NULL DEFAULT 'observed'
                       CHECK (disposition IN ('observed', 'settled', 'needs_review')),
    occurred_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(provider, event_kind, external_id)
);

-- Signed provider facts that cannot yet be attached to the immutable payment
-- ledger (unknown order, amount/currency/payer mismatch). proposed_order_id is
-- intentionally not a foreign key so orphan captures remain durable.
CREATE TABLE payment_anomalies (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    fingerprint       TEXT NOT NULL,
    proposed_order_id INTEGER NOT NULL DEFAULT 0,
    provider          TEXT NOT NULL CHECK (provider IN ('stars', 'crypto')),
    event_kind        TEXT NOT NULL DEFAULT 'captured'
                      CHECK (event_kind IN ('captured', 'refunded')),
    external_id       TEXT NOT NULL,
    related_external_id TEXT NOT NULL DEFAULT '',
    payer_id          INTEGER NOT NULL DEFAULT 0,
    amount_minor      INTEGER NOT NULL DEFAULT 0 CHECK (amount_minor >= 0),
    currency          TEXT NOT NULL,
    scale             INTEGER NOT NULL CHECK (scale BETWEEN 0 AND 9),
    raw_amount        TEXT NOT NULL DEFAULT '',
    raw_payload       TEXT NOT NULL DEFAULT '',
    reason            TEXT NOT NULL,
    occurred_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(provider, fingerprint)
);

CREATE TABLE refunds (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    order_id            INTEGER NOT NULL REFERENCES orders(id),
    provider            TEXT NOT NULL CHECK (provider IN ('stars', 'crypto')),
    external_id         TEXT NOT NULL,
	payment_external_id TEXT NOT NULL,
	payer_id             INTEGER NOT NULL DEFAULT 0,
    amount_minor        INTEGER NOT NULL CHECK (amount_minor > 0),
    currency            TEXT NOT NULL,
    scale               INTEGER NOT NULL CHECK (scale BETWEEN 0 AND 9),
    status              TEXT NOT NULL CHECK (status IN ('requested', 'succeeded', 'needs_reconcile', 'failed')),
    requested_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at        DATETIME,
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(provider, external_id)
);

-- Manual review closes through a separate append-only acknowledgement. The
-- provider facts remain immutable and queryable forever.
CREATE TABLE payment_resolutions (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    order_id                INTEGER NOT NULL DEFAULT 0,
    provider                TEXT NOT NULL CHECK (provider IN ('stars', 'crypto', 'unknown')),
    target_kind             TEXT NOT NULL CHECK (target_kind IN ('payment_event', 'payment_anomaly', 'order')),
    target_id               INTEGER NOT NULL CHECK (target_id > 0),
    decision                TEXT NOT NULL
                            CHECK (decision IN ('compensated', 'accepted_refund', 'dismissed', 'cancelled')),
    actor                   TEXT NOT NULL,
    reason                  TEXT NOT NULL,
    resulting_payment_state TEXT NOT NULL
                            CHECK (resulting_payment_state IN ('', 'settled', 'partially_refunded', 'refunded', 'cancelled', 'needs_review')),
    resolved_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(target_kind, target_id)
);

-- Every operator-applied provider ingress is attributed to the exact immutable
-- local fact created by the same transaction. Provider identities stay in the
-- referenced ledger row and are not duplicated into this audit surface.
CREATE TABLE payment_ingress_audits (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    order_id    INTEGER NOT NULL DEFAULT 0,
    provider    TEXT NOT NULL CHECK (provider IN ('stars', 'crypto')),
    event_kind  TEXT NOT NULL CHECK (event_kind IN ('captured', 'refunded')),
    target_kind TEXT NOT NULL CHECK (target_kind IN ('payment_event', 'refund', 'payment_anomaly')),
    target_id   INTEGER NOT NULL CHECK (target_id > 0),
    actor       TEXT NOT NULL CHECK (length(actor) BETWEEN 1 AND 128),
    reason      TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 512),
    applied_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(target_kind, target_id, actor, reason)
);

CREATE INDEX idx_order_events_order_time ON order_events(order_id, occurred_at, id);
CREATE INDEX idx_payment_attempts_order ON payment_attempts(order_id, created_at, id);
CREATE INDEX idx_payment_events_order_time ON payment_events(order_id, occurred_at, id);
CREATE INDEX idx_payment_anomalies_provider_time ON payment_anomalies(provider, occurred_at, id);
CREATE INDEX idx_refunds_order ON refunds(order_id, created_at, id);
CREATE INDEX idx_refunds_payment_identity ON refunds(provider, payment_external_id);
CREATE INDEX idx_payment_resolutions_order ON payment_resolutions(order_id, provider, resolved_at, id);
CREATE INDEX idx_payment_ingress_audits_order ON payment_ingress_audits(order_id, provider, applied_at, id);

-- Provider identity and refund identity must remain immutable after insert.
CREATE TRIGGER payment_attempts_identity_no_update
BEFORE UPDATE OF order_id, provider, external_id, payer_id, amount_minor, currency, scale, occurred_at
ON payment_attempts BEGIN
    SELECT RAISE(ABORT, 'payment_attempt identity is immutable');
END;
CREATE TRIGGER payment_attempts_entitlement_once
BEFORE UPDATE OF entitlement_expires_at ON payment_attempts
WHEN NOT (OLD.entitlement_expires_at IS NULL AND NEW.entitlement_expires_at IS NOT NULL)
BEGIN
    SELECT RAISE(ABORT, 'payment_attempt entitlement expiry is immutable');
END;
CREATE TRIGGER payment_attempts_no_delete
BEFORE DELETE ON payment_attempts BEGIN
    SELECT RAISE(ABORT, 'payment_attempts cannot be deleted');
END;
CREATE TRIGGER refunds_identity_no_update
BEFORE UPDATE OF order_id, provider, external_id, payment_external_id, payer_id,
	             amount_minor, currency, scale, completed_at
ON refunds BEGIN
    SELECT RAISE(ABORT, 'refund identity is immutable');
END;
CREATE TRIGGER refunds_no_delete
BEFORE DELETE ON refunds BEGIN
    SELECT RAISE(ABORT, 'refunds cannot be deleted');
END;

-- Legacy rows get an explicit imported timeline entry. A uniquely identified
-- paid capture is accepted as the prior application's settled fact; missing,
-- duplicate, or malformed identities are quarantined below for review.
INSERT INTO order_events (order_id, event_type, from_state, to_state, metadata, occurred_at)
SELECT id, 'order.legacy_imported', '', order_state,
       CASE WHEN status IN ('paid', 'delivered')
            THEN '{"source":"legacy_backfill","verified":false}'
            ELSE '{"source":"legacy_backfill"}' END,
       COALESCE(created_at, updated_at, CURRENT_TIMESTAMP)
FROM orders;

-- Import uniquely identifiable legacy captures as settled ledger facts.
-- Empty/duplicate provider IDs stay out of the identity ledger and are marked
-- needs_review below instead of aborting the upgrade.
INSERT INTO payment_attempts
	(order_id, provider, external_id, payer_id, amount_minor, currency, scale, status, occurred_at, created_at)
SELECT o.id,
       CASE WHEN lower(o.payment_method) = 'stars' THEN 'stars' ELSE 'crypto' END,
	   o.payment_id,
	   o.user_id,
       CASE WHEN lower(o.payment_method) = 'stars'
            THEN COALESCE(o.total_stars, 0)
            ELSE CAST(ROUND(COALESCE(o.total_usd, 0) * 100) AS INTEGER) END,
       CASE WHEN lower(o.payment_method) = 'stars' THEN 'XTR' ELSE 'USDT' END,
       CASE WHEN lower(o.payment_method) = 'stars' THEN 0 ELSE 2 END,
       'succeeded',
       COALESCE(o.updated_at, o.created_at, CURRENT_TIMESTAMP),
       COALESCE(o.created_at, o.updated_at, CURRENT_TIMESTAMP)
FROM orders o
WHERE o.status IN ('paid', 'delivered')
  AND lower(o.payment_method) IN ('stars', 'crypto', 'cryptobot')
  AND COALESCE(o.payment_id, '') <> ''
  AND CASE WHEN lower(o.payment_method) = 'stars'
           THEN COALESCE(o.total_stars, 0) > 0
           ELSE COALESCE(o.total_usd, 0) > 0 END
  AND 1 = (SELECT COUNT(*) FROM orders d
           WHERE d.status IN ('paid', 'delivered')
             AND (CASE WHEN lower(d.payment_method) = 'stars' THEN 'stars' ELSE 'crypto' END) =
                 (CASE WHEN lower(o.payment_method) = 'stars' THEN 'stars' ELSE 'crypto' END)
             AND d.payment_id = o.payment_id);

INSERT INTO payment_events
    (order_id, payment_attempt_id, provider, event_kind, external_id,
     amount_minor, currency, scale, disposition, occurred_at, created_at)
SELECT a.order_id, a.id, a.provider, 'captured', a.external_id,
       a.amount_minor, a.currency, a.scale, 'settled', a.occurred_at, a.created_at
FROM payment_attempts a
WHERE a.status = 'succeeded';

UPDATE orders
SET payment_state = 'needs_review'
WHERE status IN ('paid', 'delivered')
  AND NOT EXISTS (SELECT 1 FROM payment_attempts a WHERE a.order_id = orders.id);

-- Give every provider-scoped legacy quarantine a discoverable local target.
-- Invalid raw money stays textual while the normalized numeric column remains
-- non-negative and query-safe.
INSERT INTO payment_anomalies
    (fingerprint, proposed_order_id, provider, event_kind, external_id,
     amount_minor, currency, scale, raw_amount, reason, occurred_at)
SELECT 'legacy_capture_unverifiable:' || o.id,
       o.id,
       CASE WHEN lower(o.payment_method) = 'stars' THEN 'stars' ELSE 'crypto' END,
       'captured',
       COALESCE(o.payment_id, ''),
       CASE WHEN lower(o.payment_method) = 'stars'
            THEN MAX(COALESCE(o.total_stars, 0), 0)
            ELSE MAX(CAST(ROUND(COALESCE(o.total_usd, 0) * 100) AS INTEGER), 0) END,
       CASE WHEN lower(o.payment_method) = 'stars' THEN 'XTR' ELSE 'USDT' END,
       CASE WHEN lower(o.payment_method) = 'stars' THEN 0 ELSE 2 END,
       CASE WHEN (lower(o.payment_method) = 'stars' AND COALESCE(o.total_stars, 0) <= 0)
                  OR (lower(o.payment_method) IN ('crypto', 'cryptobot') AND COALESCE(o.total_usd, 0) <= 0)
            THEN CASE WHEN lower(o.payment_method) = 'stars'
                      THEN CAST(COALESCE(o.total_stars, 0) AS TEXT)
                      ELSE CAST(COALESCE(o.total_usd, 0) AS TEXT) END
            ELSE '' END,
       'legacy_capture_unverifiable',
       COALESCE(o.updated_at, o.created_at, CURRENT_TIMESTAMP)
FROM orders o
WHERE o.payment_state = 'needs_review'
  AND o.status IN ('paid', 'delivered')
  AND lower(o.payment_method) IN ('stars', 'crypto', 'cryptobot')
  AND NOT EXISTS (SELECT 1 FROM payment_attempts a WHERE a.order_id = o.id);

-- A legacy paid row with no trustworthy payment rail cannot be assigned to
-- Stars or crypto. Keep it quarantined as a provider-neutral order target; the
-- operator inbox exposes it under `unknown` without inventing a provider fact.
INSERT INTO order_events (order_id, event_type, from_state, to_state, metadata, occurred_at)
SELECT o.id, 'payment.legacy_provider_unknown', 'settled', 'needs_review',
       '{"source":"legacy_backfill","verified":false}',
       COALESCE(o.updated_at, o.created_at, CURRENT_TIMESTAMP)
FROM orders o
WHERE o.payment_state = 'needs_review'
  AND o.status IN ('paid', 'delivered')
  AND lower(COALESCE(o.payment_method, '')) NOT IN ('stars', 'crypto', 'cryptobot')
  AND NOT EXISTS (SELECT 1 FROM payment_attempts a WHERE a.order_id = o.id);

-- Provider facts are append-only. A capture disposition may advance exactly
-- once from observed to settled in the same settlement transaction; every
-- other field and transition remains immutable.
CREATE TRIGGER payment_events_no_update
BEFORE UPDATE ON payment_events
WHEN OLD.order_id <> NEW.order_id
  OR COALESCE(OLD.payment_attempt_id, 0) <> COALESCE(NEW.payment_attempt_id, 0)
  OR OLD.provider <> NEW.provider
  OR OLD.event_kind <> NEW.event_kind
  OR OLD.external_id <> NEW.external_id
  OR OLD.amount_minor <> NEW.amount_minor
  OR OLD.currency <> NEW.currency
  OR OLD.scale <> NEW.scale
  OR OLD.occurred_at <> NEW.occurred_at
  OR OLD.created_at <> NEW.created_at
  OR NOT (OLD.disposition = 'observed' AND NEW.disposition = 'settled')
BEGIN
    SELECT RAISE(ABORT, 'payment_events are append-only');
END;
CREATE TRIGGER payment_events_no_delete
BEFORE DELETE ON payment_events BEGIN
    SELECT RAISE(ABORT, 'payment_events are append-only');
END;
CREATE TRIGGER payment_anomalies_no_update
BEFORE UPDATE ON payment_anomalies BEGIN
    SELECT RAISE(ABORT, 'payment_anomalies are append-only');
END;
CREATE TRIGGER payment_anomalies_no_delete
BEFORE DELETE ON payment_anomalies BEGIN
    SELECT RAISE(ABORT, 'payment_anomalies are append-only');
END;
CREATE TRIGGER payment_resolutions_no_update
BEFORE UPDATE ON payment_resolutions BEGIN
    SELECT RAISE(ABORT, 'payment_resolutions are append-only');
END;
CREATE TRIGGER payment_resolutions_no_delete
BEFORE DELETE ON payment_resolutions BEGIN
    SELECT RAISE(ABORT, 'payment_resolutions are append-only');
END;
CREATE TRIGGER payment_ingress_audits_no_update
BEFORE UPDATE ON payment_ingress_audits BEGIN
    SELECT RAISE(ABORT, 'payment_ingress_audits are append-only');
END;
CREATE TRIGGER payment_ingress_audits_no_delete
BEFORE DELETE ON payment_ingress_audits BEGIN
    SELECT RAISE(ABORT, 'payment_ingress_audits are append-only');
END;
CREATE TRIGGER order_events_no_update
BEFORE UPDATE ON order_events BEGIN
    SELECT RAISE(ABORT, 'order_events are append-only');
END;
CREATE TRIGGER order_events_no_delete
BEFORE DELETE ON order_events BEGIN
    SELECT RAISE(ABORT, 'order_events are append-only');
END;
