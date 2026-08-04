package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Review is a buyer's rating (1..5) with optional text for a product.
// One review per (product, user); a repeated review replaces the old one.
type Review struct {
	ID        int64
	ProductID int64
	UserID    int64
	OrderID   int64
	Rating    int
	Text      string
	CreatedAt time.Time
}

// SQLReviewStore implements ReviewStore using a *sql.DB connection.
type SQLReviewStore struct {
	db *sql.DB
}

// NewSQLReviewStore creates a new SQLReviewStore from the given DB.
func NewSQLReviewStore(d *DB) *SQLReviewStore {
	return &SQLReviewStore{db: d.Conn()}
}

// Upsert inserts a review or, on (product_id, user_id) conflict, replaces the
func (s *SQLReviewStore) Upsert(ctx context.Context, r Review) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO reviews (product_id, user_id, order_id, rating, text)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(product_id, user_id) DO UPDATE SET rating = excluded.rating, text = excluded.text`,
		r.ProductID, r.UserID, nullableID(r.OrderID), r.Rating, r.Text)
	if err != nil {
		return fmt.Errorf("review store: upsert: %w", err)
	}
	return nil
}

// ProductRating returns the average rating and review count for a product.
// A product without reviews yields (0, 0, nil).
func (s *SQLReviewStore) ProductRating(ctx context.Context, productID int64) (avg float64, count int64, err error) {
	err = s.db.QueryRowContext(ctx,
		"SELECT COALESCE(AVG(rating), 0), COUNT(*) FROM reviews WHERE product_id = ?", productID,
	).Scan(&avg, &count)
	if err != nil {
		return 0, 0, fmt.Errorf("review store: product rating: %w", err)
	}
	return avg, count, nil
}

// ListByProduct returns the newest reviews for a product, up to limit.
func (s *SQLReviewStore) ListByProduct(ctx context.Context, productID int64, limit int) ([]Review, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, product_id, user_id, COALESCE(order_id, 0), rating, COALESCE(text, ''), created_at
		 FROM reviews WHERE product_id = ?
		 ORDER BY created_at DESC, id DESC LIMIT ?`, productID, limit)
	if err != nil {
		return nil, fmt.Errorf("review store: list by product: %w", err)
	}
	return scanReviews(rows)
}

// ListRecent returns the newest reviews across all products, up to limit.
// Used by the admin /reviews command.
func (s *SQLReviewStore) ListRecent(ctx context.Context, limit int) ([]Review, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, product_id, user_id, COALESCE(order_id, 0), rating, COALESCE(text, ''), created_at
		 FROM reviews ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("review store: list recent: %w", err)
	}
	return scanReviews(rows)
}

// Delete removes a review by ID (admin moderation). Deleting a non-existent
// review is a no-op.
func (s *SQLReviewStore) Delete(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM reviews WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("review store: delete: %w", err)
	}
	return nil
}

// scanReviews drains rows into a slice, closing rows in all paths.
func scanReviews(rows *sql.Rows) ([]Review, error) {
	defer rows.Close()

	var reviews []Review
	for rows.Next() {
		var r Review
		if err := rows.Scan(&r.ID, &r.ProductID, &r.UserID, &r.OrderID, &r.Rating, &r.Text, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("review store: scan review: %w", err)
		}
		reviews = append(reviews, r)
	}
	return reviews, rows.Err()
}


// nullableID maps a zero ID to NULL so that optional foreign keys
// (e.g. order_id) do not trip foreign_keys enforcement.
func nullableID(id int64) sql.NullInt64 {
	return sql.NullInt64{Int64: id, Valid: id != 0}
}