package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SQLOrderStore implements OrderStore using a *sql.DB connection.
type SQLOrderStore struct {
	db *sql.DB
}

// NewSQLOrderStore creates a new SQLOrderStore from the given DB.
func NewSQLOrderStore(d *DB) *SQLOrderStore {
	return &SQLOrderStore{db: d.Conn()}
}

// CreateOrder inserts an order and its items within a transaction. Returns the
// new order ID.
func (s *SQLOrderStore) CreateOrder(ctx context.Context, order *Order, items []OrderItem) (int64, error) {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		orderID, err := s.createOrderOnce(ctx, order, items)
		if err == nil || !isDatabaseLocked(err) {
			return orderID, err
		}
		lastErr = err
		timer := time.NewTimer(time.Duration(attempt+1) * 20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, ctx.Err()
		case <-timer.C:
		}
	}
	return 0, lastErr
}

func (s *SQLOrderStore) createOrderOnce(ctx context.Context, order *Order, items []OrderItem) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("order store: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	state, err := stateForLegacyStatus(order.Status)
	if err != nil {
		return 0, err
	}
	if order.SubscriptionProductID != 0 {
		if order.SubscriptionPeriodDays <= 0 || len(items) != 1 ||
			items[0].ProductID != order.SubscriptionProductID || items[0].Quantity != 1 {
			return 0, ErrInvalidSubscriptionCart
		}
		conflict, err := subscriptionReservationConflict(ctx, tx, order.UserID, order.SubscriptionProductID)
		if err != nil {
			return 0, err
		}
		if conflict {
			return 0, ErrSubscriptionOrderConflict
		}
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO orders
		 (user_id, total_usd, total_stars, payment_method, payment_id, status,
		  order_state, payment_state, fulfillment_state, discount_pct, promo_code,
		  subscription_product_id, subscription_period_days)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, 0), ?)`,
		order.UserID, order.TotalUSD, order.TotalStars,
		order.PaymentMethod, order.PaymentID, order.Status,
		state.order, state.payment, state.fulfillment,
		order.DiscountPct, order.PromoCode,
		order.SubscriptionProductID, order.SubscriptionPeriodDays)
	if err != nil {
		if order.SubscriptionProductID != 0 && isSubscriptionReservationConstraint(err) {
			return 0, ErrSubscriptionOrderConflict
		}
		return 0, fmt.Errorf("order store: insert order: %w", err)
	}

	orderID, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("order store: last insert id: %w", err)
	}

	for _, item := range items {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO order_items (order_id, product_id, product_name, quantity, price_usd)
			 VALUES (?, ?, ?, ?, ?)`,
			orderID, item.ProductID, item.ProductName, item.Quantity, item.PriceUSD)
		if err != nil {
			return 0, fmt.Errorf("order store: insert order item: %w", err)
		}
	}
	if err := appendOrderEvent(ctx, tx, orderID, "order.created", "", state.order); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("order store: commit tx: %w", err)
	}

	return orderID, nil
}

