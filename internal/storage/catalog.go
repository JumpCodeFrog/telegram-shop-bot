package storage

import (
	"context"
	"database/sql"
	"fmt"
)

type CatalogStore struct {
	db *sql.DB
}

func NewCatalogStore(db *sql.DB) *CatalogStore {
	return &CatalogStore{db: db}
}

func (s *CatalogStore) ListCategories(ctx context.Context) ([]Category, error) {
	query := `SELECT id, name, emoji, custom_emoji_id, sort_order, is_active FROM categories WHERE is_active = 1 ORDER BY sort_order ASC`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}
	defer rows.Close()

	var categories []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name, &c.Emoji, &c.CustomEmojiID, &c.SortOrder, &c.IsActive); err != nil {
			return nil, fmt.Errorf("scan category: %w", err)
		}
		categories = append(categories, c)
	}
	return categories, nil
}

func (s *CatalogStore) GetProductsByCategory(ctx context.Context, categoryID int64, limit, offset int) ([]Product, error) {
	query := `SELECT id, category_id, name, description, photo_url, price_usd, stock, is_digital, is_active, created_at 
	          FROM products WHERE category_id = ? AND is_active = 1 LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, query, categoryID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("get products by category: %w", err)
	}
	defer rows.Close()

	var products []Product
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.CategoryID, &p.Name, &p.Description, &p.PhotoURL, &p.PriceUSD, &p.Stock, &p.IsDigital, &p.IsActive, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan product: %w", err)
		}
		products = append(products, p)
	}
	return products, nil
}

func (s *CatalogStore) SearchProducts(ctx context.Context, queryText string, limit, offset int) ([]Product, error) {
	query := `SELECT id, category_id, name, description, photo_url, price_usd, stock, is_digital, is_active, created_at 
	          FROM products WHERE (name LIKE ? OR description LIKE ?) AND is_active = 1 LIMIT ? OFFSET ?`
	searchPattern := "%" + queryText + "%"
	rows, err := s.db.QueryContext(ctx, query, searchPattern, searchPattern, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("search products: %w", err)
	}
	defer rows.Close()

	var products []Product
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.CategoryID, &p.Name, &p.Description, &p.PhotoURL, &p.PriceUSD, &p.Stock, &p.IsDigital, &p.IsActive, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan product: %w", err)
		}
		products = append(products, p)
	}
	return products, nil
}

func (s *CatalogStore) GetProductByID(ctx context.Context, id int64) (*Product, error) {
	var p Product
	query := `SELECT id, category_id, name, description, photo_url, price_usd, stock, is_digital, digital_content, is_active, created_at FROM products WHERE id = ?`
	err := s.db.QueryRowContext(ctx, query, id).Scan(
		&p.ID, &p.CategoryID, &p.Name, &p.Description, &p.PhotoURL, &p.PriceUSD, &p.Stock, &p.IsDigital, &p.DigitalContent, &p.IsActive, &p.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get product by id: %w", err)
	}
	return &p, nil
}
