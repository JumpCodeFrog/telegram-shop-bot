package storage

import (
	"context"
	"database/sql"
)

type WishlistStore struct {
	db *sql.DB
}

func NewWishlistStore(db *sql.DB) *WishlistStore {
	return &WishlistStore{db: db}
}

func (s *WishlistStore) Add(ctx context.Context, userID, productID int64, price float64, stock int) error {
	query := `INSERT OR IGNORE INTO wishlist (user_id, product_id, price_at_added, stock_at_added) VALUES (?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, query, userID, productID, price, stock)
	return err
}

func (s *WishlistStore) Remove(ctx context.Context, userID, productID int64) error {
	query := `DELETE FROM wishlist WHERE user_id = ? AND product_id = ?`
	_, err := s.db.ExecContext(ctx, query, userID, productID)
	return err
}

func (s *WishlistStore) IsInWishlist(ctx context.Context, userID, productID int64) (bool, error) {
	var exists bool
	query := `SELECT EXISTS(SELECT 1 FROM wishlist WHERE user_id = ? AND product_id = ?)`
	err := s.db.QueryRowContext(ctx, query, userID, productID).Scan(&exists)
	return exists, err
}

// WishlistEntry combines wishlist metadata with the current product state.
type WishlistEntry struct {
	UserID       int64
	ProductID    int64
	PriceAtAdded float64
	StockAtAdded int
	LanguageCode string
	Product      Product

	PriceDropNotifiedAt    sql.NullTime
	BackInStockNotifiedAt  sql.NullTime
}

// GetAllWithProducts returns all wishlist entries joined with current product data.
// Used by the WishlistWatcherWorker to detect price drops and stock returns.
func (s *WishlistStore) GetAllWithProducts(ctx context.Context) ([]WishlistEntry, error) {
	query := `
		SELECT w.user_id, w.product_id, w.price_at_added, w.stock_at_added,
		       COALESCE(u.language_code, 'en'),
		       p.id, p.category_id, p.name, p.description, p.photo_url,
		       p.price_usd, p.price_stars, p.stock, p.is_digital, p.digital_content,
		       p.is_active, p.created_at,
		       w.price_drop_notified_at, w.back_in_stock_notified_at
		FROM wishlist w
		JOIN products p ON w.product_id = p.id
		LEFT JOIN users u ON w.user_id = u.telegram_id
		WHERE p.is_active = 1
	`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []WishlistEntry
	for rows.Next() {
		var e WishlistEntry
		if err := rows.Scan(
			&e.UserID, &e.ProductID, &e.PriceAtAdded, &e.StockAtAdded,
			&e.LanguageCode,
			&e.Product.ID, &e.Product.CategoryID, &e.Product.Name, &e.Product.Description, &e.Product.PhotoURL,
			&e.Product.PriceUSD, &e.Product.PriceStars, &e.Product.Stock, &e.Product.IsDigital, &e.Product.DigitalContent,
			&e.Product.IsActive, &e.Product.CreatedAt,
			&e.PriceDropNotifiedAt, &e.BackInStockNotifiedAt,
		); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// MarkPriceDropNotified records that a price-drop notification was sent for the
// given wishlist entry. Prevents repeated notifications while price stays down.
func (s *WishlistStore) MarkPriceDropNotified(ctx context.Context, userID, productID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE wishlist SET price_drop_notified_at = CURRENT_TIMESTAMP WHERE user_id = ? AND product_id = ?`,
		userID, productID)
	return err
}

// ClearPriceDropNotified clears the notification flag when the price rises back.
// Called so that a future price drop triggers a fresh notification.
func (s *WishlistStore) ClearPriceDropNotified(ctx context.Context, userID, productID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE wishlist SET price_drop_notified_at = NULL WHERE user_id = ? AND product_id = ?`,
		userID, productID)
	return err
}

// MarkBackInStockNotified records that a back-in-stock notification was sent.
func (s *WishlistStore) MarkBackInStockNotified(ctx context.Context, userID, productID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE wishlist SET back_in_stock_notified_at = CURRENT_TIMESTAMP WHERE user_id = ? AND product_id = ?`,
		userID, productID)
	return err
}

// ClearBackInStockNotified clears the back-in-stock flag when item goes out of stock again.
func (s *WishlistStore) ClearBackInStockNotified(ctx context.Context, userID, productID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE wishlist SET back_in_stock_notified_at = NULL WHERE user_id = ? AND product_id = ?`,
		userID, productID)
	return err
}

// GetUserWishlistIDs returns the set of product IDs in a user's wishlist.
// Used by the catalog handler to render ♥ icons.
func (s *WishlistStore) GetUserWishlistIDs(ctx context.Context, userID int64) (map[int64]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT product_id FROM wishlist WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make(map[int64]struct{})
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = struct{}{}
	}
	return ids, rows.Err()
}

func (s *WishlistStore) GetUserWishlist(ctx context.Context, userID int64) ([]Product, error) {
	query := `
		SELECT p.id, p.category_id, p.name, p.description, p.photo_url, p.price_usd, p.stock, p.is_digital, p.is_active, p.created_at 
		FROM wishlist w
		JOIN products p ON w.product_id = p.id
		WHERE w.user_id = ? AND p.is_active = 1
	`
	rows, err := s.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var products []Product
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.CategoryID, &p.Name, &p.Description, &p.PhotoURL, &p.PriceUSD, &p.Stock, &p.IsDigital, &p.IsActive, &p.CreatedAt); err != nil {
			return nil, err
		}
		products = append(products, p)
	}
	return products, nil
}
