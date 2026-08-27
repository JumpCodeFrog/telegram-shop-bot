package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RecordUnexpectedPayment persists a provider-settled fact that cannot be
// applied to the order automatically (for example a distinct second charge).
func (s *SQLOrderStore) RecordUnexpectedPayment(ctx context.Context, id int64, provider, externalID, reason string) error {
	order, err := s.loadPaymentOrder(ctx, id)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		lastErr = s.recordUnexpectedPayment(ctx, *order, provider, externalID, reason, nil, nil)
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

// RecordUnexpectedPaymentFact preserves the exact validated provider money
// fields when a capture cannot be applied to the order automatically.
func (s *SQLOrderStore) RecordUnexpectedPaymentFact(ctx context.Context, id int64, fact PaymentFact, reason string) error {
	order, err := s.loadPaymentOrder(ctx, id)
	if err != nil {
		return err
	}
	fact.Provider = normalizePaymentProvider(fact.Provider)
	resolved, exact, err := s.resolvedUnexpectedPaymentReplay(ctx, id, fact)
	if err != nil {
		return err
	}
	if resolved {
		if exact {
			// The append-only operator disposition is authoritative. Compare the
			// replay with the stored provider fact before order-price validation:
			// a later projection change must not reopen an exact resolved capture.
			return nil
		}
		recordErr := s.RecordPaymentAnomaly(ctx, anomalyFromPaymentFact(id, fact, "unexpected_payment_fact_mismatch"))
		return errors.Join(ErrPaymentIdentityConflict, ErrPaymentReceiptMismatch, recordErr)
	}
	validated, err := validatePaymentFact(*order, fact)
	if err != nil {
		// A redelivery may target an already-reviewed immutable identity while
		// disagreeing with its stored money fields. Preserve that signed conflict
		// instead of silently treating it as the previously resolved fact.
		fact.Provider = normalizePaymentProvider(fact.Provider)
		var existing int
		lookupErr := s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM payment_attempts
			WHERE order_id = ? AND provider = ? AND external_id = ?`,
			id, fact.Provider, fact.ExternalID).Scan(&existing)
		if lookupErr != nil {
			return errors.Join(err, fmt.Errorf("order store: lookup mismatched payment fact: %w", lookupErr))
		}
		if existing == 1 {
			recordErr := s.RecordPaymentAnomaly(ctx, anomalyFromPaymentFact(id, fact, "unexpected_payment_fact_mismatch"))
			if recordErr != nil {
				return errors.Join(err, recordErr)
			}
		}
		return err
	}
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		lastErr = s.recordUnexpectedPayment(ctx, *order, validated.Provider, validated.ExternalID, reason, &validated, nil)
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

// resolvedUnexpectedPaymentReplay checks the immutable capture identity, not
// the mutable order projection. A needs-review attempt remains append-only after
// its event is resolved, so provider redelivery is either an exact no-op or a
// new conflicting fact that must return to quarantine.
func (s *SQLOrderStore) resolvedUnexpectedPaymentReplay(ctx context.Context, orderID int64, fact PaymentFact) (resolved, exact bool, err error) {
	if fact.ExternalID == "" {
		return false, false, nil
	}
	var payerID, amount int64
	var currency string
	var scale, resolutionExists int
	var entitlement sql.NullTime
	var occurredAt time.Time
	err = s.db.QueryRowContext(ctx, `
		SELECT a.payer_id, a.amount_minor, a.currency, a.scale, a.entitlement_expires_at, a.occurred_at,
		       EXISTS(
		           SELECT 1 FROM payment_events e
		           JOIN payment_resolutions r
		             ON r.target_kind = 'payment_event' AND r.target_id = e.id
		           WHERE e.payment_attempt_id = a.id
		             AND e.event_kind = ? AND e.disposition = 'needs_review')
		FROM payment_attempts a
		WHERE a.order_id = ? AND a.provider = ? AND a.external_id = ?
		  AND a.status = 'needs_review'`,
		PaymentEventCaptured, orderID, fact.Provider, fact.ExternalID).Scan(
		&payerID, &amount, &currency, &scale, &entitlement, &occurredAt, &resolutionExists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("order store: lookup resolved payment replay: %w", err)
	}
	if resolutionExists == 0 {
		return false, false, nil
	}
	exact = amount == fact.AmountMinor && currency == fact.Currency && scale == fact.Scale &&
		(fact.PayerID == 0 || payerID == fact.PayerID) &&
		entitlementFactEqual(entitlement, fact.EntitlementExpiresAt) &&
		(fact.OccurredAt.IsZero() || occurredAt.Equal(fact.OccurredAt.UTC()))
	return true, exact, nil
}

func lastErrString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func isDatabaseLocked(err error) bool {
	text := strings.ToLower(lastErrString(err))
	return strings.Contains(text, "database is locked") || strings.Contains(text, "sqlite_busy")
}

// RecordSubscriptionRenewal is the compatibility wrapper for callers that do
// not yet carry a complete provider fact. It derives the immutable money fields
// from the order and still preserves the supplied entitlement expiry.
func (s *SQLOrderStore) RecordSubscriptionRenewal(ctx context.Context, id int64, provider, externalID string, sub Subscription) error {
	order, err := s.loadPaymentOrder(ctx, id)
	if err != nil {
		return err
	}
	provider = normalizePaymentProvider(provider)
	amount, currency, scale, err := orderMoney(*order, provider)
	if err != nil {
		return err
	}
	return s.RecordSubscriptionRenewalFact(ctx, id, PaymentFact{
		Provider: provider, ExternalID: externalID,
		AmountMinor: amount, Currency: currency, Scale: scale,
		EntitlementExpiresAt: sub.ExpiresAt,
	}, sub)
}

// RecordSubscriptionRenewalFact persists a valid recurring capture without
// replaying stock, loyalty, referral, or the original order transition. Every
// quarantine branch receives the exact provider fact, including its immutable
// entitlement expiry. An exact provider replay is idempotent; a conflicting
// identity fails closed.
func (s *SQLOrderStore) RecordSubscriptionRenewalFact(ctx context.Context, id int64, fact PaymentFact, sub Subscription) error {
	order, err := s.loadPaymentOrder(ctx, id)
	if err != nil {
		return err
	}
	fact, err = validatePaymentFact(*order, fact)
	if err != nil {
		return err
	}
	if fact.EntitlementExpiresAt.IsZero() || sub.ExpiresAt.IsZero() ||
		!fact.EntitlementExpiresAt.Equal(sub.ExpiresAt) {
		return ErrPaymentReceiptMismatch
	}
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		lastErr = s.recordSubscriptionRenewalOnce(ctx, id, fact, sub)
		if errors.Is(lastErr, ErrPaymentIdentityConflict) {
			recordErr := s.RecordUnexpectedPaymentFact(ctx, id, fact, "subscription_identity_conflict")
			if recordErr == nil {
				return nil
			}
			if recordErr != nil && !errors.Is(recordErr, ErrPaymentNeedsReview) {
				return errors.Join(ErrPaymentIdentityConflict, recordErr)
			}
			return ErrPaymentIdentityConflict
		}
		if errors.Is(lastErr, ErrSubscriptionOrderConflict) {
			recordErr := s.RecordUnexpectedPaymentFact(ctx, id, fact, "subscription_renewal_entitlement_conflict")
			if recordErr == nil {
				return nil
			}
			if recordErr != nil && !errors.Is(recordErr, ErrPaymentNeedsReview) {
				return errors.Join(ErrPaymentNeedsReview, recordErr)
			}
			return ErrPaymentNeedsReview
		}
		if errors.Is(lastErr, ErrSubscriptionEntitlement) {
			recordErr := s.RecordUnexpectedPaymentFact(ctx, id, fact, "subscription_renewal_entitlement_failed")
			if recordErr == nil {
				return nil
			}
			if recordErr != nil && !errors.Is(recordErr, ErrPaymentNeedsReview) {
				return errors.Join(ErrPaymentNeedsReview, lastErr, recordErr)
			}
			return errors.Join(ErrPaymentNeedsReview, lastErr)
		}
		if errors.Is(lastErr, ErrOrderStatusConflict) {
			recordErr := s.RecordUnexpectedPaymentFact(ctx, id, fact, "subscription_renewal_order_state_conflict")
			if recordErr == nil {
				return nil
			}
			if recordErr != nil && !errors.Is(recordErr, ErrPaymentNeedsReview) {
				return errors.Join(ErrPaymentNeedsReview, recordErr)
			}
			return ErrPaymentNeedsReview
		}
		if lastErr == nil || !isDatabaseLocked(lastErr) {
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

func (s *SQLOrderStore) recordSubscriptionRenewalOnce(ctx context.Context, id int64, fact PaymentFact, sub Subscription) error {
	fact.Provider = normalizePaymentProvider(fact.Provider)
	if fact.Provider != PaymentMethodStars || fact.ExternalID == "" || fact.EntitlementExpiresAt.IsZero() {
		return ErrPaymentReceiptMismatch
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("order store: begin subscription renewal: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var order Order
	if err := tx.QueryRowContext(ctx,
		`SELECT id, user_id, COALESCE(total_usd, 0), COALESCE(total_stars, 0),
		        COALESCE(payment_method, ''), COALESCE(payment_id, ''),
		        COALESCE(status, 'pending'), payment_state,
		        COALESCE(subscription_product_id, 0), subscription_period_days
		 FROM orders WHERE id = ?`, id).Scan(
		&order.ID, &order.UserID, &order.TotalUSD, &order.TotalStars,
		&order.PaymentMethod, &order.PaymentID, &order.Status, &order.PaymentState,
		&order.SubscriptionProductID, &order.SubscriptionPeriodDays,
	); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("order store: load subscription renewal order: %w", err)
	}
	if (order.Status != OrderStatusPaid && order.Status != OrderStatusDelivered) || (order.PaymentState != PaymentStateSettled &&
		order.PaymentState != PaymentStatePartiallyRefunded && order.PaymentState != PaymentStateRefunded) {
		return ErrOrderStatusConflict
	}
	validated, err := validatePaymentFact(order, fact)
	if err != nil {
		return err
	}
	fact = validated
	if !fact.EntitlementExpiresAt.Equal(sub.ExpiresAt) {
		return ErrPaymentReceiptMismatch
	}
	provider, externalID := fact.Provider, fact.ExternalID
	if err := validateSubscriptionSettlement(order, provider, externalID, sub); err != nil {
		return err
	}
	amount, currency, scale := fact.AmountMinor, fact.Currency, fact.Scale

	var attemptID, existingOrder, existingPayerID int64
	var existingAmount int64
	var existingCurrency, existingStatus string
	var existingScale int
	var existingEntitlement sql.NullTime
	var existingOccurredAt time.Time
	err = tx.QueryRowContext(ctx,
		`SELECT id, order_id, payer_id, amount_minor, currency, scale, status,
		        entitlement_expires_at, occurred_at
		 FROM payment_attempts WHERE provider = ? AND external_id = ?`,
		provider, externalID).Scan(&attemptID, &existingOrder, &existingPayerID, &existingAmount,
		&existingCurrency, &existingScale, &existingStatus, &existingEntitlement, &existingOccurredAt)
	switch {
	case err == nil:
		if existingOrder != order.ID || existingAmount != amount || existingCurrency != currency || existingScale != scale ||
			(fact.PayerID > 0 && existingPayerID != fact.PayerID) ||
			(!fact.OccurredAt.IsZero() && !existingOccurredAt.Equal(fact.OccurredAt.UTC())) {
			return ErrPaymentIdentityConflict
		}
		if existingEntitlement.Valid {
			if !fact.EntitlementExpiresAt.Equal(existingEntitlement.Time) {
				return ErrPaymentIdentityConflict
			}
			sub.ExpiresAt = existingEntitlement.Time
		} else if existingStatus == PaymentStateNeedsReview {
			if _, err := tx.ExecContext(ctx,
				`UPDATE payment_attempts SET entitlement_expires_at = ?
				 WHERE id = ? AND entitlement_expires_at IS NULL`, fact.EntitlementExpiresAt.UTC(), attemptID); err != nil {
				return fmt.Errorf("order store: repair quarantined renewal expiry: %w", err)
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("order store: commit quarantined renewal expiry: %w", err)
			}
			return ErrPaymentNeedsReview
		}
		if existingStatus == PaymentStateNeedsReview {
			var resolved int
			if err := tx.QueryRowContext(ctx, `
				SELECT EXISTS(
					SELECT 1 FROM payment_events e
					JOIN payment_resolutions r
					  ON r.target_kind = 'payment_event' AND r.target_id = e.id
					WHERE e.payment_attempt_id = ? AND e.event_kind = ?
					  AND e.disposition = 'needs_review')`,
				attemptID, PaymentEventCaptured).Scan(&resolved); err != nil {
				return fmt.Errorf("order store: lookup resolved renewal replay: %w", err)
			}
			if resolved != 0 {
				return nil
			}
			return ErrPaymentNeedsReview
		}
		if existingStatus != "succeeded" {
			return ErrPaymentIdentityConflict
		}
		if err := renewSubscriptionTx(ctx, tx, sub, order.SubscriptionPeriodDays, true); err != nil {
			return err
		}
		if !existingEntitlement.Valid {
			if _, err := tx.ExecContext(ctx,
				`UPDATE payment_attempts SET entitlement_expires_at = ?
				 WHERE id = ? AND entitlement_expires_at IS NULL`, sub.ExpiresAt.UTC(), attemptID); err != nil {
				return fmt.Errorf("order store: persist repaired renewal expiry: %w", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("order store: commit subscription renewal replay repair: %w", err)
		}
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("order store: lookup subscription renewal: %w", err)
	}
	if err := renewSubscriptionTx(ctx, tx, sub, order.SubscriptionPeriodDays, false); err != nil {
		return err
	}

	var occurredAt any
	if !fact.OccurredAt.IsZero() {
		occurredAt = fact.OccurredAt.UTC()
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO payment_attempts
		 (order_id, provider, external_id, payer_id, amount_minor, currency, scale, status, entitlement_expires_at, occurred_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 'succeeded', ?, COALESCE(?, CURRENT_TIMESTAMP))`,
		order.ID, provider, externalID, fact.PayerID, amount, currency, scale, sub.ExpiresAt.UTC(), occurredAt)
	if err != nil {
		return fmt.Errorf("order store: insert subscription renewal: %w", err)
	}
	attemptID, err = res.LastInsertId()
	if err != nil {
		return fmt.Errorf("order store: subscription renewal attempt id: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO payment_events
		 (order_id, payment_attempt_id, provider, event_kind, external_id,
		  amount_minor, currency, scale, disposition, occurred_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'settled', COALESCE(?, CURRENT_TIMESTAMP))`,
		order.ID, attemptID, provider, PaymentEventCaptured, externalID,
		amount, currency, scale, occurredAt); err != nil {
		return fmt.Errorf("order store: append subscription renewal capture: %w", err)
	}
	nextState := PaymentStateSettled
	var refunded int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount_minor), 0) FROM refunds
		 WHERE order_id = ? AND provider = ? AND status = 'succeeded'`, order.ID, provider).Scan(&refunded); err != nil {
		return fmt.Errorf("order store: sum renewal refunds: %w", err)
	}
	if refunded > 0 {
		nextState = PaymentStatePartiallyRefunded
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE orders SET payment_state = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, nextState, order.ID); err != nil {
		return fmt.Errorf("order store: update subscription renewal projection: %w", err)
	}
	if err := appendOrderEvent(ctx, tx, order.ID, "payment.subscription_renewed", order.PaymentState, nextState); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("order store: commit subscription renewal: %w", err)
	}
	return nil
}

func (s *SQLOrderStore) loadPaymentOrder(ctx context.Context, id int64) (*Order, error) {
	var order Order
	if err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, COALESCE(total_usd, 0), COALESCE(total_stars, 0),
		        COALESCE(payment_method, ''), COALESCE(payment_id, ''),
		        COALESCE(status, 'pending'), payment_state
		 FROM orders WHERE id = ?`, id).Scan(
		&order.ID, &order.UserID, &order.TotalUSD, &order.TotalStars,
		&order.PaymentMethod, &order.PaymentID, &order.Status, &order.PaymentState,
	); err == sql.ErrNoRows {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, fmt.Errorf("order store: load payment order: %w", err)
	}
	return &order, nil
}

func anomalyFromPaymentFact(orderID int64, fact PaymentFact, reason string) PaymentAnomaly {
	rawPayload := ""
	if !fact.EntitlementExpiresAt.IsZero() {
		rawPayload = "entitlement_expires_at:" + fact.EntitlementExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	return PaymentAnomaly{
		ProposedOrderID: orderID,
		Provider:        fact.Provider,
		ExternalID:      fact.ExternalID,
		PayerID:         fact.PayerID,
		AmountMinor:     fact.AmountMinor,
		Currency:        fact.Currency,
		Scale:           fact.Scale,
		RawPayload:      rawPayload,
		Reason:          reason,
		OccurredAt:      fact.OccurredAt,
	}
}

func (s *SQLOrderStore) recordUnexpectedPayment(ctx context.Context, order Order, provider, externalID, reason string, fact *PaymentFact, audit *PaymentIngressAudit) error {
	provider = normalizePaymentProvider(provider)
	amount, currency, scale, err := orderMoney(order, provider)
	if err != nil {
		return err
	}
	if externalID == "" {
		return ErrInvalidMoney
	}
	if fact != nil {
		amount, currency, scale = fact.AmountMinor, fact.Currency, fact.Scale
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("order store: begin unexpected payment: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var existingAttemptID, existingOrder, existingPayerID, existingAmount int64
	var existingCurrency, existingStatus string
	var existingScale int
	var existingEntitlement sql.NullTime
	var existingOccurredAt time.Time
	err = tx.QueryRowContext(ctx,
		`SELECT id, order_id, payer_id, amount_minor, currency, scale, status,
		        entitlement_expires_at, occurred_at
		 FROM payment_attempts WHERE provider = ? AND external_id = ?`,
		provider, externalID).Scan(&existingAttemptID, &existingOrder, &existingPayerID, &existingAmount,
		&existingCurrency, &existingScale, &existingStatus, &existingEntitlement, &existingOccurredAt)
	if err == nil && fact != nil {
		exactFact := existingOrder == order.ID && existingAmount == amount && existingCurrency == currency &&
			existingScale == scale && (fact.PayerID == 0 || existingPayerID == fact.PayerID) &&
			entitlementFactEqual(existingEntitlement, fact.EntitlementExpiresAt) &&
			(fact.OccurredAt.IsZero() || existingOccurredAt.Equal(fact.OccurredAt.UTC()))
		if exactFact && existingStatus == "succeeded" {
			return nil
		}
		if exactFact && existingStatus == PaymentStateNeedsReview {
			var resolved int
			if resolveErr := tx.QueryRowContext(ctx, `
				SELECT EXISTS(
					SELECT 1 FROM payment_events e
					JOIN payment_resolutions r
					  ON r.target_kind = 'payment_event' AND r.target_id = e.id
					WHERE e.payment_attempt_id = ? AND e.event_kind = ?
					  AND e.disposition = 'needs_review')`,
				existingAttemptID, PaymentEventCaptured).Scan(&resolved); resolveErr != nil {
				return fmt.Errorf("order store: lookup payment resolution replay: %w", resolveErr)
			}
			if resolved != 0 {
				// Operator disposition is append-only and authoritative. An exact
				// provider replay must not reopen the case or mutate its projection.
				return nil
			}
			return ErrPaymentNeedsReview
		}
		// The immutable provider identity already belongs to an attempt, so a
		// second row cannot be inserted. Preserve the exact conflicting fact,
		// including entitlement expiry, in the anomaly inbox instead.
		if existingOrder != order.ID {
			reason = "identity_conflict"
		}
		_ = tx.Rollback()
		recordErr := s.recordPaymentAnomaly(ctx, anomalyFromPaymentFact(order.ID, *fact, reason), audit)
		return errors.Join(ErrPaymentIdentityConflict, recordErr)
	}
	if err == nil && existingOrder == order.ID {
		// The capture already exists, but this call proves it still cannot be
		// applied safely. Preserve the reason in the anomaly inbox and move the
		// projection to needs_review instead of returning a non-durable error.
		_ = tx.Rollback()
		return s.recordPaymentAnomaly(ctx, PaymentAnomaly{
			ProposedOrderID: order.ID,
			Provider:        provider,
			ExternalID:      externalID,
			AmountMinor:     amount,
			Currency:        currency,
			Scale:           scale,
			Reason:          reason,
		}, audit)
	}
	if err == nil && existingOrder != order.ID {
		reason = "identity_conflict"
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("order store: lookup unexpected payment: %w", err)
	}

	var attemptID sql.NullInt64
	insertedCapture := false
	var captureEventID int64
	if errors.Is(err, sql.ErrNoRows) {
		var entitlementExpiry any
		var occurredAt any
		if fact != nil && !fact.EntitlementExpiresAt.IsZero() {
			entitlementExpiry = fact.EntitlementExpiresAt.UTC()
		}
		if fact != nil && !fact.OccurredAt.IsZero() {
			occurredAt = fact.OccurredAt.UTC()
		}
		payerID := int64(0)
		if fact != nil {
			payerID = fact.PayerID
		}
		res, insertErr := tx.ExecContext(ctx,
			`INSERT INTO payment_attempts
			 (order_id, provider, external_id, payer_id, amount_minor, currency, scale, status, entitlement_expires_at, occurred_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, 'needs_review', ?, COALESCE(?, CURRENT_TIMESTAMP))`,
			order.ID, provider, externalID, payerID, amount, currency, scale, entitlementExpiry, occurredAt)
		if insertErr != nil {
			return fmt.Errorf("order store: insert unexpected payment attempt: %w", insertErr)
		}
		id, idErr := res.LastInsertId()
		if idErr != nil {
			return fmt.Errorf("order store: unexpected payment attempt id: %w", idErr)
		}
		attemptID = sql.NullInt64{Int64: id, Valid: true}
		result, insertErr := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO payment_events
			 (order_id, payment_attempt_id, provider, event_kind, external_id,
			  amount_minor, currency, scale, disposition, occurred_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'needs_review', COALESCE(?, CURRENT_TIMESTAMP))`,
			order.ID, attemptID, provider, PaymentEventCaptured, externalID,
			amount, currency, scale, occurredAt)
		if insertErr != nil {
			return fmt.Errorf("order store: record unexpected capture: %w", insertErr)
		}
		insertedCapture, err = rowsInserted(result)
		if err != nil {
			return fmt.Errorf("order store: unexpected capture rows affected: %w", err)
		}
		if insertedCapture {
			captureEventID, err = result.LastInsertId()
			if err != nil {
				return fmt.Errorf("order store: unexpected capture event id: %w", err)
			}
		}
	}

	insertedIssue := false
	// A fresh unexpected capture already carries its needs-review disposition.
	// Add a synthetic conflict row only when the real provider identity belongs
	// to another order; this keeps the ledger factual rather than duplicative.
	if existingOrder != 0 && existingOrder != order.ID {
		identity := fmt.Sprintf("%s:order:%d:%s", externalID, order.ID, reason)
		result, insertErr := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO payment_events
			 (order_id, provider, event_kind, external_id, amount_minor, currency, scale, disposition)
			 VALUES (?, ?, ?, ?, ?, ?, ?, 'needs_review')`,
			order.ID, provider, PaymentEventIdentityConflict, identity, amount, currency, scale)
		if insertErr != nil {
			return fmt.Errorf("order store: record unexpected payment: %w", insertErr)
		}
		insertedIssue, err = rowsInserted(result)
		if err != nil {
			return fmt.Errorf("order store: unexpected payment rows affected: %w", err)
		}
	}
	if !insertedCapture && !insertedIssue {
		return ErrPaymentNeedsReview
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE orders SET payment_state = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		PaymentStateNeedsReview, order.ID); err != nil {
		return fmt.Errorf("order store: flag unexpected payment: %w", err)
	}
	fromState := order.PaymentState
	if fromState == "" {
		fromState = PaymentStateSettled
	}
	if err := appendOrderEvent(ctx, tx, order.ID, "payment.needs_review", fromState, PaymentStateNeedsReview); err != nil {
		return err
	}
	if audit != nil {
		if !insertedCapture || captureEventID <= 0 {
			return ErrPaymentReviewConflict
		}
		if err := appendPaymentIngressAudit(ctx, tx, order.ID, provider, PaymentEventCaptured,
			PaymentIngressTargetEvent, captureEventID, *audit); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("order store: commit unexpected payment: %w", err)
	}
	return ErrPaymentNeedsReview
}

func entitlementFactEqual(existing sql.NullTime, supplied time.Time) bool {
	if supplied.IsZero() {
		return !existing.Valid
	}
	return existing.Valid && existing.Time.Equal(supplied.UTC())
}

func rowsInserted(result sql.Result) (bool, error) {
	n, err := result.RowsAffected()
	return n > 0, err
}
