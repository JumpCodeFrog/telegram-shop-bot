package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Subscription status values.
const (
	SubStatusActive   = "active"
	SubStatusCanceled = "canceled"
	SubStatusExpired  = "expired"
)

// Subscription is a Stars recurring subscription to a product.
// RemindedAt marks that the "expiring soon" reminder was already sent for the
// current period; renewal clears it.
type Subscription struct {
	ID         int64
	UserID     int64
	ProductID  int64
	OrderID    int64
	ChargeID   string
	Status     string
	ExpiresAt  time.Time
	RemindedAt sql.NullTime
}

// SQLSubscriptionStore implements SubscriptionStore using a *sql.DB connection.
type SQLSubscriptionStore struct {
	db *sql.DB
}

// NewSQLSubscriptionStore creates a new SQLSubscriptionStore from the given DB.
func NewSQLSubscriptionStore(d *DB) *SQLSubscriptionStore {
	return &SQLSubscriptionStore{db: d.Conn()}
}

// Upsert inserts a subscription or, on (user_id, product_id) conflict, renews
// it: moves expires_at forward, refreshes order/charge/status, and clears
// reminded_at so the next period gets its own reminder.
func (s *SQLSubscriptionStore) Upsert(ctx context.Context, sub Subscription) error {
	status := sub.Status
	if status == "" {
		status = SubStatusActive
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO subscriptions (user_id, product_id, order_id, telegram_charge_id, status, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, product_id) DO UPDATE SET
		     order_id           = excluded.order_id,
		     telegram_charge_id = excluded.telegram_charge_id,
		     status             = excluded.status,
		     expires_at         = excluded.expires_at,
		     reminded_at        = NULL,
		     updated_at         = CURRENT_TIMESTAMP`,
		sub.UserID, sub.ProductID, nullableID(sub.OrderID), sub.ChargeID, status, sub.ExpiresAt.UTC())
	if err != nil {
		return fmt.Errorf("subscription store: upsert: %w", err)
	}
	return nil
}

// ListActiveByUser returns the user's active subscriptions ordered by
// expiry date (soonest first).
func (s *SQLSubscriptionStore) ListActiveByUser(ctx context.Context, userID int64) ([]Subscription, error) {
	rows, err := s.db.QueryContext(ctx,
		subscriptionSelect+` WHERE user_id = ? AND status = ? ORDER BY expires_at ASC`,
		userID, SubStatusActive)
	if err != nil {
		return nil, fmt.Errorf("subscription store: list active by user: %w", err)
	}
	return scanSubscriptions(rows)
}

// SetStatusByCharge updates the status of the subscription identified by its
// Telegram payment charge ID. Returns ErrNotFound if no subscription matches.
func (s *SQLSubscriptionStore) SetStatusByCharge(ctx context.Context, chargeID, status string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE subscriptions SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE telegram_charge_id = ?`,
		status, chargeID)
	if err != nil {
		return fmt.Errorf("subscription store: set status by charge: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("subscription store: set status rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DueForReminder returns active, not-yet-reminded subscriptions expiring
// within the given duration from now (already-expired ones are excluded —
// they belong to ExpireOverdue).
func (s *SQLSubscriptionStore) DueForReminder(ctx context.Context, within time.Duration) ([]Subscription, error) {
	now := time.Now().UTC()
	rows, err := s.db.QueryContext(ctx,
		subscriptionSelect+`
		 WHERE status = ? AND reminded_at IS NULL AND expires_at > ? AND expires_at <= ?
		 ORDER BY expires_at ASC`,
		SubStatusActive, now, now.Add(within))
	if err != nil {
		return nil, fmt.Errorf("subscription store: due for reminder: %w", err)
	}
	return scanSubscriptions(rows)
}

// MarkReminded records that the expiry reminder was sent for a subscription,
// so the worker never reminds twice for the same period.
func (s *SQLSubscriptionStore) MarkReminded(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE subscriptions SET reminded_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		id)
	if err != nil {
		return fmt.Errorf("subscription store: mark reminded: %w", err)
	}
	return nil
}

// ExpireOverdue marks active subscriptions whose expires_at has passed as
// expired and returns how many rows were updated.
func (s *SQLSubscriptionStore) ExpireOverdue(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE subscriptions SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE status = ? AND expires_at <= ?`,
		SubStatusExpired, SubStatusActive, time.Now().UTC())
	if err != nil {
		return 0, fmt.Errorf("subscription store: expire overdue: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("subscription store: expire overdue rows affected: %w", err)
	}
	return n, nil
}

const subscriptionSelect = `SELECT id, user_id, product_id, COALESCE(order_id, 0), telegram_charge_id, status, expires_at, reminded_at
	 FROM subscriptions`

// scanSubscriptions drains rows into a slice, closing rows in all paths.
func scanSubscriptions(rows *sql.Rows) ([]Subscription, error) {
	defer rows.Close()

	var subs []Subscription
	for rows.Next() {
		var sub Subscription
		if err := rows.Scan(&sub.ID, &sub.UserID, &sub.ProductID, &sub.OrderID,
			&sub.ChargeID, &sub.Status, &sub.ExpiresAt, &sub.RemindedAt); err != nil {
			return nil, fmt.Errorf("subscription store: scan subscription: %w", err)
		}
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}
