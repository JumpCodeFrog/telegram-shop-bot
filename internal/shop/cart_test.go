package shop

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"shop_bot/internal/storage"

	"pgregory.net/rapid"
)

// mockCartStore is a minimal mock for storage.CartStore used by CartService tests.
type mockCartStore struct {
	items             []storage.CartItem
	err               error
	addCalls          int
	lastAddProductID  int64
	lastUpdateQty     int
	lastUpdateProduct int64
	removeCalls       int
	lastRemoveProduct int64
}

func (m *mockCartStore) AddItem(_ context.Context, _, productID int64) error {
	m.addCalls++
	m.lastAddProductID = productID
	return m.err
}
func (m *mockCartStore) GetItems(_ context.Context, _ int64) ([]storage.CartItem, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.items, nil
}
func (m *mockCartStore) UpdateQuantity(_ context.Context, _, productID int64, qty int) error {
	m.lastUpdateProduct = productID
	m.lastUpdateQty = qty
	return m.err
}
func (m *mockCartStore) RemoveItem(_ context.Context, _, productID int64) error {
	m.removeCalls++
	m.lastRemoveProduct = productID
	return m.err
}
func (m *mockCartStore) ClearCart(_ context.Context, _ int64) error { return m.err }
func (m *mockCartStore) GetAbandonedCarts(_ context.Context, _ time.Duration) ([]int64, error) {
	return nil, m.err
}
func (m *mockCartStore) MarkRecoverySent(_ context.Context, _ int64) error { return m.err }

// Feature: shop_bot, Property 6: Корректность вычисления итогов корзины
// Validates: Requirements 4.9, 4.3
func TestProperty6_CartTotalsComputation(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate 1-5 random products
		numProducts := rapid.IntRange(1, 5).Draw(t, "numProducts")

		products := make([]storage.Product, numProducts)
		byID := make(map[int64]*storage.Product, numProducts)
		for i := 0; i < numProducts; i++ {
			p := storage.Product{
				ID:         int64(i + 1),
				CategoryID: 1,
				Name:       rapid.StringMatching(`[A-Za-z]{1,20}`).Draw(t, fmt.Sprintf("name_%d", i)),
				PriceUSD:   rapid.Float64Range(0.01, 9999.99).Draw(t, fmt.Sprintf("priceUSD_%d", i)),
				PriceStars: rapid.IntRange(1, 100000).Draw(t, fmt.Sprintf("priceStars_%d", i)),
				IsActive:   true, Stock: 10,
			}
			products[i] = p
			byID[p.ID] = &products[i]
		}

		// Generate cart items with random quantities for those products
		cartItems := make([]storage.CartItem, numProducts)
		for i := 0; i < numProducts; i++ {
			cartItems[i] = storage.CartItem{
				ID:        int64(i + 1),
				UserID:    42,
				ProductID: products[i].ID,
				Quantity:  rapid.IntRange(1, 20).Draw(t, fmt.Sprintf("qty_%d", i)),
			}
		}

		// Create mock stores
		cartMock := &mockCartStore{items: cartItems}
		productMock := &mockProductStore{byID: byID}

		svc := NewCartService(cartMock, productMock)

		// Call CartService.Get()
		view, err := svc.Get(context.Background(), 42)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Independently compute expected totals
		var expectedUSD float64
		var expectedStars int
		for _, ci := range cartItems {
			p := byID[ci.ProductID]
			expectedUSD += p.PriceUSD * float64(ci.Quantity)
			expectedStars += p.PriceStars * ci.Quantity
		}

		// Verify TotalUSD matches (using small epsilon for floating point)
		if math.Abs(view.TotalUSD-expectedUSD) > 1e-9 {
			t.Fatalf("TotalUSD mismatch: got %f, want %f", view.TotalUSD, expectedUSD)
		}

		// Verify TotalStars matches exactly
		if view.TotalStars != expectedStars {
			t.Fatalf("TotalStars mismatch: got %d, want %d", view.TotalStars, expectedStars)
		}

		// Verify item count matches
		if len(view.Items) != numProducts {
			t.Fatalf("item count mismatch: got %d, want %d", len(view.Items), numProducts)
		}
	})
}

func TestChangeQuantity_IncrementsExistingItem(t *testing.T) {
	cartMock := &mockCartStore{
		items: []storage.CartItem{{UserID: 42, ProductID: 7, Quantity: 2}},
	}
	productMock := &mockProductStore{
		byID: map[int64]*storage.Product{
			7: {ID: 7, IsActive: true, Stock: 10},
		},
	}

	svc := NewCartService(cartMock, productMock)
	if err := svc.ChangeQuantity(context.Background(), 42, 7, 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cartMock.lastUpdateQty != 3 {
		t.Fatalf("expected updated quantity 3, got %d", cartMock.lastUpdateQty)
	}
	if cartMock.addCalls != 0 {
		t.Fatalf("expected no AddItem call, got %d", cartMock.addCalls)
	}
}

func TestChangeQuantity_RemovesWhenQuantityDropsToZero(t *testing.T) {
	cartMock := &mockCartStore{
		items: []storage.CartItem{{UserID: 42, ProductID: 7, Quantity: 1}},
	}
	productMock := &mockProductStore{
		byID: map[int64]*storage.Product{
			7: {ID: 7, IsActive: true, Stock: 10},
		},
	}

	svc := NewCartService(cartMock, productMock)
	if err := svc.ChangeQuantity(context.Background(), 42, 7, -1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cartMock.removeCalls != 1 {
		t.Fatalf("expected one RemoveItem call, got %d", cartMock.removeCalls)
	}
	if cartMock.lastRemoveProduct != 7 {
		t.Fatalf("expected RemoveItem for product 7, got %d", cartMock.lastRemoveProduct)
	}
}

func TestChangeQuantity_AddsMissingItem(t *testing.T) {
	cartMock := &mockCartStore{}
	productMock := &mockProductStore{
		byID: map[int64]*storage.Product{
			7: {ID: 7, IsActive: true, Stock: 10},
		},
	}

	svc := NewCartService(cartMock, productMock)
	if err := svc.ChangeQuantity(context.Background(), 42, 7, 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cartMock.addCalls != 1 {
		t.Fatalf("expected one AddItem call, got %d", cartMock.addCalls)
	}
	if cartMock.lastUpdateQty != 0 {
		t.Fatalf("did not expect UpdateQuantity for delta=1, got %d", cartMock.lastUpdateQty)
	}
}

func TestChangeQuantity_RejectsOutOfStockIncrease(t *testing.T) {
	cartMock := &mockCartStore{
		items: []storage.CartItem{{UserID: 42, ProductID: 7, Quantity: 2}},
	}
	productMock := &mockProductStore{
		byID: map[int64]*storage.Product{
			7: {ID: 7, IsActive: true, Stock: 2},
		},
	}

	svc := NewCartService(cartMock, productMock)
	err := svc.ChangeQuantity(context.Background(), 42, 7, 1)
	if err != storage.ErrProductOutOfStock {
		t.Fatalf("expected ErrProductOutOfStock, got %v", err)
	}
}
