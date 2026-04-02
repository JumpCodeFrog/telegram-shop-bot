package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// SQLProductStore implements ProductStore using a *sql.DB connection.
type SQLProductStore struct {
	db *sql.DB
}

// NewSQLProductStore creates a new SQLProductStore from the given DB.
func NewSQLProductStore(d *DB) *SQLProductStore {
	return &SQLProductStore{db: d.Conn()}
}

// GetCategories returns all active categories.
func (s *SQLProductStore) GetCategories(ctx context.Context) ([]Category, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, name, COALESCE(emoji, ''), COALESCE(custom_emoji_id, ''), sort_order, is_active FROM categories WHERE is_active = 1 ORDER BY sort_order ASC")
	if err != nil {
		return nil, fmt.Errorf("product store: get categories: %w", err)
	}
	defer rows.Close()

	var cats []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name, &c.Emoji, &c.CustomEmojiID, &c.SortOrder, &c.IsActive); err != nil {
			return nil, fmt.Errorf("product store: scan category: %w", err)
		}
		cats = append(cats, c)
	}
	return cats, rows.Err()
}

// GetProductsByCategory returns all active products for the given category.
func (s *SQLProductStore) GetProductsByCategory(ctx context.Context, categoryID int64) ([]Product, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, category_id, name, COALESCE(description, ''), COALESCE(photo_url, ''),
		        price_usd, COALESCE(price_stars, 0), stock, is_digital, COALESCE(digital_content, ''), is_active, created_at
		 FROM products WHERE category_id = ? AND is_active = 1`, categoryID)
	if err != nil {
		return nil, fmt.Errorf("product store: get products by category: %w", err)
	}
	defer rows.Close()

	var products []Product
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.CategoryID, &p.Name, &p.Description, &p.PhotoURL,
			&p.PriceUSD, &p.PriceStars, &p.Stock, &p.IsDigital, &p.DigitalContent, &p.IsActive, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("product store: scan product: %w", err)
		}
		products = append(products, p)
	}
	return products, rows.Err()
}

// GetProductsByCategoryPaged returns in-stock and active products for a category with
// pagination. Returns the products, total count, and any error.
func (s *SQLProductStore) GetProductsByCategoryPaged(ctx context.Context, categoryID int64, limit, offset int) ([]Product, int, error) {
	var total int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM products WHERE category_id = ? AND is_active = 1 AND stock > 0", categoryID,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("product store: count paged products: %w", err)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, category_id, name, COALESCE(description, ''), COALESCE(photo_url, ''),
		        price_usd, COALESCE(price_stars, 0), stock, is_digital, COALESCE(digital_content, ''), is_active, created_at
		 FROM products WHERE category_id = ? AND is_active = 1 AND stock > 0
		 ORDER BY id LIMIT ? OFFSET ?`, categoryID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("product store: get paged products: %w", err)
	}
	defer rows.Close()

	var products []Product
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.CategoryID, &p.Name, &p.Description, &p.PhotoURL,
			&p.PriceUSD, &p.PriceStars, &p.Stock, &p.IsDigital, &p.DigitalContent, &p.IsActive, &p.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("product store: scan paged product: %w", err)
		}
		products = append(products, p)
	}
	return products, total, rows.Err()
}

// GetProduct returns a single product by ID. Returns ErrNotFound if the product
// does not exist.
func (s *SQLProductStore) GetProduct(ctx context.Context, id int64) (*Product, error) {
	var p Product
	err := s.db.QueryRowContext(ctx,
		`SELECT id, category_id, name, COALESCE(description, ''), COALESCE(photo_url, ''),
		        price_usd, COALESCE(price_stars, 0), stock, is_digital, COALESCE(digital_content, ''), is_active, created_at
		 FROM products WHERE id = ?`, id).
		Scan(&p.ID, &p.CategoryID, &p.Name, &p.Description, &p.PhotoURL,
			&p.PriceUSD, &p.PriceStars, &p.Stock, &p.IsDigital, &p.DigitalContent, &p.IsActive, &p.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("product store: get product: %w", err)
	}
	return &p, nil
}

