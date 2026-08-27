package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func appendOrderEvent(ctx context.Context, tx *sql.Tx, orderID int64, eventType, fromState, toState string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO order_events (order_id, event_type, from_state, to_state)
		 VALUES (?, ?, ?, ?)`, orderID, eventType, fromState, toState)
	if err != nil {
		return fmt.Errorf("order store: append order event: %w", err)
	}
	return nil
}

// observePayment records the provider fact before the legacy projection is
// changed. Replays are harmless; cross-order identity collisions are durable.
func observePayment(ctx context.Context, tx *sql.Tx, order Order, provider, externalID string, supplied *PaymentFact, entitlementExpiry *time.Time) (int64, error) {
	provider = normalizePaymentProvider(provider)
	if externalID == "" {
		return 0, fmt.Errorf("order store: empty payment identity: %w", ErrInvalidMoney)
	}
	amount, currency, scale, err := orderMoney(order, provider)
	if err != nil {
		return 0, err
	}
	var payerID int64
	var occurredAt any
	if supplied != nil {
		fact, factErr := validatePaymentFact(order, *supplied)
		if factErr != nil || fact.Provider != provider || fact.ExternalID != externalID {
			if factErr != nil {
				return 0, factErr
			}
			return 0, ErrPaymentReceiptMismatch
		}
		amount, currency, scale, payerID = fact.AmountMinor, fact.Currency, fact.Scale, fact.PayerID
		if !fact.OccurredAt.IsZero() {
			occurredAt = fact.OccurredAt.UTC()
		}
	}
	var expires any
	if entitlementExpiry != nil && !entitlementExpiry.IsZero() {
		expires = entitlementExpiry.UTC()
	}

	var existingID, existingOrderID, existingPayerID, existingAmount int64
	var existingCurrency, existingStatus string
	var existingScale int
	var existingEntitlement sql.NullTime
	var existingOccurredAt time.Time
	err = tx.QueryRowContext(ctx,
		`SELECT id, order_id, payer_id, amount_minor, currency, scale, status,
		        entitlement_expires_at, occurred_at
		 FROM payment_attempts WHERE provider = ? AND external_id = ?`,
		provider, externalID).Scan(&existingID, &existingOrderID, &existingPayerID, &existingAmount,
		&existingCurrency, &existingScale, &existingStatus, &existingEntitlement, &existingOccurredAt)
	switch {
	case err == nil && existingOrderID == order.ID:
		if existingAmount != amount || existingCurrency != currency || existingScale != scale || existingStatus != "succeeded" ||
			(payerID > 0 && existingPayerID != payerID) || !paymentOccurredAtEqual(existingOccurredAt, supplied) {
			return 0, ErrPaymentIdentityConflict
		}
		if entitlementExpiry != nil && !entitlementExpiry.IsZero() {
			switch {
			case existingEntitlement.Valid && !existingEntitlement.Time.Equal(entitlementExpiry.UTC()):
				return 0, ErrPaymentIdentityConflict
			case !existingEntitlement.Valid:
				if _, err := tx.ExecContext(ctx,
					`UPDATE payment_attempts SET entitlement_expires_at = ?
					 WHERE id = ? AND entitlement_expires_at IS NULL`, entitlementExpiry.UTC(), existingID); err != nil {
					return 0, fmt.Errorf("order store: persist payment entitlement expiry: %w", err)
				}
			}
		}
		return existingID, ErrOrderStatusConflict
	case err == nil:
		return 0, ErrPaymentIdentityConflict
	case !errors.Is(err, sql.ErrNoRows):
		return 0, fmt.Errorf("order store: lookup payment identity: %w", err)
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO payment_attempts
		 (order_id, provider, external_id, payer_id, amount_minor, currency, scale, status, entitlement_expires_at, occurred_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 'observed', ?, COALESCE(?, CURRENT_TIMESTAMP))`,
		order.ID, provider, externalID, payerID, amount, currency, scale, expires, occurredAt)
	if err != nil {
		return 0, fmt.Errorf("order store: observe payment attempt: %w", err)
	}
	attemptID, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("order store: payment attempt id: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO payment_events
		 (order_id, payment_attempt_id, provider, event_kind, external_id,
		  amount_minor, currency, scale, disposition, occurred_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'observed', COALESCE(?, CURRENT_TIMESTAMP))`,
		order.ID, attemptID, provider, PaymentEventCaptured, externalID,
		amount, currency, scale, occurredAt); err != nil {
		return 0, fmt.Errorf("order store: observe payment event: %w", err)
	}
	return attemptID, nil
}

func paymentOccurredAtEqual(existing time.Time, supplied *PaymentFact) bool {
	return supplied == nil || supplied.OccurredAt.IsZero() || existing.Equal(supplied.OccurredAt.UTC())
}

func markPaymentSettled(ctx context.Context, tx *sql.Tx, attemptID int64) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE payment_attempts SET status = 'succeeded' WHERE id = ?`, attemptID); err != nil {
		return fmt.Errorf("order store: settle payment attempt: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE payment_events SET disposition = 'settled'
		 WHERE payment_attempt_id = ? AND event_kind = ?`, attemptID, PaymentEventCaptured); err != nil {
		return fmt.Errorf("order store: settle payment event: %w", err)
	}
	return nil
}
