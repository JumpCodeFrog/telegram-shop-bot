package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	PaymentIngressReplay     = "replay"
	PaymentIngressQuarantine = "quarantine"
	PaymentIngressApply      = "apply"
)

func (s *SQLOrderStore) PreviewProviderCaptureIngress(ctx context.Context, orderID int64, fact PaymentFact) (string, error) {
	order, err := s.loadPaymentOrder(ctx, orderID)
	if err != nil {
		return "", err
	}
	if fact.PayerID <= 0 || fact.PayerID != order.UserID || fact.OccurredAt.IsZero() {
		return "", ErrPaymentReceiptMismatch
	}
	fact, err = validatePaymentFact(*order, fact)
	if err != nil {
		return "", err
	}
	var existingOrder, payerID, amount int64
	var currency, status string
	var scale int
	var occurredAt time.Time
	err = s.db.QueryRowContext(ctx, `
		SELECT order_id, payer_id, amount_minor, currency, scale, status, occurred_at
		FROM payment_attempts WHERE provider = ? AND external_id = ?`,
		fact.Provider, fact.ExternalID).Scan(&existingOrder, &payerID, &amount, &currency, &scale, &status, &occurredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PaymentIngressQuarantine, nil
	}
	if err != nil {
		return "", fmt.Errorf("order store: preview provider capture: %w", err)
	}
	if existingOrder == orderID && payerID == fact.PayerID && amount == fact.AmountMinor && currency == fact.Currency &&
		scale == fact.Scale && occurredAt.Equal(fact.OccurredAt.UTC()) &&
		(status == "succeeded" || status == PaymentStateNeedsReview) {
		return PaymentIngressReplay, nil
	}
	return PaymentIngressQuarantine, nil
}