// CreateProduct inserts a new product and returns its ID.
func (s *SQLProductStore) CreateProduct(ctx context.Context, p *Product) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO products (category_id, name, description, photo_url, price_usd, price_stars, stock, is_digital, digital_content, is_active)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.CategoryID, p.Name, p.Description, p.PhotoURL, p.PriceUSD, p.PriceStars, p.Stock, p.IsDigital, p.DigitalContent, p.IsActive)
	if err != nil {
		return 0, fmt.Errorf("product store: create product: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("product store: last insert id: %w", err)
	}
	return id, nil
}

// UpdateProduct updates all fields of an existing product.
func (s *SQLProductStore) UpdateProduct(ctx context.Context, p *Product) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE products SET category_id = ?, name = ?, description = ?, photo_url = ?,
		        price_usd = ?, price_stars = ?, stock = ?, is_digital = ?, digital_content = ?, is_active = ?
		 WHERE id = ?`,
		p.CategoryID, p.Name, p.Description, p.PhotoURL, p.PriceUSD, p.PriceStars, p.Stock, p.IsDigital, p.DigitalContent, p.IsActive, p.ID)
	if err != nil {
		return fmt.Errorf("product store: update product: %w", err)
	}
	return nil
}

// DeleteProduct removes a product by ID.
func (s *SQLProductStore) DeleteProduct(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM products WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("product store: delete product: %w", err)
	}
	return nil
}

// CreateCategory inserts a new category and returns its ID.
func (s *SQLProductStore) CreateCategory(ctx context.Context, cat *Category) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO categories (name, emoji, custom_emoji_id, sort_order, is_active) VALUES (?, ?, ?, ?, ?)",
		cat.Name, cat.Emoji, cat.CustomEmojiID, cat.SortOrder, cat.IsActive)
	if err != nil {
		return 0, fmt.Errorf("product store: create category: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("product store: last insert id: %w", err)
	}
	return id, nil
}

// UpdateCategory updates an existing category.
func (s *SQLProductStore) UpdateCategory(ctx context.Context, cat *Category) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE categories SET name = ?, emoji = ?, custom_emoji_id = ?, sort_order = ?, is_active = ? WHERE id = ?",
		cat.Name, cat.Emoji, cat.CustomEmojiID, cat.SortOrder, cat.IsActive, cat.ID)
	if err != nil {
		return fmt.Errorf("product store: update category: %w", err)
	}
	return nil
}

// DeleteCategory removes a category by ID. Returns an error if the category
// still has products assigned to it.
func (s *SQLProductStore) DeleteCategory(ctx context.Context, id int64) error {
	var count int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM products WHERE category_id = ?", id,
	).Scan(&count); err != nil {
		return fmt.Errorf("product store: check products in category: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("product store: category has %d products, delete or reassign them first", count)
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM categories WHERE id = ?", id); err != nil {
		return fmt.Errorf("product store: delete category: %w", err)
	}
	return nil
}

// SearchProducts returns active and in-stock products matching the query in name or description.
func (s *SQLProductStore) SearchProducts(ctx context.Context, query string) ([]Product, error) {
	pattern := "%" + query + "%"
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, category_id, name, COALESCE(description, ''), COALESCE(photo_url, ''),
		        price_usd, COALESCE(price_stars, 0), stock, is_digital, COALESCE(digital_content, ''), is_active, created_at
		 FROM products
		 WHERE is_active = 1 AND stock > 0 AND (name LIKE ? OR description LIKE ?)
		 ORDER BY name`,
		pattern, pattern)
	if err != nil {
		return nil, fmt.Errorf("product store: search products: %w", err)
	}
	defer rows.Close()

	var products []Product
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.CategoryID, &p.Name, &p.Description, &p.PhotoURL,
			&p.PriceUSD, &p.PriceStars, &p.Stock, &p.IsDigital, &p.DigitalContent, &p.IsActive, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("product store: scan search product: %w", err)
		}
		products = append(products, p)
	}
	return products, rows.Err()
}

// GetCategory returns a single category by ID. Returns ErrNotFound if it does
// not exist.
func (s *SQLProductStore) GetCategory(ctx context.Context, id int64) (*Category, error) {
	var c Category
	err := s.db.QueryRowContext(ctx,
		"SELECT id, name, COALESCE(emoji, ''), COALESCE(custom_emoji_id, ''), sort_order, is_active FROM categories WHERE id = ?", id,
	).Scan(&c.ID, &c.Name, &c.Emoji, &c.CustomEmojiID, &c.SortOrder, &c.IsActive)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("product store: get category: %w", err)
	}
	return &c, nil
}
