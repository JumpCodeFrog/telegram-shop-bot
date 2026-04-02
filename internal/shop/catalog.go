package shop

import (
	"context"

	"shop_bot/internal/service"
	"shop_bot/internal/storage"
)

// CatalogService provides business logic for browsing the product catalog.
type CatalogService struct {
	products storage.ProductStore
	exchange *service.ExchangeService
}

// NewCatalogService creates a new CatalogService backed by the given ProductStore and ExchangeService.
func NewCatalogService(ps storage.ProductStore, exchange ...*service.ExchangeService) *CatalogService {
	var ex *service.ExchangeService
	if len(exchange) > 0 {
		ex = exchange[0]
	}
	return &CatalogService{products: ps, exchange: ex}
}

// applyExchangeRate updates the PriceStars field based on real-time rates.
func (s *CatalogService) applyExchangeRate(p *storage.Product) {
	if s.exchange != nil {
		p.PriceStars = s.exchange.ConvertUSDToStars(p.PriceUSD)
	}
}

// ListCategories returns all available categories.
func (s *CatalogService) ListCategories(ctx context.Context) ([]storage.Category, error) {
	return s.products.GetCategories(ctx)
}

// ListProducts returns products for the given category that are currently active and in stock.
func (s *CatalogService) ListProducts(ctx context.Context, categoryID int64) ([]storage.Product, error) {
	all, err := s.products.GetProductsByCategory(ctx, categoryID)
	if err != nil {
		return nil, err
	}

	isActive := make([]storage.Product, 0, len(all))
	for _, p := range all {
		if p.IsActive && p.Stock > 0 {
			s.applyExchangeRate(&p)
			isActive = append(isActive, p)
		}
	}
	return isActive, nil
}

// ListProductsPaged returns in-stock products for a category with pagination.
func (s *CatalogService) ListProductsPaged(ctx context.Context, categoryID int64, limit, offset int) ([]storage.Product, int, error) {
	prods, total, err := s.products.GetProductsByCategoryPaged(ctx, categoryID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	for i := range prods {
		s.applyExchangeRate(&prods[i])
	}
	return prods, total, nil
}

// GetProduct returns a single product by ID.
func (s *CatalogService) GetProduct(ctx context.Context, id int64) (*storage.Product, error) {
	p, err := s.products.GetProduct(ctx, id)
	if err != nil {
		return nil, err
	}
	s.applyExchangeRate(p)
	return p, nil
}

// CreateCategory creates a new category and returns its ID.
func (s *CatalogService) CreateCategory(ctx context.Context, cat *storage.Category) (int64, error) {
	return s.products.CreateCategory(ctx, cat)
}

// UpdateCategory updates the name and emoji of an existing category.
func (s *CatalogService) UpdateCategory(ctx context.Context, cat *storage.Category) error {
	return s.products.UpdateCategory(ctx, cat)
}

// DeleteCategory deletes a category by ID. Returns an error if products exist.
func (s *CatalogService) DeleteCategory(ctx context.Context, id int64) error {
	return s.products.DeleteCategory(ctx, id)
}

// GetCategory returns a single category by ID.
func (s *CatalogService) GetCategory(ctx context.Context, id int64) (*storage.Category, error) {
	return s.products.GetCategory(ctx, id)
}
