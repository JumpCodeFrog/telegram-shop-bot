-- Migration 020: widen the provider CHECK constraints on the six immutable
-- ledger tables to accept 'yookassa' (this plan) and 'stripe' (the approved
-- immediate follow-up; the app layer rejects stripe facts until that provider
-- exists, so the CHECK remains a safety net, not a feature flag).
-- SQLite cannot ALTER a CHECK constraint, so each table is rebuilt:
-- CREATE <name>_new -> explicit-column INSERT SELECT -> DROP -> RENAME, then
-- the 017 indexes and triggers are recreated on the rebuilt tables (018's
-- definitions supersede 017's for the two identity triggers).
--
-- The whole file runs inside the single transaction the migrator opens
-- (applyMigration in db.go). The one foreign key among these tables is
-- payment_events.payment_attempt_id REFERENCES payment_attempts(id) (017:165).
-- A DROP TABLE performs an implicit DELETE FROM: if any FK-bearing rows still
-- reference the dropped parent rows at that moment, SQLite records a deferred
-- foreign key violation, and the COMMIT-time deferred check reports it even
-- though the rebuilt tables (ids preserved by the explicit INSERT SELECT)
-- leave the final state consistent — empirically, re-creating valid child
-- rows does not clear a violation recorded by a parent DROP. PRAGMA
-- foreign_keys itself is a no-op inside a transaction; defer_foreign_keys is
-- the documented in-transaction deferral mechanism and must still be armed.
-- Therefore the child rows are first parked in an FK-less TEMP table and the
-- child table dropped BEFORE the parent is dropped: no referencing rows exist
-- when payment_attempts dies, no violation is ever recorded, and
-- payment_events is then recreated from the backup against the rebuilt
-- parent. The remaining four tables have no foreign keys among the six
-- (refunds references only orders, which is never dropped) and use the plain
-- rebuild shape.
PRAGMA defer_foreign_keys=ON;

-- Park the only child rows referencing payment_attempts outside any foreign
-- key relationship, then remove the child table so the parent DROP below
-- cannot record a deferred violation. TEMP tables cannot declare foreign
-- keys, which is exactly what the parking spot needs.
CREATE TEMP TABLE payment_events_020_backup AS SELECT * FROM payment_events;
DROP TABLE payment_events;

CREATE TABLE payment_attempts_new (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    order_id    INTEGER NOT NULL REFERENCES orders(id),
    provider    TEXT NOT NULL CHECK (provider IN ('stars', 'crypto', 'yookassa', 'stripe')),
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

INSERT INTO payment_attempts_new
    (id, order_id, provider, external_id, payer_id, amount_minor, currency, scale,
     status, entitlement_expires_at, occurred_at, created_at)
SELECT id, order_id, provider, external_id, payer_id, amount_minor, currency, scale,
       status, entitlement_expires_at, occurred_at, created_at
FROM payment_attempts;

DROP TABLE payment_attempts;
ALTER TABLE payment_attempts_new RENAME TO payment_attempts;

CREATE TABLE payment_events_new (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    order_id           INTEGER NOT NULL REFERENCES orders(id),
    payment_attempt_id INTEGER REFERENCES payment_attempts(id),
    provider           TEXT NOT NULL CHECK (provider IN ('stars', 'crypto', 'yookassa', 'stripe')),
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

INSERT INTO payment_events_new
    (id, order_id, payment_attempt_id, provider, event_kind, external_id, amount_minor,
     currency, scale, disposition, occurred_at, created_at)
SELECT id, order_id, payment_attempt_id, provider, event_kind, external_id, amount_minor,
       currency, scale, disposition, occurred_at, created_at
FROM payment_events_020_backup;

DROP TABLE payment_events_020_backup;
ALTER TABLE payment_events_new RENAME TO payment_events;

CREATE TABLE payment_anomalies_new (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    fingerprint       TEXT NOT NULL,
    proposed_order_id INTEGER NOT NULL DEFAULT 0,
    provider          TEXT NOT NULL CHECK (provider IN ('stars', 'crypto', 'yookassa', 'stripe')),
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

INSERT INTO payment_anomalies_new
    (id, fingerprint, proposed_order_id, provider, event_kind, external_id,
     related_external_id, payer_id, amount_minor, currency, scale, raw_amount, raw_payload,
     reason, occurred_at)
SELECT id, fingerprint, proposed_order_id, provider, event_kind, external_id,
       related_external_id, payer_id, amount_minor, currency, scale, raw_amount, raw_payload,
       reason, occurred_at
FROM payment_anomalies;

DROP TABLE payment_anomalies;
ALTER TABLE payment_anomalies_new RENAME TO payment_anomalies;

CREATE TABLE refunds_new (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    order_id            INTEGER NOT NULL REFERENCES orders(id),
    provider            TEXT NOT NULL CHECK (provider IN ('stars', 'crypto', 'yookassa', 'stripe')),
    external_id         TEXT NOT NULL,
    payment_external_id TEXT NOT NULL,
    payer_id            INTEGER NOT NULL DEFAULT 0,
    amount_minor        INTEGER NOT NULL CHECK (amount_minor > 0),
    currency            TEXT NOT NULL,
    scale               INTEGER NOT NULL CHECK (scale BETWEEN 0 AND 9),
    status              TEXT NOT NULL CHECK (status IN ('requested', 'succeeded', 'needs_reconcile', 'failed')),
    requested_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at        DATETIME,
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(provider, external_id)
);

INSERT INTO refunds_new
    (id, order_id, provider, external_id, payment_external_id, payer_id, amount_minor,
     currency, scale, status, requested_at, completed_at, created_at)
SELECT id, order_id, provider, external_id, payment_external_id, payer_id, amount_minor,
       currency, scale, status, requested_at, completed_at, created_at
FROM refunds;

DROP TABLE refunds;
ALTER TABLE refunds_new RENAME TO refunds;

CREATE TABLE payment_resolutions_new (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    order_id                INTEGER NOT NULL DEFAULT 0,
    provider                TEXT NOT NULL CHECK (provider IN ('stars', 'crypto', 'yookassa', 'stripe', 'unknown')),
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

INSERT INTO payment_resolutions_new
    (id, order_id, provider, target_kind, target_id, decision, actor, reason,
     resulting_payment_state, resolved_at)
SELECT id, order_id, provider, target_kind, target_id, decision, actor, reason,
       resulting_payment_state, resolved_at
FROM payment_resolutions;

DROP TABLE payment_resolutions;
ALTER TABLE payment_resolutions_new RENAME TO payment_resolutions;

CREATE TABLE payment_ingress_audits_new (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    order_id    INTEGER NOT NULL DEFAULT 0,
    provider    TEXT NOT NULL CHECK (provider IN ('stars', 'crypto', 'yookassa', 'stripe')),
    event_kind  TEXT NOT NULL CHECK (event_kind IN ('captured', 'refunded')),
    target_kind TEXT NOT NULL CHECK (target_kind IN ('payment_event', 'refund', 'payment_anomaly')),
    target_id   INTEGER NOT NULL CHECK (target_id > 0),
    actor       TEXT NOT NULL CHECK (length(actor) BETWEEN 1 AND 128),
    reason      TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 512),
    applied_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(target_kind, target_id, actor, reason)
);

INSERT INTO payment_ingress_audits_new
    (id, order_id, provider, event_kind, target_kind, target_id, actor, reason, applied_at)
SELECT id, order_id, provider, event_kind, target_kind, target_id, actor, reason, applied_at
FROM payment_ingress_audits;

DROP TABLE payment_ingress_audits;
ALTER TABLE payment_ingress_audits_new RENAME TO payment_ingress_audits;

-- DROP TABLE silently removed the provider-table indexes; recreate them from
-- 017. idx_order_events_order_time was not dropped and is not touched.
CREATE INDEX idx_payment_attempts_order ON payment_attempts(order_id, created_at, id);
CREATE INDEX idx_payment_events_order_time ON payment_events(order_id, occurred_at, id);
CREATE INDEX idx_payment_anomalies_provider_time ON payment_anomalies(provider, occurred_at, id);
CREATE INDEX idx_refunds_order ON refunds(order_id, created_at, id);
CREATE INDEX idx_refunds_payment_identity ON refunds(provider, payment_external_id);
CREATE INDEX idx_payment_resolutions_order ON payment_resolutions(order_id, provider, resolved_at, id);
CREATE INDEX idx_payment_ingress_audits_order ON payment_ingress_audits(order_id, provider, applied_at, id);

-- DROP TABLE also silently removed every trigger defined on the old tables.
-- Recreate them with the current definitions: 018's versions for the two
-- identity triggers (they superseded 017's), 017's versions for the rest.
-- order_events and its triggers were never dropped and stay untouched.
DROP TRIGGER IF EXISTS payment_attempts_identity_no_update;
CREATE TRIGGER payment_attempts_identity_no_update
BEFORE UPDATE OF order_id, provider, external_id, payer_id, amount_minor, currency, scale, occurred_at
ON payment_attempts BEGIN
    SELECT RAISE(ABORT, 'payment_attempt identity is immutable');
END;
DROP TRIGGER IF EXISTS payment_attempts_entitlement_once;
CREATE TRIGGER payment_attempts_entitlement_once
BEFORE UPDATE OF entitlement_expires_at ON payment_attempts
WHEN NOT (OLD.entitlement_expires_at IS NULL AND NEW.entitlement_expires_at IS NOT NULL)
BEGIN
    SELECT RAISE(ABORT, 'payment_attempt entitlement expiry is immutable');
END;
DROP TRIGGER IF EXISTS payment_attempts_no_delete;
CREATE TRIGGER payment_attempts_no_delete
BEFORE DELETE ON payment_attempts BEGIN
    SELECT RAISE(ABORT, 'payment_attempts cannot be deleted');
END;
DROP TRIGGER IF EXISTS refunds_identity_no_update;
CREATE TRIGGER refunds_identity_no_update
BEFORE UPDATE OF order_id, provider, external_id, payment_external_id, payer_id,
                 amount_minor, currency, scale, completed_at
ON refunds BEGIN
    SELECT RAISE(ABORT, 'refund identity is immutable');
END;
DROP TRIGGER IF EXISTS refunds_no_delete;
CREATE TRIGGER refunds_no_delete
BEFORE DELETE ON refunds BEGIN
    SELECT RAISE(ABORT, 'refunds cannot be deleted');
END;
DROP TRIGGER IF EXISTS payment_events_no_update;
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
DROP TRIGGER IF EXISTS payment_events_no_delete;
CREATE TRIGGER payment_events_no_delete
BEFORE DELETE ON payment_events BEGIN
    SELECT RAISE(ABORT, 'payment_events are append-only');
END;
DROP TRIGGER IF EXISTS payment_anomalies_no_update;
CREATE TRIGGER payment_anomalies_no_update
BEFORE UPDATE ON payment_anomalies BEGIN
    SELECT RAISE(ABORT, 'payment_anomalies are append-only');
END;
DROP TRIGGER IF EXISTS payment_anomalies_no_delete;
CREATE TRIGGER payment_anomalies_no_delete
BEFORE DELETE ON payment_anomalies BEGIN
    SELECT RAISE(ABORT, 'payment_anomalies are append-only');
END;
DROP TRIGGER IF EXISTS payment_resolutions_no_update;
CREATE TRIGGER payment_resolutions_no_update
BEFORE UPDATE ON payment_resolutions BEGIN
    SELECT RAISE(ABORT, 'payment_resolutions are append-only');
END;
DROP TRIGGER IF EXISTS payment_resolutions_no_delete;
CREATE TRIGGER payment_resolutions_no_delete
BEFORE DELETE ON payment_resolutions BEGIN
    SELECT RAISE(ABORT, 'payment_resolutions are append-only');
END;
DROP TRIGGER IF EXISTS payment_ingress_audits_no_update;
CREATE TRIGGER payment_ingress_audits_no_update
BEFORE UPDATE ON payment_ingress_audits BEGIN
    SELECT RAISE(ABORT, 'payment_ingress_audits are append-only');
END;
DROP TRIGGER IF EXISTS payment_ingress_audits_no_delete;
CREATE TRIGGER payment_ingress_audits_no_delete
BEFORE DELETE ON payment_ingress_audits BEGIN
    SELECT RAISE(ABORT, 'payment_ingress_audits are append-only');
END;
