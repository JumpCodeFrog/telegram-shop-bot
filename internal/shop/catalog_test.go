package shop

import (
	"context"
	"fmt"
	"testing"

	"shop_bot/internal/storage"

	"pgregory.net/rapid"
)

// mockProductStore is a minimal mock for storage.ProductStore used by CatalogService tests.
type mockProductStore struct {
	categories []storage.Category
	products   map[int64][]storage.Product // keyed by categoryID
	byID       map[int64]*storage.Product
	err        error
}

func (m *mockProductStore) GetCategories(_ context.Context) ([]storage.Category, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.categories, nil
}

func (m *mockProductStore) GetProductsByCategory(_ context.Context, categoryID int64) ([]storage.Product, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.products[categoryID], nil
}

func (m *mockProductStore) GetProduct(_ context.Context, id int64) (*storage.Product, error) {
	if m.err != nil {
		return nil, m.err
	}
	p, ok := m.byID[id]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return p, nil
}

func (m *mockProductStore) GetProductsByCategoryPaged(_ context.Context, categoryID int64, limit, offset int) ([]storage.Product, int, error) {
	if m.err != nil {
		return nil, 0, m.err
	}
	all := m.products[categoryID]
	if offset >= len(all) {
		return nil, len(all), nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	return all[offset:end], len(all), nil
}
func (m *mockProductStore) CreateProduct(_ context.Context, _ *storage.Product) (int64, error) {
	return 0, nil
}
func (m *mockProductStore) UpdateProduct(_ context.Context, _ *storage.Product) error { return nil }
func (m *mockProductStore) DeleteProduct(_ context.Context, _ int64) error            { return nil }
func (m *mockProductStore) CreateCategory(_ context.Context, _ *storage.Category) (int64, error) {
	return 0, nil
}
func (m *mockProductStore) UpdateCategory(_ context.Context, _ *storage.Category) error { return nil }
func (m *mockProductStore) DeleteCategory(_ context.Context, _ int64) error             { return nil }
func (m *mockProductStore) SearchProducts(_ context.Context, _ string) ([]storage.Product, error) {
	return nil, nil
}
func (m *mockProductStore) GetCategory(_ context.Context, id int64) (*storage.Category, error) {
	return nil, storage.ErrNotFound
}

func TestListCategories(t *testing.T) {
	cats := []storage.Category{
		{ID: 1, Name: "Electronics", Emoji: "📱"},
		{ID: 2, Name: "Books", Emoji: "📚"},
	}
	svc := NewCatalogService(&mockProductStore{categories: cats})

	got, err := svc.ListCategories(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != len(cats) {
		t.Fatalf("expected %d categories, got %d", len(cats), len(got))
	}
	for i, c := range got {
		if c.ID != cats[i].ID || c.Name != cats[i].Name || c.Emoji != cats[i].Emoji {
			t.Errorf("category %d mismatch: got %+v, want %+v", i, c, cats[i])
		}
	}
}

func TestListProducts_FiltersOutOfStock(t *testing.T) {
	products := map[int64][]storage.Product{
		1: {
			{ID: 1, CategoryID: 1, Name: "Phone", PriceUSD: 999, PriceStars: 100, IsActive: true, Stock: 10},
			{ID: 2, CategoryID: 1, Name: "Tablet", PriceUSD: 499, PriceStars: 50, IsActive: false},
			{ID: 3, CategoryID: 1, Name: "Laptop", PriceUSD: 1499, PriceStars: 150, IsActive: true, Stock: 10},
		},
	}
	svc := NewCatalogService(&mockProductStore{products: products})

	got, err := svc.ListProducts(context.Background(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 in-stock products, got %d", len(got))
	}
	for _, p := range got {
		if !(p.IsActive && p.Stock > 0) {
			t.Errorf("product %q should be in stock", p.Name)
		}
	}
}

func TestListProducts_EmptyCategory(t *testing.T) {
	svc := NewCatalogService(&mockProductStore{products: map[int64][]storage.Product{}})

	got, err := svc.ListProducts(context.Background(), 99)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 products for empty category, got %d", len(got))
	}
}

func TestListProducts_AllOutOfStock(t *testing.T) {
	products := map[int64][]storage.Product{
		1: {
			{ID: 1, CategoryID: 1, Name: "Phone", IsActive: false},
			{ID: 2, CategoryID: 1, Name: "Tablet", IsActive: false},
		},
	}
	svc := NewCatalogService(&mockProductStore{products: products})

	got, err := svc.ListProducts(context.Background(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 in-stock products, got %d", len(got))
	}
}

func TestGetProduct(t *testing.T) {
	p := &storage.Product{ID: 42, Name: "Widget", Description: "A fine widget", PriceUSD: 9.99, PriceStars: 10, PhotoURL: "photo123"}
	svc := NewCatalogService(&mockProductStore{byID: map[int64]*storage.Product{42: p}})

	got, err := svc.GetProduct(context.Background(), 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != p.ID || got.Name != p.Name || got.Description != p.Description {
		t.Errorf("product mismatch: got %+v, want %+v", got, p)
	}
}

func TestGetProduct_NotFound(t *testing.T) {
	svc := NewCatalogService(&mockProductStore{byID: map[int64]*storage.Product{}})

	_, err := svc.GetProduct(context.Background(), 999)
	if err != storage.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// Feature: shop_bot, Property 3: Каталог отображает только товары в наличии
// Validates: Requirements 3.2, 3.4
func TestProperty3_ListProductsOnlyInStock(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		catID := rapid.Int64Range(1, 100).Draw(t, "categoryID")
		n := rapid.IntRange(0, 20).Draw(t, "numProducts")

		products := make([]storage.Product, n)
		for i := range products {
			products[i] = storage.Product{
				ID:         int64(i + 1),
				CategoryID: catID,
				Name:       rapid.StringMatching(`[A-Za-z]{1,20}`).Draw(t, fmt.Sprintf("name_%d", i)),
				PriceUSD:   rapid.Float64Range(0.01, 10000).Draw(t, fmt.Sprintf("priceUSD_%d", i)),
				PriceStars: rapid.IntRange(1, 100000).Draw(t, fmt.Sprintf("priceStars_%d", i)),
				IsActive:   rapid.Bool().Draw(t, fmt.Sprintf("isActive_%d", i)),
				Stock:      rapid.IntRange(0, 20).Draw(t, fmt.Sprintf("stock_%d", i)),
			}
		}

		mock := &mockProductStore{
			products: map[int64][]storage.Product{catID: products},
		}
		svc := NewCatalogService(mock)

		got, err := svc.ListProducts(context.Background(), catID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// All returned products must be in stock
		for _, p := range got {
			if !(p.IsActive && p.Stock > 0) {
				t.Fatalf("returned out-of-stock product: %+v", p)
			}
			if p.Name == "" {
				t.Fatalf("returned product with empty name")
			}
			if p.PriceUSD <= 0 {
				t.Fatalf("returned product with non-positive PriceUSD: %v", p.PriceUSD)
			}
			if p.PriceStars <= 0 {
				t.Fatalf("returned product with non-positive PriceStars: %v", p.PriceStars)
			}
		}

		// Count expected in-stock products
		expectedCount := 0
		for _, p := range products {
			if p.IsActive && p.Stock > 0 {
				expectedCount++
			}
		}
		if len(got) != expectedCount {
			t.Fatalf("expected %d in-stock products, got %d", expectedCount, len(got))
		}
	})
}

// Feature: shop_bot, Property 4: Карточка товара содержит все обязательные поля
// Validates: Requirements 3.3
func TestProperty4_GetProductContainsAllFields(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		id := rapid.Int64Range(1, 1000).Draw(t, "productID")
		product := &storage.Product{
			ID:          id,
			CategoryID:  rapid.Int64Range(1, 100).Draw(t, "categoryID"),
			Name:        rapid.StringMatching(`[A-Za-z]{1,30}`).Draw(t, "name"),
			Description: rapid.StringMatching(`[A-Za-z ]{1,100}`).Draw(t, "description"),
			PriceUSD:    rapid.Float64Range(0.01, 10000).Draw(t, "priceUSD"),
			PriceStars:  rapid.IntRange(1, 100000).Draw(t, "priceStars"),
			PhotoURL:    rapid.StringMatching(`[a-zA-Z0-9]{5,30}`).Draw(t, "photoID"),
			IsActive:    true, Stock: 10,
		}

		mock := &mockProductStore{
			byID: map[int64]*storage.Product{id: product},
		}
		svc := NewCatalogService(mock)

		got, err := svc.GetProduct(context.Background(), id)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if got.Name == "" {
			t.Fatal("product name is empty")
		}
		if got.Description == "" {
			t.Fatal("product description is empty")
		}
		if got.PriceUSD == 0 {
			t.Fatal("product PriceUSD is zero")
		}
		if got.PriceStars == 0 {
			t.Fatal("product PriceStars is zero")
		}
		if got.PhotoURL == "" {
			t.Fatal("product PhotoURL is empty")
		}
	})
}

// Feature: shop_bot, Property 19: Список категорий содержит все категории
// Validates: Requirements 3.1
func TestProperty19_ListCategoriesReturnsAll(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 30).Draw(t, "numCategories")

		categories := make([]storage.Category, n)
		for i := range categories {
			categories[i] = storage.Category{
				ID:    int64(i + 1),
				Name:  rapid.StringMatching(`[A-Za-z]{1,20}`).Draw(t, fmt.Sprintf("catName_%d", i)),
				Emoji: rapid.StringMatching(`[^\x00]{1,4}`).Draw(t, fmt.Sprintf("catEmoji_%d", i)),
			}
		}

		mock := &mockProductStore{categories: categories}
		svc := NewCatalogService(mock)

		got, err := svc.ListCategories(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(got) != len(categories) {
			t.Fatalf("expected %d categories, got %d", len(categories), len(got))
		}

		for i, c := range got {
			if c.Name == "" {
				t.Fatalf("category %d has empty name", i)
			}
			if c.Emoji == "" {
				t.Fatalf("category %d has empty emoji", i)
			}
			if c.ID != categories[i].ID || c.Name != categories[i].Name || c.Emoji != categories[i].Emoji {
				t.Fatalf("category %d mismatch: got %+v, want %+v", i, c, categories[i])
			}
		}
	})
}
