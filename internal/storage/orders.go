package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// SQLOrderStore implements OrderStore using a *sql.DB connection.
type SQLOrderStore struct {
	db *sql.DB
}

// NewSQLOrderStore creates a new SQLOrderStore from the given DB.
func NewSQLOrderStore(d *DB) *SQLOrderStore {
	return &SQLOrderStore{db: d.Conn()}
}

func (s *SQLOrderStore) Conn() *sql.DB {
	return s.db
}

// CreateOrder inserts an order and its items within a transaction. Returns the
// new order ID.
func (s *SQLOrderStore) CreateOrder(ctx context.Context, order *Order, items []OrderItem) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("order store: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO orders (user_id, total_usd, total_stars, payment_method, payment_id, status, discount_pct, promo_code)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		order.UserID, order.TotalUSD, order.TotalStars,
		order.PaymentMethod, order.PaymentID, order.Status,
		order.DiscountPct, order.PromoCode)
	if err != nil {
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
		        COALESCE(status, 'pending'), COALESCE(discount_pct, 0),
		        COALESCE(promo_code, ''), created_at
		 FROM orders WHERE id = ?`, id).
		Scan(&o.ID, &o.UserID, &o.TotalUSD, &o.TotalStars,
			&o.PaymentMethod, &o.PaymentID, &o.Status,
			&o.DiscountPct, &o.PromoCode, &o.CreatedAt)
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

// GetUserOrders returns all orders for the given user sorted by created_at
// descending, each with its items loaded.
func (s *SQLOrderStore) GetUserOrders(ctx context.Context, userID int64) ([]Order, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, COALESCE(total_usd, 0), COALESCE(total_stars, 0),
		        COALESCE(payment_method, ''), COALESCE(payment_id, ''),
		        COALESCE(status, 'pending'), COALESCE(discount_pct, 0),
		        COALESCE(promo_code, ''), created_at
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
			&o.DiscountPct, &o.PromoCode, &o.CreatedAt); err != nil {
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
			        COALESCE(status, 'pending'), COALESCE(discount_pct, 0),
			        COALESCE(promo_code, ''), created_at
			 FROM orders WHERE status = ?
			 ORDER BY created_at DESC`, statusFilter)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, user_id, COALESCE(total_usd, 0), COALESCE(total_stars, 0),
			        COALESCE(payment_method, ''), COALESCE(payment_id, ''),
			        COALESCE(status, 'pending'), COALESCE(discount_pct, 0),
			        COALESCE(promo_code, ''), created_at
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
			&o.DiscountPct, &o.PromoCode, &o.CreatedAt); err != nil {
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
// Returns ErrOrderStatusConflict if the order is not in fromStatus (already
// transitioned or wrong ID), making the operation idempotent and race-safe.
func (s *SQLOrderStore) UpdateOrderStatus(ctx context.Context, id int64, fromStatus, status, paymentMethod, paymentID string) error {
	ex := getExecutor(ctx, s.db)

	res, err := ex.ExecContext(ctx,
		`UPDATE orders SET status = ?, payment_method = ?, payment_id = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND status = ?`,
		status, paymentMethod, paymentID, id, fromStatus)
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
	res, err := s.db.ExecContext(ctx,
		`UPDATE orders SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND user_id = ? AND status = ?`,
		OrderStatusCancelled, orderID, userID, OrderStatusPending)
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
	return nil
}