// GetOrder returns a single order by ID with its items loaded. Returns
// ErrNotFound if the order does not exist.
func (s *SQLOrderStore) GetOrder(ctx context.Context, id int64) (*Order, error) {
	var o Order
	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, COALESCE(total_usd, 0), COALESCE(total_stars, 0),
		        COALESCE(payment_method, ''), COALESCE(payment_id, ''),
		        COALESCE(status, 'pending'), order_state, payment_state, fulfillment_state,
		        COALESCE(discount_pct, 0), COALESCE(promo_code, ''),
		        COALESCE(subscription_product_id, 0), subscription_period_days,
		        created_at, updated_at
		 FROM orders WHERE id = ?`, id).
		Scan(&o.ID, &o.UserID, &o.TotalUSD, &o.TotalStars,
			&o.PaymentMethod, &o.PaymentID, &o.Status,
			&o.OrderState, &o.PaymentState, &o.FulfillmentState,
			&o.DiscountPct, &o.PromoCode,
			&o.SubscriptionProductID, &o.SubscriptionPeriodDays,
			&o.CreatedAt, &o.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("order store: get order: %w", err)
	}

	items, err := s.loadOrderItems(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	o.Items = items

	return &o, nil
}

// HasSubscriptionEntitlementConflict is a last pre-charge guard for legacy
// or manually repaired data. New order creation enforces the same invariant,
// but a stale pending invoice must also fail closed at pre-checkout.
func (s *SQLOrderStore) HasSubscriptionEntitlementConflict(ctx context.Context, userID, productID int64) (bool, error) {
	if productID <= 0 {
		return false, nil
	}
	var conflict int
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM subscriptions
			WHERE user_id = ? AND product_id = ?
			  AND status IN ('active', 'canceled')
			  AND expires_at > CURRENT_TIMESTAMP
		)`, userID, productID).Scan(&conflict)
	if err != nil {
		return false, fmt.Errorf("order store: precheckout subscription guard: %w", err)
	}
	return conflict != 0, nil
}

