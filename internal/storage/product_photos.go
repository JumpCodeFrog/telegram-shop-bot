package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// MaxProductPhotos limits how many photos a product may have.
const MaxProductPhotos = 10

// ErrTooManyPhotos is returned by Add when a product already has
// MaxProductPhotos photos.
var ErrTooManyPhotos = errors.New("storage: product photo limit reached")

// ProductPhoto is an extra product image stored as a Telegram file_id.
// The product's photo_url column remains the cover / fallback image.
type ProductPhoto struct {
	ID        int64
	ProductID int64
	FileID    string
	SortOrder int
}

// SQLProductPhotoStore implements ProductPhotoStore using a *sql.DB connection.
type SQLProductPhotoStore struct {
	db *sql.DB
}

// NewSQLProductPhotoStore creates a new SQLProductPhotoStore from the given DB.
func NewSQLProductPhotoStore(d *DB) *SQLProductPhotoStore {
	return &SQLProductPhotoStore{db: d.Conn()}
}

// Add appends a photo to a product with the next sort_order. Returns
// ErrTooManyPhotos once the product already has MaxProductPhotos photos.
// The count check and insert are a single statement, so concurrent adds
// cannot overshoot the limit.
func (s *SQLProductPhotoStore) Add(ctx context.Context, productID int64, fileID string) error {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO product_photos (product_id, file_id, sort_order)
		 SELECT ?, ?, COALESCE(MAX(sort_order) + 1, 0)
		 FROM product_photos WHERE product_id = ?
		 HAVING COUNT(*) < ?`,
		productID, fileID, productID, MaxProductPhotos)
	if err != nil {
		return fmt.Errorf("product photo store: add: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("product photo store: add rows affected: %w", err)
	}
	if n == 0 {
		return ErrTooManyPhotos
	}
	return nil
}

// List returns all photos of a product ordered by sort_order.
func (s *SQLProductPhotoStore) List(ctx context.Context, productID int64) ([]ProductPhoto, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, product_id, file_id, sort_order
		 FROM product_photos WHERE product_id = ?
		 ORDER BY sort_order ASC, id ASC`, productID)
	if err != nil {
		return nil, fmt.Errorf("product photo store: list: %w", err)
	}
	defer rows.Close()

	var photos []ProductPhoto
	for rows.Next() {
		var p ProductPhoto
		if err := rows.Scan(&p.ID, &p.ProductID, &p.FileID, &p.SortOrder); err != nil {
			return nil, fmt.Errorf("product photo store: scan photo: %w", err)
		}
		photos = append(photos, p)
	}
	return photos, rows.Err()
}

// Delete removes a photo by ID. Deleting a non-existent photo is a no-op.
func (s *SQLProductPhotoStore) Delete(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM product_photos WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("product photo store: delete: %w", err)
	}
	return nil
}
