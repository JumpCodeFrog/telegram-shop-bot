package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type CachedProductStore struct {
	base  ProductStore
	redis *redis.Client
	ttl   time.Duration
}

func NewCachedProductStore(base ProductStore, redis *redis.Client, ttl time.Duration) *CachedProductStore {
	return &CachedProductStore{
		base:  base,
		redis: redis,
		ttl:   ttl,
	}
}

func (s *CachedProductStore) GetCategories(ctx context.Context) ([]Category, error) {
	if s.redis == nil {
		return s.base.GetCategories(ctx)
	}
	key := "catalog:categories"
	val, err := s.redis.Get(ctx, key).Result()
	if err == nil {
		var cats []Category
		if json.Unmarshal([]byte(val), &cats) == nil {
			return cats, nil
		}
	}

	cats, err := s.base.GetCategories(ctx)
	if err == nil {
		data, _ := json.Marshal(cats)
		s.redis.Set(ctx, key, data, s.ttl)
	}
	return cats, err
}

func (s *CachedProductStore) GetProductsByCategory(ctx context.Context, categoryID int64) ([]Product, error) {
	if s.redis == nil {
		return s.base.GetProductsByCategory(ctx, categoryID)
	}
	key := fmt.Sprintf("catalog:products:%d", categoryID)
	val, err := s.redis.Get(ctx, key).Result()
	if err == nil {
		var products []Product
		if json.Unmarshal([]byte(val), &products) == nil {
			return products, nil
		}
	}

	products, err := s.base.GetProductsByCategory(ctx, categoryID)
	if err == nil {
		data, _ := json.Marshal(products)
		s.redis.Set(ctx, key, data, s.ttl)
	}
	return products, err
}

func (s *CachedProductStore) GetProductsByCategoryPaged(ctx context.Context, categoryID int64, limit, offset int) ([]Product, int, error) {
	// For simplicity, we don't cache paged results yet, or we can use a more complex key
	return s.base.GetProductsByCategoryPaged(ctx, categoryID, limit, offset)
}

func (s *CachedProductStore) GetProduct(ctx context.Context, id int64) (*Product, error) {
	if s.redis == nil {
		return s.base.GetProduct(ctx, id)
	}
	key := fmt.Sprintf("catalog:product:%d", id)
	val, err := s.redis.Get(ctx, key).Result()
	if err == nil {
		var p Product
		if json.Unmarshal([]byte(val), &p) == nil {
			return &p, nil
		}
	}

	p, err := s.base.GetProduct(ctx, id)
	if err == nil {
		data, _ := json.Marshal(p)
		s.redis.Set(ctx, key, data, s.ttl)
	}
	return p, err
}

// Write operations should invalidate cache

func (s *CachedProductStore) invalidateCategoryCache(ctx context.Context, catID int64) {
	if s.redis == nil {
		return
	}
	s.redis.Del(ctx, "catalog:categories")
	if catID != 0 {
		s.redis.Del(ctx, fmt.Sprintf("catalog:products:%d", catID))
	}
}

func (s *CachedProductStore) invalidateProductCache(ctx context.Context, pID int64, catID int64) {
	if s.redis == nil {
		return
	}
	s.redis.Del(ctx, fmt.Sprintf("catalog:product:%d", pID))
	if catID != 0 {
		s.redis.Del(ctx, fmt.Sprintf("catalog:products:%d", catID))
	}
}

func (s *CachedProductStore) CreateProduct(ctx context.Context, p *Product) (int64, error) {
	id, err := s.base.CreateProduct(ctx, p)
	if err == nil {
		s.invalidateCategoryCache(ctx, p.CategoryID)
	}
	return id, err
}

func (s *CachedProductStore) UpdateProduct(ctx context.Context, p *Product) error {
	err := s.base.UpdateProduct(ctx, p)
	if err == nil {
		s.invalidateProductCache(ctx, p.ID, p.CategoryID)
	}
	return err
}

func (s *CachedProductStore) DeleteProduct(ctx context.Context, id int64) error {
	p, _ := s.base.GetProduct(ctx, id)
	err := s.base.DeleteProduct(ctx, id)
	if err == nil && p != nil {
		s.invalidateProductCache(ctx, id, p.CategoryID)
	}
	return err
}

func (s *CachedProductStore) SearchProducts(ctx context.Context, query string) ([]Product, error) {
	return s.base.SearchProducts(ctx, query)
}

func (s *CachedProductStore) CreateCategory(ctx context.Context, cat *Category) (int64, error) {
	id, err := s.base.CreateCategory(ctx, cat)
	if err == nil {
		s.invalidateCategoryCache(ctx, 0)
	}
	return id, err
}

func (s *CachedProductStore) UpdateCategory(ctx context.Context, cat *Category) error {
	err := s.base.UpdateCategory(ctx, cat)
	if err == nil {
		s.invalidateCategoryCache(ctx, cat.ID)
	}
	return err
}

func (s *CachedProductStore) DeleteCategory(ctx context.Context, id int64) error {
	err := s.base.DeleteCategory(ctx, id)
	if err == nil {
		s.invalidateCategoryCache(ctx, id)
	}
	return err
}

func (s *CachedProductStore) GetCategory(ctx context.Context, id int64) (*Category, error) {
	return s.base.GetCategory(ctx, id)
}
