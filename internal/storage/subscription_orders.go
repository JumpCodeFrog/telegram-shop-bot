package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// subscriptionReservationConflict protects one recurring contract per user
// and product. Canceled subscriptions still grant access until expires_at, and
// every pending order (including needs_review) keeps the reservation.
func subscriptionReservationConflict(ctx context.Context, tx *sql.Tx, userID, productID int64) (bool, error) {
	var conflict int
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM subscriptions
			WHERE user_id = ? AND product_id = ?
			  AND status IN ('active', 'canceled')
			  AND expires_at > CURRENT_TIMESTAMP
			UNION ALL
			SELECT 1 FROM orders
			WHERE user_id = ? AND subscription_product_id = ? AND status = 'pending'
		)`, userID, productID, userID, productID).Scan(&conflict)
	if err != nil {
		return false, fmt.Errorf("order store: check subscription reservation: %w", err)
	}
	return conflict != 0, nil
}

func isSubscriptionReservationConstraint(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "idx_orders_pending_subscription") ||
		strings.Contains(text, "orders.user_id, orders.subscription_product_id")
}

func validateSubscriptionSettlement(order Order, provider, externalID string, sub Subscription) error {
	if normalizePaymentProvider(provider) != PaymentMethodStars || externalID == "" ||
		order.SubscriptionProductID <= 0 || order.SubscriptionPeriodDays <= 0 ||
		sub.UserID != order.UserID || sub.ProductID != order.SubscriptionProductID ||
		sub.OrderID != order.ID || sub.ChargeID != externalID || sub.ExpiresAt.IsZero() {
		return ErrPaymentReceiptMismatch
	}
	return nil
}

// activateSubscriptionTx establishes a new recurring contract. A previous
// expired contract may be replaced; an unexpired contract remains exclusive.
func activateSubscriptionTx(ctx context.Context, tx *sql.Tx, sub Subscription) error {
	var existing Subscription
	err := tx.QueryRowContext(ctx, `
		SELECT id, COALESCE(order_id, 0), telegram_charge_id, status, expires_at
		FROM subscriptions WHERE user_id = ? AND product_id = ?`,
		sub.UserID, sub.ProductID).Scan(
		&existing.ID, &existing.OrderID, &existing.ChargeID, &existing.Status, &existing.ExpiresAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `
			INSERT INTO subscriptions
				(user_id, product_id, order_id, telegram_charge_id, status, expires_at)
			VALUES (?, ?, ?, ?, 'active', ?)`,
			sub.UserID, sub.ProductID, sub.OrderID, sub.ChargeID, sub.ExpiresAt.UTC())
		if err != nil {
			return fmt.Errorf("%w: insert: %v", ErrSubscriptionEntitlement, err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("%w: load: %v", ErrSubscriptionEntitlement, err)
	case existing.OrderID == sub.OrderID && existing.ChargeID == sub.ChargeID:
		return nil
	case existing.ExpiresAt.After(time.Now().UTC()) &&
		(existing.Status == SubStatusActive || existing.Status == SubStatusCanceled):
		return ErrSubscriptionOrderConflict
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE subscriptions
		SET order_id = ?, telegram_charge_id = ?, status = 'active', expires_at = ?,
		    reminded_at = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`, sub.OrderID, sub.ChargeID, sub.ExpiresAt.UTC(), existing.ID)
	if err != nil {
		return fmt.Errorf("%w: replace expired: %v", ErrSubscriptionEntitlement, err)
	}
	return nil
}

// renewSubscriptionTx extends an existing active contract without changing
// its initial cancellation charge. Canceled/expired or cross-order renewals are
// quarantined by the caller instead of silently reactivating access.
func renewSubscriptionTx(ctx context.Context, tx *sql.Tx, sub Subscription, periodDays int, replay bool) error {
	var existing Subscription
	err := tx.QueryRowContext(ctx, `
		SELECT id, COALESCE(order_id, 0), telegram_charge_id, status, expires_at
		FROM subscriptions WHERE user_id = ? AND product_id = ?`,
		sub.UserID, sub.ProductID).Scan(
		&existing.ID, &existing.OrderID, &existing.ChargeID, &existing.Status, &existing.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSubscriptionOrderConflict
	}
	if err != nil {
		return fmt.Errorf("%w: renewal load: %v", ErrSubscriptionEntitlement, err)
	}
	if existing.Status != SubStatusActive || existing.OrderID != sub.OrderID {
		return ErrSubscriptionOrderConflict
	}
	if !sub.ExpiresAt.After(existing.ExpiresAt) {
		if replay {
			return nil
		}
		return ErrSubscriptionOrderConflict
	}
	now := time.Now().UTC()
	base := existing.ExpiresAt.UTC()
	if now.After(base) {
		base = now
	}
	if periodDays <= 0 || !sub.ExpiresAt.After(now) ||
		sub.ExpiresAt.After(base.Add(time.Duration(periodDays)*24*time.Hour+24*time.Hour)) {
		return ErrSubscriptionOrderConflict
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE subscriptions
		SET expires_at = ?, reminded_at = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND status = 'active' AND order_id = ?`,
		sub.ExpiresAt.UTC(), existing.ID, sub.OrderID)
	if err != nil {
		return fmt.Errorf("%w: renewal extend: %v", ErrSubscriptionEntitlement, err)
	}
	return nil
}
