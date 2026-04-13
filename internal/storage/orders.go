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

// CreateOrder inserts an order and its items within a transaction. Returns the
// new order ID.
func (s *SQLOrderStore) CreateOrder(ctx context.Context, order *Order, items []OrderItem) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("order store: begin tx: %w", err)
	}
	defer tx.Rollback()

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
// If transitioning to "paid", it also decrements the stock of all products in the order.
// Returns ErrOrderStatusConflict if the order is not in fromStatus (already
// transitioned or wrong ID), making the operation idempotent and race-safe.
func (s *SQLOrderStore) UpdateOrderStatus(ctx context.Context, id int64, fromStatus, status, paymentMethod, paymentID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("order store: begin tx: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
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
