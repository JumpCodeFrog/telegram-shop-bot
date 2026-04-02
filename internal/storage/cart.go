package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type SQLCartStore struct {
	db *sql.DB
}

// NewSQLCartStore keeps backward compatibility with older call sites that pass
// the storage DB wrapper instead of a raw *sql.DB.
func NewSQLCartStore(db *DB) *SQLCartStore {
	return &SQLCartStore{db: db.Conn()}
}

func NewCartStore(db *sql.DB) *SQLCartStore {
	return &SQLCartStore{db: db}
}

func (s *SQLCartStore) AddItem(ctx context.Context, userID, productID int64) error {
	query := `
		INSERT INTO cart_items (user_id, product_id, quantity)
		VALUES (?, ?, 1)
		ON CONFLICT(user_id, product_id) DO UPDATE SET
			quantity = cart_items.quantity + 1
	`
	_, err := s.db.ExecContext(ctx, query, userID, productID)
	return err
}

func (s *SQLCartStore) UpdateQuantity(ctx context.Context, userID, productID int64, quantity int) error {
	if quantity <= 0 {
		return s.RemoveItem(ctx, userID, productID)
	}
	query := `UPDATE cart_items SET quantity = ? WHERE user_id = ? AND product_id = ?`
	_, err := s.db.ExecContext(ctx, query, quantity, userID, productID)
	return err
}

func (s *SQLCartStore) RemoveItem(ctx context.Context, userID, productID int64) error {
	query := `DELETE FROM cart_items WHERE user_id = ? AND product_id = ?`
	_, err := s.db.ExecContext(ctx, query, userID, productID)
	return err
}

func (s *SQLCartStore) ClearCart(ctx context.Context, userID int64) error {
	query := `DELETE FROM cart_items WHERE user_id = ?`
	_, err := s.db.ExecContext(ctx, query, userID)
	return err
}

func (s *SQLCartStore) GetAbandonedCarts(ctx context.Context, olderThan time.Duration) ([]int64, error) {
	query := `
		SELECT DISTINCT user_id FROM cart_items 
		WHERE added_at < datetime('now', '-' || ? || ' seconds')
		AND recovery_sent_at IS NULL
	`
	rows, err := s.db.QueryContext(ctx, query, olderThan.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var userIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		userIDs = append(userIDs, id)
	}
	return userIDs, nil
}

func (s *SQLCartStore) MarkRecoverySent(ctx context.Context, userID int64) error {
	query := `UPDATE cart_items SET recovery_sent_at = CURRENT_TIMESTAMP WHERE user_id = ?`
	_, err := s.db.ExecContext(ctx, query, userID)
	return err
}

func (s *SQLCartStore) GetItems(ctx context.Context, userID int64) ([]CartItem, error) {
	query := `
		SELECT c.id, c.user_id, c.product_id, c.quantity, c.added_at, p.name, p.price_usd
		FROM cart_items c
		JOIN products p ON c.product_id = p.id
		WHERE c.user_id = ?
	`
	rows, err := s.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("get cart items: %w", err)
	}
	defer rows.Close()

	var items []CartItem
	for rows.Next() {
		var i CartItem
		if err := rows.Scan(&i.ID, &i.UserID, &i.ProductID, &i.Quantity, &i.AddedAt, &i.ProductName, &i.ProductPrice); err != nil {
			return nil, fmt.Errorf("scan cart item: %w", err)
		}
		items = append(items, i)
	}
	return items, nil
}