func (s *SQLPaymentLedgerStore) PreviewProviderRefundIngress(ctx context.Context, refund Refund) (string, error) {
	provider := normalizePaymentProvider(refund.Provider)
	if refund.OrderID <= 0 || refund.AmountMinor <= 0 || refund.ExternalID == "" ||
		refund.PaymentExternalID == "" || (provider != PaymentMethodStars && provider != PaymentMethodCrypto) ||
		refund.Scale < 0 || refund.Scale > 9 || refund.Currency == "" || refund.PayerID <= 0 ||
		refund.OccurredAt.IsZero() || (provider == PaymentMethodStars && refund.ExternalID != refund.PaymentExternalID) {
		return "", ErrPaymentReceiptMismatch
	}
	var orderPayerID int64
	if err := s.db.QueryRowContext(ctx, `SELECT user_id FROM orders WHERE id = ?`, refund.OrderID).Scan(&orderPayerID); errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", fmt.Errorf("ledger: preview provider refund payer: %w", err)
	}
	if orderPayerID != refund.PayerID {
		return "", ErrPaymentReceiptMismatch
	}
	var captured, parentOrder, capturePayerID int64
	var currency, status string
	var scale int
	err := s.db.QueryRowContext(ctx, `
		SELECT amount_minor, order_id, payer_id, currency, scale, status FROM payment_attempts
		WHERE provider = ? AND external_id = ? AND status IN ('succeeded', 'needs_review')`,
		provider, refund.PaymentExternalID).Scan(&captured, &parentOrder, &capturePayerID, &currency, &scale, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return PaymentIngressQuarantine, nil
	}
	if err != nil {
		return "", fmt.Errorf("ledger: preview provider refund parent: %w", err)
	}
	if parentOrder != refund.OrderID || capturePayerID != refund.PayerID || currency != refund.Currency || scale != refund.Scale {
		return PaymentIngressQuarantine, nil
	}
	var existing Refund
	var refundOccurredAt time.Time
	err = s.db.QueryRowContext(ctx, `
		SELECT order_id, payment_external_id, payer_id, amount_minor, currency, scale, status, completed_at
		FROM refunds WHERE provider = ? AND external_id = ?`, provider, refund.ExternalID).Scan(
		&existing.OrderID, &existing.PaymentExternalID, &existing.PayerID, &existing.AmountMinor,
		&existing.Currency, &existing.Scale, &existing.Status, &refundOccurredAt)
	if err == nil {
		if existing.OrderID == refund.OrderID && existing.PaymentExternalID == refund.PaymentExternalID &&
			existing.PayerID == refund.PayerID && existing.AmountMinor == refund.AmountMinor && existing.Currency == refund.Currency &&
			existing.Scale == refund.Scale && existing.Status == "succeeded" && refundOccurredAt.Equal(refund.OccurredAt.UTC()) {
			return PaymentIngressReplay, nil
		}
		return PaymentIngressQuarantine, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("ledger: preview provider refund identity: %w", err)
	}
	var refunded int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(amount_minor), 0) FROM refunds
		WHERE provider = ? AND payment_external_id = ? AND status = 'succeeded'`,
		provider, refund.PaymentExternalID).Scan(&refunded); err != nil {
		return "", fmt.Errorf("ledger: preview provider refund sum: %w", err)
	}
	if refunded+refund.AmountMinor > captured {
		return PaymentIngressQuarantine, nil
	}
	return PaymentIngressApply, nil
}

// IngestProviderCapture records an authoritative provider-only capture as an
// exact replay or a needs-review fact. It never performs fulfillment, stock,
// loyalty, referral, or entitlement side effects.
func (s *SQLOrderStore) IngestProviderCapture(ctx context.Context, orderID int64, fact PaymentFact, audit PaymentIngressAudit) error {
	if err := validatePaymentIngressAudit(audit); err != nil {
		return err
	}
	order, err := s.loadPaymentOrder(ctx, orderID)
	if err != nil {
		return err
	}
	if fact.PayerID <= 0 || fact.PayerID != order.UserID || fact.OccurredAt.IsZero() {
		return ErrPaymentReceiptMismatch
	}
	fact, err = validatePaymentFact(*order, fact)
	if err != nil {
		return err
	}
	var existingOrder, payerID, amount int64
	var currency, status string
	var scale int
	var occurredAt time.Time
	err = s.db.QueryRowContext(ctx, `
		SELECT order_id, payer_id, amount_minor, currency, scale, status, occurred_at
		FROM payment_attempts WHERE provider = ? AND external_id = ?`,
		fact.Provider, fact.ExternalID).Scan(&existingOrder, &payerID, &amount, &currency, &scale, &status, &occurredAt)
	if err == nil {
		if existingOrder == orderID && payerID == fact.PayerID && amount == fact.AmountMinor && currency == fact.Currency &&
			scale == fact.Scale && occurredAt.Equal(fact.OccurredAt.UTC()) &&
			(status == "succeeded" || status == PaymentStateNeedsReview) {
			return nil
		}
		anomalyErr := s.recordPaymentAnomaly(ctx, PaymentAnomaly{
			ProposedOrderID: orderID, Provider: fact.Provider, EventKind: PaymentEventCaptured,
			ExternalID: fact.ExternalID, PayerID: fact.PayerID, AmountMinor: fact.AmountMinor,
			Currency: fact.Currency, Scale: fact.Scale, Reason: "operator_ingest_identity_conflict",
			OccurredAt: fact.OccurredAt,
		}, &audit)
		if anomalyErr != nil && !errors.Is(anomalyErr, ErrPaymentNeedsReview) {
			return errors.Join(ErrPaymentIdentityConflict, anomalyErr)
		}
		return ErrPaymentIdentityConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("order store: preview provider capture ingress: %w", err)
	}
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		lastErr = s.recordUnexpectedPayment(ctx, *order, fact.Provider, fact.ExternalID,
			"operator_ingested_provider_capture", &fact, &audit)
		if !isDatabaseLocked(lastErr) {
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
