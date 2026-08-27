package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type SQLPaymentLedgerStore struct {
	db *sql.DB
}

func NewSQLPaymentLedgerStore(db *DB) *SQLPaymentLedgerStore {
	return &SQLPaymentLedgerStore{db: db.Conn()}
}

func (s *SQLPaymentLedgerStore) ListOrderEvents(ctx context.Context, orderID int64) ([]OrderEvent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, order_id, event_type, from_state, to_state, metadata, occurred_at
		 FROM order_events WHERE order_id = ? ORDER BY id`, orderID)
	if err != nil {
		return nil, fmt.Errorf("ledger: list order events: %w", err)
	}
	defer rows.Close()
	var result []OrderEvent
	for rows.Next() {
		var event OrderEvent
		if err := rows.Scan(&event.ID, &event.OrderID, &event.EventType,
			&event.FromState, &event.ToState, &event.Metadata, &event.OccurredAt); err != nil {
			return nil, fmt.Errorf("ledger: scan order event: %w", err)
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

func (s *SQLPaymentLedgerStore) ListPaymentAttempts(ctx context.Context, orderID int64) ([]PaymentAttempt, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, order_id, provider, external_id, payer_id, amount_minor, currency, scale,
		        status, entitlement_expires_at, occurred_at, created_at
		 FROM payment_attempts WHERE order_id = ? ORDER BY id`, orderID)
	if err != nil {
		return nil, fmt.Errorf("ledger: list payment attempts: %w", err)
	}
	defer rows.Close()
	var result []PaymentAttempt
	for rows.Next() {
		var attempt PaymentAttempt
		if err := rows.Scan(&attempt.ID, &attempt.OrderID, &attempt.Provider,
			&attempt.ExternalID, &attempt.PayerID, &attempt.AmountMinor, &attempt.Currency, &attempt.Scale,
			&attempt.Status, &attempt.EntitlementExpiresAt, &attempt.OccurredAt, &attempt.CreatedAt); err != nil {
			return nil, fmt.Errorf("ledger: scan payment attempt: %w", err)
		}
		result = append(result, attempt)
	}
	return result, rows.Err()
}

func (s *SQLPaymentLedgerStore) ListPaymentEvents(ctx context.Context, disposition string) ([]PaymentEvent, error) {
	query := `SELECT id, order_id, payment_attempt_id, provider, event_kind, external_id,
	                 amount_minor, currency, scale, disposition, occurred_at, created_at
	          FROM payment_events`
	var args []any
	if disposition != "" {
		query += ` WHERE disposition = ?`
		args = append(args, disposition)
	}
	query += ` ORDER BY id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("ledger: list payment events: %w", err)
	}
	defer rows.Close()
	var result []PaymentEvent
	for rows.Next() {
		var event PaymentEvent
		if err := rows.Scan(&event.ID, &event.OrderID, &event.PaymentAttemptID,
			&event.Provider, &event.EventKind, &event.ExternalID, &event.AmountMinor,
			&event.Currency, &event.Scale, &event.Disposition,
			&event.OccurredAt, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("ledger: scan payment event: %w", err)
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

func (s *SQLPaymentLedgerStore) ListRefunds(ctx context.Context, orderID int64) ([]Refund, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, order_id, provider, external_id, payment_external_id, payer_id,
		        amount_minor, currency, scale, status, requested_at, completed_at, created_at
		 FROM refunds WHERE order_id = ? ORDER BY id`, orderID)
	if err != nil {
		return nil, fmt.Errorf("ledger: list refunds: %w", err)
	}
	defer rows.Close()
	var result []Refund
	for rows.Next() {
		var refund Refund
		if err := rows.Scan(&refund.ID, &refund.OrderID, &refund.Provider,
			&refund.ExternalID, &refund.PaymentExternalID, &refund.PayerID, &refund.AmountMinor,
			&refund.Currency, &refund.Scale, &refund.Status, &refund.RequestedAt,
			&refund.CompletedAt, &refund.CreatedAt); err != nil {
			return nil, fmt.Errorf("ledger: scan refund: %w", err)
		}
		if refund.CompletedAt.Valid {
			refund.OccurredAt = refund.CompletedAt.Time
		}
		result = append(result, refund)
	}
	return result, rows.Err()
}

// RecordRefund applies a provider-confirmed refund idempotently. It updates
// only the payment projection; restock/reward compensation is a separate,
// explicit policy and is intentionally not inferred here.
func (s *SQLPaymentLedgerStore) RecordRefund(ctx context.Context, refund Refund) error {
	return s.recordRefund(ctx, refund, nil)
}

