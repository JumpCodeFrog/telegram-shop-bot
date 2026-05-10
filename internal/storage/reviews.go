package storage

import (
	"context"
	"database/sql"
	"fmt"
)

type SQLReviewStore struct {
	db *sql.DB
}

func NewReviewStore(db *sql.DB) *SQLReviewStore {
	return &SQLReviewStore{db: db}
}

func (s *SQLReviewStore) CreateReview(ctx context.Context, r *Review) error {
	query := `INSERT INTO reviews (product_id, user_id, rating, text) VALUES (?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, query, r.ProductID, r.UserID, r.Rating, r.Text)
	if err != nil {
		return fmt.Errorf("review store: create review: %w", err)
	}
	return nil
}

func (s *SQLReviewStore) GetReviewsByProduct(ctx context.Context, productID int64) ([]Review, error) {
	query := `
		SELECT r.id, r.product_id, r.user_id, r.rating, COALESCE(r.text, ''), r.created_at, u.first_name
		FROM reviews r
		JOIN users u ON r.user_id = u.id
		WHERE r.product_id = ?
		ORDER BY r.created_at DESC
	`
	rows, err := s.db.QueryContext(ctx, query, productID)
	if err != nil {
		return nil, fmt.Errorf("review store: get reviews by product: %w", err)
	}
	defer rows.Close()

	var reviews []Review
	for rows.Next() {
		var r Review
		if err := rows.Scan(&r.ID, &r.ProductID, &r.UserID, &r.Rating, &r.Text, &r.CreatedAt, &r.UserFirstName); err != nil {
			return nil, err
		}
		reviews = append(reviews, r)
	}
	return reviews, rows.Err()
}

func (s *SQLReviewStore) GetAverageRating(ctx context.Context, productID int64) (float64, int, error) {
	var avg sql.NullFloat64
	var count int
	query := `SELECT AVG(rating), COUNT(*) FROM reviews WHERE product_id = ?`
	err := s.db.QueryRowContext(ctx, query, productID).Scan(&avg, &count)
	if err != nil {
		return 0, 0, fmt.Errorf("review store: get average rating: %w", err)
	}
	return avg.Float64, count, nil
}

func (s *SQLReviewStore) HasUserOrderedProduct(ctx context.Context, userID, productID int64) (bool, error) {
	var count int
	query := `
		SELECT COUNT(*) FROM order_items oi
		JOIN orders o ON oi.order_id = o.id
		WHERE o.user_id = ? AND oi.product_id = ? AND o.status = 'paid'
	`
	err := s.db.QueryRowContext(ctx, query, userID, productID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("review store: check user ordered product: %w", err)
	}
	return count > 0, nil
}