// GetUserOrders returns all orders for the given user sorted by created_at
// descending, each with its items loaded.
func (s *SQLOrderStore) GetUserOrders(ctx context.Context, userID int64) ([]Order, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, COALESCE(total_usd, 0), COALESCE(total_stars, 0),
		        COALESCE(payment_method, ''), COALESCE(payment_id, ''),
		        COALESCE(status, 'pending'), order_state, payment_state, fulfillment_state,
		        COALESCE(discount_pct, 0), COALESCE(promo_code, ''),
		        COALESCE(subscription_product_id, 0), subscription_period_days,
		        created_at, updated_at
		 FROM orders WHERE user_id = ?
		 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("order store: get user orders: %w", err)
	}
	defer rows.Close()

	var orders []Order
	for rows.Next() {
		var o Order
		if err := rows.Scan(&o.ID, &o.UserID, &o.TotalUSD, &o.TotalStars,
			&o.PaymentMethod, &o.PaymentID, &o.Status,
			&o.OrderState, &o.PaymentState, &o.FulfillmentState,
			&o.DiscountPct, &o.PromoCode,
			&o.SubscriptionProductID, &o.SubscriptionPeriodDays,
			&o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, fmt.Errorf("order store: scan order: %w", err)
		}
		orders = append(orders, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range orders {
		items, err := s.loadOrderItems(ctx, orders[i].ID)
		if err != nil {
			return nil, err
		}
		orders[i].Items = items
	}

	return orders, nil
}

// GetAllOrders returns all orders sorted by created_at descending. If
// statusFilter is non-empty, only orders with that status are returned.
func (s *SQLOrderStore) GetAllOrders(ctx context.Context, statusFilter string) ([]Order, error) {
	var (
		rows *sql.Rows
		err  error
	)

	if statusFilter != "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, user_id, COALESCE(total_usd, 0), COALESCE(total_stars, 0),
			        COALESCE(payment_method, ''), COALESCE(payment_id, ''),
			        COALESCE(status, 'pending'), order_state, payment_state, fulfillment_state,
			        COALESCE(discount_pct, 0), COALESCE(promo_code, ''),
			        COALESCE(subscription_product_id, 0), subscription_period_days,
			        created_at, updated_at
			 FROM orders WHERE status = ?
			 ORDER BY created_at DESC`, statusFilter)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, user_id, COALESCE(total_usd, 0), COALESCE(total_stars, 0),
			        COALESCE(payment_method, ''), COALESCE(payment_id, ''),
			        COALESCE(status, 'pending'), order_state, payment_state, fulfillment_state,
			        COALESCE(discount_pct, 0), COALESCE(promo_code, ''),
			        COALESCE(subscription_product_id, 0), subscription_period_days,
			        created_at, updated_at
			 FROM orders ORDER BY created_at DESC`)
	}
	if err != nil {
		return nil, fmt.Errorf("order store: get all orders: %w", err)
	}
	defer rows.Close()

	var orders []Order
	for rows.Next() {
		var o Order
		if err := rows.Scan(&o.ID, &o.UserID, &o.TotalUSD, &o.TotalStars,
			&o.PaymentMethod, &o.PaymentID, &o.Status,
			&o.OrderState, &o.PaymentState, &o.FulfillmentState,
			&o.DiscountPct, &o.PromoCode,
			&o.SubscriptionProductID, &o.SubscriptionPeriodDays,
			&o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, fmt.Errorf("order store: scan order: %w", err)
		}
		orders = append(orders, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range orders {
		items, err := s.loadOrderItems(ctx, orders[i].ID)
		if err != nil {
			return nil, err
		}
		orders[i].Items = items
	}

	return orders, nil
}

// UpdateOrderStatus atomically transitions an order from fromStatus to status.
// If transitioning to "paid", it also decrements the stock of all products in the order.
// Returns ErrOrderStatusConflict if the order is not in fromStatus (already
// transitioned or wrong ID), making the operation idempotent and race-safe.
func (s *SQLOrderStore) UpdateOrderStatus(ctx context.Context, id int64, fromStatus, status, paymentMethod, paymentID string) error {
	return s.updateOrderStatus(ctx, id, fromStatus, status, paymentMethod, paymentID, nil, nil)
}

// UpdateOrderStatusWithPaymentFact writes the validated provider fact without
// replacing its currency with the order's accounting currency.
func (s *SQLOrderStore) UpdateOrderStatusWithPaymentFact(ctx context.Context, id int64, fromStatus, status string, fact PaymentFact) error {
	return s.updateOrderStatus(ctx, id, fromStatus, status, fact.Provider, fact.ExternalID, nil, &fact)
}

// UpdateOrderStatusWithSubscription settles a Stars capture and activates its
// subscription entitlement in the same SQLite transaction.
func (s *SQLOrderStore) UpdateOrderStatusWithSubscription(ctx context.Context, id int64, fromStatus, status, paymentMethod, paymentID string, sub Subscription) error {
	return s.updateOrderStatus(ctx, id, fromStatus, status, paymentMethod, paymentID, &sub, nil)
}

// UpdateOrderStatusWithSubscriptionFact settles a validated provider capture
// and its entitlement in the same transaction.
func (s *SQLOrderStore) UpdateOrderStatusWithSubscriptionFact(ctx context.Context, id int64, fromStatus, status string, fact PaymentFact, sub Subscription) error {
	return s.updateOrderStatus(ctx, id, fromStatus, status, fact.Provider, fact.ExternalID, &sub, &fact)
}

func (s *SQLOrderStore) updateOrderStatus(ctx context.Context, id int64, fromStatus, status, paymentMethod, paymentID string, sub *Subscription, fact *PaymentFact) error {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		lastErr = s.updateOrderStatusOnce(ctx, id, fromStatus, status, paymentMethod, paymentID, sub, fact)
		if errors.Is(lastErr, ErrPaymentIdentityConflict) {
			// The state transition transaction deliberately rolls back on a
			// cross-order provider identity. Persist the conflict in a separate
			// transaction, while preserving the caller-facing identity error.
			var recordErr error
			if fact != nil {
				recordErr = s.RecordPaymentAnomaly(ctx, anomalyFromPaymentFact(id, *fact, "identity_conflict"))
			} else {
				recordErr = s.RecordUnexpectedPayment(ctx, id, paymentMethod, paymentID, "identity_conflict")
			}
			if recordErr != nil && !errors.Is(recordErr, ErrPaymentNeedsReview) {
				return errors.Join(ErrPaymentIdentityConflict, recordErr)
			}
			return ErrPaymentIdentityConflict
		}
		if errors.Is(lastErr, ErrSubscriptionOrderConflict) && sub != nil {
			var recordErr error
			if fact != nil {
				recordErr = s.RecordUnexpectedPaymentFact(ctx, id, *fact, "subscription_entitlement_conflict")
			} else {
				recordErr = s.RecordUnexpectedPayment(ctx, id, paymentMethod, paymentID, "subscription_entitlement_conflict")
			}
			if recordErr == nil {
				return nil
			}
			if recordErr != nil && !errors.Is(recordErr, ErrPaymentNeedsReview) {
				return errors.Join(ErrPaymentNeedsReview, recordErr)
			}
			return ErrPaymentNeedsReview
		}
		if errors.Is(lastErr, ErrSubscriptionEntitlement) && sub != nil {
			var recordErr error
			if fact != nil {
				recordErr = s.RecordUnexpectedPaymentFact(ctx, id, *fact, "subscription_entitlement_failed")
			} else {
				recordErr = s.RecordUnexpectedPayment(ctx, id, paymentMethod, paymentID, "subscription_entitlement_failed")
			}
			if recordErr == nil {
				return nil
			}
			if recordErr != nil && !errors.Is(recordErr, ErrPaymentNeedsReview) {
				return errors.Join(ErrPaymentNeedsReview, lastErr, recordErr)
			}
			return errors.Join(ErrPaymentNeedsReview, lastErr)
		}
		if lastErr == nil || errors.Is(lastErr, ErrOrderStatusConflict) ||
			errors.Is(lastErr, ErrPaymentNeedsReview) ||
			!strings.Contains(lastErr.Error(), "database is locked") {
			return lastErr
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 15 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func (s *SQLOrderStore) updateOrderStatusOnce(ctx context.Context, id int64, fromStatus, status, paymentMethod, paymentID string, sub *Subscription, fact *PaymentFact) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("order store: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var current Order
	if err := tx.QueryRowContext(ctx,
		`SELECT id, user_id, COALESCE(total_usd, 0), COALESCE(total_stars, 0),
		        COALESCE(payment_method, ''), COALESCE(payment_id, ''),
		        COALESCE(status, 'pending'), payment_state,
		        COALESCE(subscription_product_id, 0), subscription_period_days
		 FROM orders WHERE id = ?`, id).Scan(
		&current.ID, &current.UserID, &current.TotalUSD, &current.TotalStars,
		&current.PaymentMethod, &current.PaymentID, &current.Status, &current.PaymentState,
		&current.SubscriptionProductID, &current.SubscriptionPeriodDays,
	); err == sql.ErrNoRows {
		return ErrOrderStatusConflict
	} else if err != nil {
		return fmt.Errorf("order store: load current order: %w", err)
	}

	if sub != nil {
		if err := validateSubscriptionSettlement(current, paymentMethod, paymentID, *sub); err != nil {
			return err
		}
	}
	if fact != nil {
		validated, err := validatePaymentFact(current, *fact)
		if err != nil || validated.Provider != normalizePaymentProvider(paymentMethod) || validated.ExternalID != paymentID {
			if err != nil {
				return err
			}
			return ErrPaymentReceiptMismatch
		}
		*fact = validated
	}
	if current.Status != fromStatus || (status == OrderStatusPaid && current.PaymentState != PaymentStatePending) {
		// A provider may redeliver an already-settled initial capture after the
		// process crashed in an older split-commit version. Repair only the exact
		// entitlement identity, never replay stock or rewards.
		repairedSubscription := false
		if sub != nil && status == OrderStatusPaid &&
			(current.Status == OrderStatusPaid || current.Status == OrderStatusDelivered) &&
			(current.PaymentState == PaymentStateSettled || current.PaymentState == PaymentStatePartiallyRefunded) &&
			normalizePaymentProvider(current.PaymentMethod) == normalizePaymentProvider(paymentMethod) &&
			current.PaymentID == paymentID {
			if fact != nil {
				var persisted sql.NullTime
				err := tx.QueryRowContext(ctx, `
					SELECT entitlement_expires_at FROM payment_attempts
					WHERE provider = ? AND external_id = ? AND order_id = ? AND status = 'succeeded'`,
					normalizePaymentProvider(paymentMethod), paymentID, current.ID).Scan(&persisted)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("order store: load replay entitlement expiry: %w", err)
				}
				if persisted.Valid {
					if !fact.EntitlementExpiresAt.IsZero() &&
						!fact.EntitlementExpiresAt.Equal(persisted.Time) {
						return ErrPaymentIdentityConflict
					}
					sub.ExpiresAt = persisted.Time
					fact.EntitlementExpiresAt = persisted.Time
				}
			}
			if err := activateSubscriptionTx(ctx, tx, *sub); err != nil {
				return err
			}
			repairedSubscription = true
		}
		if fact != nil && status == OrderStatusPaid && current.PaymentID == paymentID &&
			normalizePaymentProvider(current.PaymentMethod) == normalizePaymentProvider(paymentMethod) {
			var entitlementExpiry *time.Time
			if sub != nil {
				entitlementExpiry = &sub.ExpiresAt
			}
			if _, err := observePayment(ctx, tx, current, paymentMethod, paymentID, fact, entitlementExpiry); err != nil &&
				!errors.Is(err, ErrOrderStatusConflict) {
				return err
			}
		}
		if repairedSubscription {
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("order store: commit subscription replay repair: %w", err)
			}
		}
		return ErrOrderStatusConflict
	}

	next, err := stateForLegacyStatus(status)
	if err != nil {
		return err
	}
	if status == OrderStatusDelivered {
		if current.PaymentState != PaymentStateSettled && current.PaymentState != PaymentStatePartiallyRefunded {
			return ErrOrderStatusConflict
		}
		next.payment = current.PaymentState
	}
	methodExpr, idExpr := current.PaymentMethod, current.PaymentID
	if paymentMethod != "" {
		methodExpr = normalizePaymentProvider(paymentMethod)
	}
	if paymentID != "" {
		idExpr = paymentID
	}
	var attemptID int64
	if status == OrderStatusPaid {
		var entitlementExpiry *time.Time
		if sub != nil {
			entitlementExpiry = &sub.ExpiresAt
		}
		attemptID, err = observePayment(ctx, tx, current, methodExpr, idExpr, fact, entitlementExpiry)
		if err != nil {
			return err
		}
	}

	res, err := tx.ExecContext(ctx,
		`UPDATE orders SET status = ?, order_state = ?, payment_state = ?, fulfillment_state = ?,
		                    payment_method = ?, payment_id = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND status = ? AND (? <> 'paid' OR payment_state = 'pending')`,
		status, next.order, next.payment, next.fulfillment,
		methodExpr, idExpr, id, fromStatus, status)
	if err != nil {
		return fmt.Errorf("order store: update order status: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("order store: update order status rows affected: %w", err)
	}
	if n == 0 {
		return ErrOrderStatusConflict
	}
	if status == OrderStatusPaid {
		if err := markPaymentSettled(ctx, tx, attemptID); err != nil {
			return err
		}
	}
	eventType := "order." + status
	if status == OrderStatusPaid {
		eventType = "payment.settled"
	} else if status == OrderStatusDelivered {
		eventType = "fulfillment.fulfilled"
	}
	if err := appendOrderEvent(ctx, tx, id, eventType, fromStatus, status); err != nil {
		return err
	}

	// If transitioning to paid, decrement stock
	if status == OrderStatusPaid {
		var userID int64
		var promoCode string
		if err := tx.QueryRowContext(ctx,
			`SELECT user_id, COALESCE(promo_code, '') FROM orders WHERE id = ?`, id,
		).Scan(&userID, &promoCode); err != nil {
			return fmt.Errorf("order store: load order payment metadata: %w", err)
		}

		// 1. Get items (use internal method but with tx)
		rows, err := tx.QueryContext(ctx,
			`SELECT product_id, quantity FROM order_items WHERE order_id = ?`, id)
		if err != nil {
			return fmt.Errorf("order store: get items for stock update: %w", err)
		}
		defer rows.Close()

		type item struct {
			productID int64
			quantity  int
		}
		var items []item
		for rows.Next() {
			var i item
			if err := rows.Scan(&i.productID, &i.quantity); err != nil {
				return fmt.Errorf("order store: scan item for stock update: %w", err)
			}
			items = append(items, i)
		}

		// 2. Decrement stock for each item atomically; fail if stock would go negative.
		for _, i := range items {
			res, err := tx.ExecContext(ctx,
				`UPDATE products SET stock = stock - ? WHERE id = ? AND stock >= ?`,
				i.quantity, i.productID, i.quantity)
			if err != nil {
				return fmt.Errorf("order store: decrement stock for product %d: %w", i.productID, err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				return fmt.Errorf("order store: stock rows affected for product %d: %w", i.productID, err)
			}
			if n == 0 {
				return fmt.Errorf("order store: product %d: %w", i.productID, ErrProductOutOfStock)
			}
		}

		if promoCode != "" {
			var promoID int64
			err := tx.QueryRowContext(ctx,
				`SELECT id FROM promo_codes WHERE code = ?`, promoCode,
			).Scan(&promoID)
			if err != nil && err != sql.ErrNoRows {
				return fmt.Errorf("order store: load promo for paid order: %w", err)
			}
			if err == nil {
				res, err := tx.ExecContext(ctx,
					`INSERT OR IGNORE INTO promo_usages (promo_id, user_id, order_id) VALUES (?, ?, ?)`,
					promoID, userID, id,
				)
				if err != nil {
					return fmt.Errorf("order store: record promo usage for paid order: %w", err)
				}
				affected, err := res.RowsAffected()
				if err != nil {
					return fmt.Errorf("order store: promo usage rows affected: %w", err)
				}
				if affected > 0 {
					if _, err := tx.ExecContext(ctx,
						`UPDATE promo_codes SET used_count = used_count + 1 WHERE id = ?`, promoID,
					); err != nil {
						return fmt.Errorf("order store: increment promo used_count for paid order: %w", err)
					}
				}
			}
		}
		if sub != nil {
			if err := activateSubscriptionTx(ctx, tx, *sub); err != nil {
				return err
			}
			if err := appendOrderEvent(ctx, tx, id, "subscription.activated", "", SubStatusActive); err != nil {
				return err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("order store: commit stock update: %w", err)
	}

	return nil
}

// loadOrderItems returns all order items for the given order ID.
func (s *SQLOrderStore) loadOrderItems(ctx context.Context, orderID int64) ([]OrderItem, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, order_id, COALESCE(product_id, 0), COALESCE(product_name, ''), COALESCE(quantity, 0), COALESCE(price_usd, 0)
		 FROM order_items WHERE order_id = ?`, orderID)
	if err != nil {
		return nil, fmt.Errorf("order store: load order items: %w", err)
	}
	defer rows.Close()

	var items []OrderItem
	for rows.Next() {
		var item OrderItem
		if err := rows.Scan(&item.ID, &item.OrderID, &item.ProductID, &item.ProductName, &item.Quantity, &item.PriceUSD); err != nil {
			return nil, fmt.Errorf("order store: scan order item: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// CancelOrder cancels a pending order for the given user. Returns ErrNotFound if
// the order does not exist, belongs to a different user, or is not in pending status.
func (s *SQLOrderStore) CancelOrder(ctx context.Context, orderID, userID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("order store: begin cancel tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx,
		`UPDATE orders SET status = ?, order_state = ?, payment_state = ?, fulfillment_state = ?,
		                   updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND user_id = ? AND status = ? AND payment_state = ?`,
		OrderStatusCancelled, OrderStateCancelled, PaymentStateCancelled,
		FulfillmentStateUnfulfilled, orderID, userID, OrderStatusPending, PaymentStatePending)
	if err != nil {
		return fmt.Errorf("order store: cancel order: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("order store: cancel order rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := appendOrderEvent(ctx, tx, orderID, "order.cancelled", OrderStatusPending, OrderStatusCancelled); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("order store: commit cancel: %w", err)
	}
	return nil
}