// IngestProviderRefund attributes an explicit operator write to the exact
// refund or anomaly row created by the same transaction.
func (s *SQLPaymentLedgerStore) IngestProviderRefund(ctx context.Context, refund Refund, audit PaymentIngressAudit) error {
	if err := validatePaymentIngressAudit(audit); err != nil {
		return err
	}
	provider := normalizePaymentProvider(refund.Provider)
	if refund.PayerID <= 0 || refund.OccurredAt.IsZero() ||
		(provider == PaymentMethodStars && refund.ExternalID != refund.PaymentExternalID) {
		return ErrPaymentReceiptMismatch
	}
	var orderPayerID int64
	if err := s.db.QueryRowContext(ctx, `SELECT user_id FROM orders WHERE id = ?`, refund.OrderID).Scan(&orderPayerID); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("ledger: load refund payer: %w", err)
	}
	if orderPayerID != refund.PayerID {
		return ErrPaymentReceiptMismatch
	}
	return s.recordRefund(ctx, refund, &audit)
}

func (s *SQLPaymentLedgerStore) recordRefund(ctx context.Context, refund Refund, audit *PaymentIngressAudit) error {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		lastErr = s.recordRefundOnce(ctx, refund, audit)
		if lastErr == nil {
			return nil
		}
		if errors.Is(lastErr, ErrNotFound) || errors.Is(lastErr, ErrPaymentIdentityConflict) ||
			errors.Is(lastErr, ErrRefundExceedsPayment) || errors.Is(lastErr, ErrInvalidMoney) ||
			errors.Is(lastErr, ErrPaymentReceiptMismatch) {
			reason := "refund_parent_not_found"
			switch {
			case errors.Is(lastErr, ErrPaymentIdentityConflict):
				reason = "refund_identity_conflict"
			case errors.Is(lastErr, ErrRefundExceedsPayment):
				reason = "refund_exceeds_payment"
			case errors.Is(lastErr, ErrInvalidMoney), errors.Is(lastErr, ErrPaymentReceiptMismatch):
				reason = "refund_invalid_provider_fact"
			}
			recordErr := s.recordRefundAnomaly(ctx, refund, reason, audit)
			if recordErr != nil && !errors.Is(recordErr, ErrPaymentNeedsReview) {
				return errors.Join(lastErr, recordErr)
			}
			return lastErr
		}
		if !strings.Contains(lastErr.Error(), "database is locked") {
			return lastErr
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func (s *SQLPaymentLedgerStore) recordRefundAnomaly(ctx context.Context, refund Refund, reason string, audit *PaymentIngressAudit) error {
	provider := normalizePaymentProvider(refund.Provider)
	rawPayload := ""
	if provider != PaymentMethodStars && provider != PaymentMethodCrypto && refund.OrderID > 0 {
		var orderProvider string
		if err := s.db.QueryRowContext(ctx, `SELECT payment_method FROM orders WHERE id = ?`, refund.OrderID).Scan(&orderProvider); err == nil {
			provider = normalizePaymentProvider(orderProvider)
			rawPayload = "invalid_provider:" + strings.TrimSpace(refund.Provider)
		}
	}
	amount := refund.AmountMinor
	scale := refund.Scale
	rawAmount := ""
	if amount <= 0 || scale < 0 || scale > 9 {
		rawAmount = strconv.FormatInt(refund.AmountMinor, 10) + "@" + strconv.Itoa(refund.Scale)
	}
	if amount < 0 {
		amount = 0
	}
	if scale < 0 || scale > 9 {
		scale = 0
	}
	externalID := strings.TrimSpace(refund.ExternalID)
	if externalID == "" {
		rawPayload += ";missing_refund_external_id"
	}
	if strings.TrimSpace(refund.PaymentExternalID) == "" {
		rawPayload += ";missing_payment_external_id"
	}
	return (&SQLOrderStore{db: s.db}).recordPaymentAnomaly(ctx, PaymentAnomaly{
		ProposedOrderID:   refund.OrderID,
		Provider:          provider,
		EventKind:         PaymentEventRefunded,
		ExternalID:        externalID,
		RelatedExternalID: refund.PaymentExternalID,
		PayerID:           refund.PayerID,
		AmountMinor:       amount,
		Currency:          refund.Currency,
		Scale:             scale,
		RawAmount:         rawAmount,
		RawPayload:        strings.TrimPrefix(rawPayload, ";"),
		Reason:            reason,
		OccurredAt:        refund.OccurredAt,
	}, audit)
}

func (s *SQLPaymentLedgerStore) recordRefundOnce(ctx context.Context, refund Refund, audit *PaymentIngressAudit) error {
	if refund.AmountMinor <= 0 || refund.ExternalID == "" || refund.PaymentExternalID == "" {
		return ErrInvalidMoney
	}
	provider := normalizePaymentProvider(refund.Provider)
	if (provider != PaymentMethodStars && provider != PaymentMethodCrypto) ||
		refund.Scale < 0 || refund.Scale > 9 || strings.TrimSpace(refund.Currency) == "" {
		return ErrPaymentReceiptMismatch
	}
	var occurredAt any
	if !refund.OccurredAt.IsZero() {
		occurredAt = refund.OccurredAt.UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("ledger: begin refund: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var captured, existingOrder, capturePayerID int64
	var currency, captureStatus string
	var scale int
	var captureEntitlement sql.NullTime
	err = tx.QueryRowContext(ctx,
		`SELECT amount_minor, order_id, payer_id, currency, scale, status, entitlement_expires_at FROM payment_attempts
		 WHERE provider = ? AND external_id = ? AND status IN ('succeeded', 'needs_review')`,
		provider, refund.PaymentExternalID).Scan(&captured, &existingOrder, &capturePayerID, &currency, &scale, &captureStatus, &captureEntitlement)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("ledger: load captured payment: %w", err)
	}
	if existingOrder != refund.OrderID || currency != refund.Currency || scale != refund.Scale ||
		(refund.PayerID > 0 && capturePayerID > 0 && refund.PayerID != capturePayerID) {
		return ErrPaymentIdentityConflict
	}
	var existing Refund
	var existingOccurredAt time.Time
	err = tx.QueryRowContext(ctx,
		`SELECT order_id, payment_external_id, payer_id, amount_minor, currency, scale, status, completed_at
		 FROM refunds WHERE provider = ? AND external_id = ?`,
		provider, refund.ExternalID).Scan(&existing.OrderID, &existing.PaymentExternalID,
		&existing.PayerID, &existing.AmountMinor, &existing.Currency, &existing.Scale, &existing.Status, &existingOccurredAt)
	if err == nil {
		if existing.OrderID != refund.OrderID || existing.PaymentExternalID != refund.PaymentExternalID ||
			existing.AmountMinor != refund.AmountMinor || existing.Currency != refund.Currency ||
			existing.Scale != refund.Scale || existing.Status != "succeeded" ||
			(refund.PayerID > 0 && existing.PayerID > 0 && refund.PayerID != existing.PayerID) ||
			(!refund.OccurredAt.IsZero() && !existingOccurredAt.Equal(refund.OccurredAt.UTC())) {
			return ErrPaymentIdentityConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("ledger: load refund identity: %w", err)
	}
	var refunded int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount_minor), 0) FROM refunds
		 WHERE provider = ? AND payment_external_id = ? AND status = 'succeeded'`,
		provider, refund.PaymentExternalID).Scan(&refunded); err != nil {
		return fmt.Errorf("ledger: sum refunds: %w", err)
	}
	if refunded+refund.AmountMinor > captured {
		return ErrRefundExceedsPayment
	}
	res, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO refunds
		 (order_id, provider, external_id, payment_external_id, payer_id, amount_minor,
		  currency, scale, status, completed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'succeeded', COALESCE(NULLIF(?, ''), CURRENT_TIMESTAMP))`,
		refund.OrderID, provider, refund.ExternalID, refund.PaymentExternalID,
		refund.PayerID, refund.AmountMinor, refund.Currency, refund.Scale, occurredAt)
	if err != nil {
		return fmt.Errorf("ledger: insert refund: %w", err)
	}
	inserted, _ := res.RowsAffected()
	if inserted == 0 {
		return ErrPaymentIdentityConflict
	}
	refundID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("ledger: inserted refund id: %w", err)
	}
	entitlementNeedsReview, err := reconcileRefundedSubscriptionEntitlement(
		ctx, tx, refund.OrderID, provider, captureEntitlement, refunded+refund.AmountMinor == captured,
	)
	if err != nil {
		return err
	}
	disposition := PaymentDispositionSettled
	if captureStatus == PaymentStateNeedsReview || entitlementNeedsReview {
		disposition = PaymentDispositionNeedsReview
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO payment_events
		 (order_id, provider, event_kind, external_id, amount_minor, currency, scale, disposition, occurred_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, COALESCE(NULLIF(?, ''), CURRENT_TIMESTAMP))`,
		refund.OrderID, provider, PaymentEventRefunded, refund.ExternalID,
		refund.AmountMinor, refund.Currency, refund.Scale, disposition, occurredAt); err != nil {
		return fmt.Errorf("ledger: append refund event: %w", err)
	}
	var orderCaptured, orderRefunded int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount_minor), 0) FROM payment_attempts
		 WHERE order_id = ? AND provider = ? AND status IN ('succeeded', 'needs_review')`,
		refund.OrderID, provider).Scan(&orderCaptured); err != nil {
		return fmt.Errorf("ledger: sum order captures: %w", err)
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount_minor), 0) FROM refunds
		 WHERE order_id = ? AND provider = ? AND status = 'succeeded'`,
		refund.OrderID, provider).Scan(&orderRefunded); err != nil {
		return fmt.Errorf("ledger: sum order refunds: %w", err)
	}
	state := PaymentStatePartiallyRefunded
	if orderRefunded == orderCaptured {
		state = PaymentStateRefunded
	}
	var previousState string
	if err := tx.QueryRowContext(ctx, `SELECT payment_state FROM orders WHERE id = ?`, refund.OrderID).Scan(&previousState); err != nil {
		return fmt.Errorf("ledger: load refund projection: %w", err)
	}
	// A refund is another provider fact, not a resolution decision. Keep the
	// order quarantined until an operator explicitly reconciles every unresolved
	// capture/identity conflict.
	if previousState == PaymentStateNeedsReview || entitlementNeedsReview {
		state = PaymentStateNeedsReview
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE orders SET payment_state = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		state, refund.OrderID); err != nil {
		return fmt.Errorf("ledger: update refund state: %w", err)
	}
	if err := appendOrderEvent(ctx, tx, refund.OrderID, "payment.refunded", previousState, state); err != nil {
		return err
	}
	if audit != nil {
		if err := appendPaymentIngressAudit(ctx, tx, refund.OrderID, provider, PaymentEventRefunded,
			PaymentIngressTargetRefund, refundID, *audit); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ledger: commit refund: %w", err)
	}
	if entitlementNeedsReview {
		return ErrPaymentNeedsReview
	}
	return nil
}

// reconcileRefundedSubscriptionEntitlement removes access granted by a fully
// refunded capture in the same transaction as the refund fact. When an older,
// still-paid period exists, the subscription rolls back to that immutable
// expiry. Missing entitlement provenance fails closed: access is suspended and
// the order remains in needs_review instead of silently retaining paid access.
func reconcileRefundedSubscriptionEntitlement(ctx context.Context, tx *sql.Tx, orderID int64, provider string, captureEntitlement sql.NullTime, captureFullyRefunded bool) (bool, error) {
	if !captureFullyRefunded {
		return false, nil
	}
	var userID int64
	var productID sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT user_id, subscription_product_id FROM orders WHERE id = ?`, orderID).
		Scan(&userID, &productID); err != nil {
		return false, fmt.Errorf("ledger: load refunded subscription order: %w", err)
	}
	if !productID.Valid || productID.Int64 <= 0 {
		return false, nil
	}

	var fallbackExpiry time.Time
	err := tx.QueryRowContext(ctx, `
		SELECT a.entitlement_expires_at
		FROM payment_attempts a
		WHERE a.order_id = ? AND a.provider = ? AND a.status = 'succeeded'
		  AND a.entitlement_expires_at IS NOT NULL
		  AND COALESCE((
		      SELECT SUM(r.amount_minor) FROM refunds r
		      WHERE r.provider = a.provider AND r.payment_external_id = a.external_id
		        AND r.status = 'succeeded'
		  ), 0) < a.amount_minor
		ORDER BY a.entitlement_expires_at DESC, a.id DESC
		LIMIT 1`, orderID, provider).Scan(&fallbackExpiry)
	switch {
	case err == nil:
		fallbackExpiry = fallbackExpiry.UTC()
		status := SubStatusExpired
		if fallbackExpiry.After(time.Now().UTC()) {
			status = SubStatusActive
		}
		res, updateErr := tx.ExecContext(ctx, `
			UPDATE subscriptions
			SET expires_at = ?,
			    status = CASE WHEN status = 'canceled' THEN status ELSE ? END,
			    reminded_at = NULL,
			    updated_at = CURRENT_TIMESTAMP
			WHERE user_id = ? AND product_id = ?`,
			fallbackExpiry, status, userID, productID.Int64)
		if updateErr != nil {
			return false, fmt.Errorf("ledger: roll back refunded subscription entitlement: %w", updateErr)
		}
		updated, updateErr := res.RowsAffected()
		if updateErr != nil {
			return false, fmt.Errorf("ledger: refunded subscription rows affected: %w", updateErr)
		}
		return updated != 1, nil
	case !errors.Is(err, sql.ErrNoRows):
		return false, fmt.Errorf("ledger: load prior subscription entitlement: %w", err)
	}

	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `
		UPDATE subscriptions
		SET expires_at = MIN(expires_at, ?),
		    status = CASE WHEN status = 'canceled' THEN status ELSE 'expired' END,
		    reminded_at = NULL,
		    updated_at = CURRENT_TIMESTAMP
		WHERE user_id = ? AND product_id = ?`, now, userID, productID.Int64)
	if err != nil {
		return false, fmt.Errorf("ledger: revoke refunded subscription entitlement: %w", err)
	}
	updated, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("ledger: refunded subscription rows affected: %w", err)
	}
	// A fully refunded capture without a recorded entitlement expiry lacks the
	// provenance needed to prove whether some other period should survive.
	return !captureEntitlement.Valid || updated != 1, nil
}
