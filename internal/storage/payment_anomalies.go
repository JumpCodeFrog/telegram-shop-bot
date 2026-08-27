package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RecordPaymentAnomaly durably quarantines a normalized signed provider fact.
// It is idempotent for an exact retry and marks a known order needs_review in
// the same transaction. Orphan facts deliberately survive without an order FK.
func (s *SQLOrderStore) RecordPaymentAnomaly(ctx context.Context, anomaly PaymentAnomaly) error {
	return s.recordPaymentAnomaly(ctx, anomaly, nil)
}

func (s *SQLOrderStore) recordPaymentAnomaly(ctx context.Context, anomaly PaymentAnomaly, audit *PaymentIngressAudit) error {
	if audit != nil {
		if err := validatePaymentIngressAudit(*audit); err != nil {
			return err
		}
	}
	anomaly.Provider = normalizePaymentProvider(anomaly.Provider)
	if anomaly.EventKind == "" {
		anomaly.EventKind = PaymentEventCaptured
	}
	if (anomaly.Provider != PaymentMethodStars && anomaly.Provider != PaymentMethodCrypto) ||
		(anomaly.EventKind != PaymentEventCaptured && anomaly.EventKind != PaymentEventRefunded) ||
		anomaly.AmountMinor < 0 || anomaly.Scale < 0 || anomaly.Scale > 9 ||
		(anomaly.AmountMinor == 0 && anomaly.RawAmount == "" && anomaly.RawPayload == "") ||
		(strings.TrimSpace(anomaly.ExternalID) == "" && anomaly.RawPayload == "") ||
		strings.TrimSpace(anomaly.Reason) == "" {
		return ErrPaymentReceiptMismatch
	}
	canonicalFact := struct {
		Provider          string `json:"provider"`
		EventKind         string `json:"event_kind"`
		ExternalID        string `json:"external_id"`
		RelatedExternalID string `json:"related_external_id"`
		PayerID           int64  `json:"payer_id"`
		AmountMinor       int64  `json:"amount_minor"`
		Currency          string `json:"currency"`
		Scale             int    `json:"scale"`
		RawPayload        string `json:"raw_payload"`
		OccurredAt        string `json:"occurred_at"`
	}{anomaly.Provider, anomaly.EventKind, anomaly.ExternalID, anomaly.RelatedExternalID, anomaly.PayerID,
		anomaly.AmountMinor, anomaly.Currency, anomaly.Scale,
		anomaly.RawPayload, anomalyOccurredAtFingerprint(anomaly.OccurredAt)}
	canonical, marshalErr := json.Marshal(canonicalFact)
	if marshalErr != nil {
		return fmt.Errorf("payment anomaly: fingerprint: %w", marshalErr)
	}
	digest := sha256.Sum256(canonical)
	anomaly.Fingerprint = hex.EncodeToString(digest[:])
	if anomaly.OccurredAt.IsZero() {
		// Earlier builds included the local CURRENT_TIMESTAMP fallback in the
		// retry identity. Reuse such a row when every authoritative provider
		// field matches, then converge all future retries on its durable key.
		var legacyFingerprint string
		err := s.db.QueryRowContext(ctx, `
				SELECT fingerprint FROM payment_anomalies
			WHERE provider = ? AND event_kind = ? AND external_id = ?
			  AND related_external_id = ? AND payer_id = ? AND amount_minor = ?
			  AND currency = ? AND scale = ? AND raw_payload = ?
			ORDER BY id LIMIT 1`,
			anomaly.Provider, anomaly.EventKind, anomaly.ExternalID, anomaly.RelatedExternalID,
			anomaly.PayerID, anomaly.AmountMinor, anomaly.Currency, anomaly.Scale,
			anomaly.RawPayload).Scan(&legacyFingerprint)
		if err == nil {
			anomaly.Fingerprint = legacyFingerprint
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("payment anomaly: load canonical retry: %w", err)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("payment anomaly: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var occurredAt any
	if !anomaly.OccurredAt.IsZero() {
		occurredAt = anomaly.OccurredAt.UTC()
	}
	result, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO payment_anomalies
			(fingerprint, proposed_order_id, provider, event_kind, external_id, related_external_id, payer_id,
			 amount_minor, currency, scale, raw_amount, raw_payload, reason, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, COALESCE(?, CURRENT_TIMESTAMP))`,
		anomaly.Fingerprint, anomaly.ProposedOrderID, anomaly.Provider, anomaly.EventKind,
		anomaly.ExternalID, anomaly.RelatedExternalID, anomaly.PayerID,
		anomaly.AmountMinor, anomaly.Currency, anomaly.Scale, anomaly.RawAmount, anomaly.RawPayload, anomaly.Reason, occurredAt)
	if err != nil {
		return fmt.Errorf("payment anomaly: insert: %w", err)
	}
	inserted, err := rowsInserted(result)
	if err != nil {
		return fmt.Errorf("payment anomaly: rows affected: %w", err)
	}
	var anomalyID int64
	if inserted {
		anomalyID, err = result.LastInsertId()
		if err != nil {
			return fmt.Errorf("payment anomaly: inserted id: %w", err)
		}
	} else {
		var existing PaymentAnomaly
		if err := tx.QueryRowContext(ctx, `
			SELECT id, proposed_order_id, provider, event_kind, external_id,
			       related_external_id, payer_id, amount_minor, currency, scale,
			       raw_amount, raw_payload, reason, occurred_at
			FROM payment_anomalies WHERE provider = ? AND fingerprint = ?`,
			anomaly.Provider, anomaly.Fingerprint).Scan(
			&existing.ID, &existing.ProposedOrderID, &existing.Provider, &existing.EventKind,
			&existing.ExternalID, &existing.RelatedExternalID, &existing.PayerID,
			&existing.AmountMinor, &existing.Currency, &existing.Scale,
			&existing.RawAmount, &existing.RawPayload, &existing.Reason, &existing.OccurredAt); err != nil {
			return fmt.Errorf("payment anomaly: load retry: %w", err)
		}
		if existing.Provider != anomaly.Provider || existing.EventKind != anomaly.EventKind ||
			existing.ExternalID != anomaly.ExternalID ||
			existing.RelatedExternalID != anomaly.RelatedExternalID || existing.PayerID != anomaly.PayerID ||
			existing.AmountMinor != anomaly.AmountMinor || existing.Currency != anomaly.Currency ||
			existing.Scale != anomaly.Scale || existing.RawPayload != anomaly.RawPayload ||
			(!anomaly.OccurredAt.IsZero() && !existing.OccurredAt.Equal(anomaly.OccurredAt.UTC())) {
			return ErrPaymentIdentityConflict
		}
		var resolved int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM payment_resolutions
			WHERE target_kind = 'payment_anomaly' AND target_id = ?`, existing.ID).Scan(&resolved); err != nil {
			return fmt.Errorf("payment anomaly: load resolution: %w", err)
		}
		if resolved == 1 {
			if audit != nil {
				if err := appendPaymentIngressAudit(ctx, tx, anomaly.ProposedOrderID, anomaly.Provider,
					anomaly.EventKind, PaymentIngressTargetAnomaly, existing.ID, *audit); err != nil {
					return err
				}
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("payment anomaly: commit resolved retry: %w", err)
			}
			return nil
		}
		anomalyID = existing.ID
	}
	if anomaly.ProposedOrderID > 0 {
		var previous string
		err = tx.QueryRowContext(ctx, `SELECT payment_state FROM orders WHERE id = ?`, anomaly.ProposedOrderID).Scan(&previous)
		switch {
		case err == nil:
			if previous != PaymentStateNeedsReview {
				if _, err := tx.ExecContext(ctx, `
					UPDATE orders SET payment_state = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
					PaymentStateNeedsReview, anomaly.ProposedOrderID); err != nil {
					return fmt.Errorf("payment anomaly: quarantine order: %w", err)
				}
				if err := appendOrderEvent(ctx, tx, anomaly.ProposedOrderID,
					"payment.anomaly", previous, PaymentStateNeedsReview); err != nil {
					return err
				}
			}
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("payment anomaly: load proposed order: %w", err)
		}
	}
	if audit != nil {
		if err := appendPaymentIngressAudit(ctx, tx, anomaly.ProposedOrderID, anomaly.Provider,
			anomaly.EventKind, PaymentIngressTargetAnomaly, anomalyID, *audit); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("payment anomaly: commit: %w", err)
	}
	return ErrPaymentNeedsReview
}

func anomalyOccurredAtFingerprint(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
