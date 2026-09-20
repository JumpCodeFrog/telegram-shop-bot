package shop

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"shop_bot/internal/service"
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
func (m *mockCartStore) CountActiveCarts(_ context.Context) (int64, error) { return 0, m.err }

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

// TestCartViewTotalRUB verifies TotalRUB is computed once from TotalUSD at the
// end of Get() (not summed per item, where per-item rounding drifts): 1x$10.00
// + 2x$4.995 => TotalUSD 19.99, and 19.99 at rate 92.5 -> 1849.08 RUB (the
// half-kopeck 1849.075 rounds up). A drifting multi-item cart (3x$6.663) pins
// the same rule where the two strategies disagree: once-at-end 1848.98 RUB vs
// per-item 3x616.33 = 1848.99 RUB. Without an exchange service or with a zero
// RUB rate (RUB disabled) TotalRUB stays 0.
func TestCartViewTotalRUB(t *testing.T) {
	cartMock := &mockCartStore{items: []storage.CartItem{
		{UserID: 42, ProductID: 1, Quantity: 1},
		{UserID: 42, ProductID: 2, Quantity: 2},
	}}
	productMock := &mockProductStore{byID: map[int64]*storage.Product{
		1: {ID: 1, CategoryID: 1, Name: "gadget", PriceUSD: 10.0, PriceStars: 500, IsActive: true, Stock: 10},
		2: {ID: 2, CategoryID: 1, Name: "cable", PriceUSD: 4.995, PriceStars: 249, IsActive: true, Stock: 10},
	}}

	svc := NewCartService(cartMock, productMock, service.NewExchangeService(50, 92.5, 0))
	view, err := svc.Get(context.Background(), 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if math.Abs(view.TotalUSD-19.99) > 1e-9 {
		t.Fatalf("TotalUSD=%f, want 19.99", view.TotalUSD)
	}
	if math.Abs(view.TotalRUB-1849.08) > 1e-9 {
		t.Fatalf("TotalRUB=%f, want 1849.08", view.TotalRUB)
	}

	// Drifting multi-item cart: 3 x $6.663 at rate 92.5. Converting once at
	// the end of Get() (see the drift comment in cart.go) gives
	// ConvertUSDToRUB(19.989) = 1848.98, while per-item conversion would give
	// 3 x ConvertUSDToRUB(6.663) = 3 x 616.33 = 1848.99. The leg pins the
	// once-at-end rule: a single-item cart cannot distinguish the two.
	driftCartMock := &mockCartStore{items: []storage.CartItem{
		{UserID: 42, ProductID: 3, Quantity: 1},
		{UserID: 42, ProductID: 4, Quantity: 1},
		{UserID: 42, ProductID: 5, Quantity: 1},
	}}
	driftProductMock := &mockProductStore{byID: map[int64]*storage.Product{
		3: {ID: 3, CategoryID: 1, Name: "widget", PriceUSD: 6.663, PriceStars: 333, IsActive: true, Stock: 10},
		4: {ID: 4, CategoryID: 1, Name: "bolt", PriceUSD: 6.663, PriceStars: 333, IsActive: true, Stock: 10},
		5: {ID: 5, CategoryID: 1, Name: "gizmo", PriceUSD: 6.663, PriceStars: 333, IsActive: true, Stock: 10},
	}}

	driftView, err := NewCartService(driftCartMock, driftProductMock, service.NewExchangeService(50, 92.5, 0)).Get(context.Background(), 42)
	if err != nil {
		t.Fatalf("unexpected error for drifting cart: %v", err)
	}
	if math.Abs(driftView.TotalUSD-19.989) > 1e-9 {
		t.Fatalf("drifting TotalUSD=%f, want 19.989", driftView.TotalUSD)
	}
	if math.Abs(driftView.TotalRUB-1848.98) > 1e-9 {
		t.Fatalf("drifting TotalRUB=%f, want 1848.98 (once-at-end), not 1848.99 (per item)", driftView.TotalRUB)
	}

	// No exchange service wired: TotalRUB stays 0.
	bare := NewCartService(cartMock, productMock)
	view, err = bare.Get(context.Background(), 42)
	if err != nil {
		t.Fatalf("unexpected error without exchange: %v", err)
	}
	if view.TotalRUB != 0 {
		t.Fatalf("TotalRUB without exchange service=%f, want 0", view.TotalRUB)
	}

	// RUB rate 0 (RUB payments disabled): TotalRUB stays 0.
	zeroRate := NewCartService(cartMock, productMock, service.NewExchangeService(50, 0, 5.13))
	view, err = zeroRate.Get(context.Background(), 42)
	if err != nil {
		t.Fatalf("unexpected error with zero rate: %v", err)
	}
	if view.TotalRUB != 0 {
		t.Fatalf("TotalRUB with zero rate=%f, want 0", view.TotalRUB)
	}
}

// TestCartViewTotalTONNano verifies TotalTONNano is computed once from
// TotalUSD at the end of Get() (not summed per item, where per-item nanoton
// rounding drifts): 1x$10.00 + 2x$4.995 => TotalUSD 19.99, and 19.99 at
// 5.13 USD/TON -> 3896686160 nanotons. A drifting multi-item cart
// (3x$6.663) pins the same rule where the two strategies disagree:
// once-at-end 3896491228 vs per-item 3x1298830409 = 3896491227. Without an
// exchange service or with a zero TON rate (TON disabled) TotalTONNano
// stays 0.
func TestCartViewTotalTONNano(t *testing.T) {
	cartMock := &mockCartStore{items: []storage.CartItem{
		{UserID: 42, ProductID: 1, Quantity: 1},
		{UserID: 42, ProductID: 2, Quantity: 2},
	}}
	productMock := &mockProductStore{byID: map[int64]*storage.Product{
		1: {ID: 1, CategoryID: 1, Name: "gadget", PriceUSD: 10.0, PriceStars: 500, IsActive: true, Stock: 10},
		2: {ID: 2, CategoryID: 1, Name: "cable", PriceUSD: 4.995, PriceStars: 249, IsActive: true, Stock: 10},
	}}

	svc := NewCartService(cartMock, productMock, service.NewExchangeService(50, 92.5, 5.13))
	view, err := svc.Get(context.Background(), 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if math.Abs(view.TotalUSD-19.99) > 1e-9 {
		t.Fatalf("TotalUSD=%f, want 19.99", view.TotalUSD)
	}
	if view.TotalTONNano != 3896686160 {
		t.Fatalf("TotalTONNano=%d, want 3896686160", view.TotalTONNano)
	}

	// Drifting multi-item cart: 3 x $6.663 at 5.13 USD/TON. Converting once
	// at the end of Get() (see the drift comment in cart.go) gives
	// ConvertUSDToNanoTON(19.989, 5.13) = 3896491228, while per-item
	// conversion would give 3 x ConvertUSDToNanoTON(6.663, 5.13) =
	// 3 x 1298830409 = 3896491227. The leg pins the once-at-end rule: a
	// single-item cart cannot distinguish the two. (Both values computed
	// with the real Go expression int64(math.Round(usd*1e9/5.13)).)
	driftCartMock := &mockCartStore{items: []storage.CartItem{
		{UserID: 42, ProductID: 3, Quantity: 1},
		{UserID: 42, ProductID: 4, Quantity: 1},
		{UserID: 42, ProductID: 5, Quantity: 1},
	}}
	driftProductMock := &mockProductStore{byID: map[int64]*storage.Product{
		3: {ID: 3, CategoryID: 1, Name: "widget", PriceUSD: 6.663, PriceStars: 333, IsActive: true, Stock: 10},
		4: {ID: 4, CategoryID: 1, Name: "bolt", PriceUSD: 6.663, PriceStars: 333, IsActive: true, Stock: 10},
		5: {ID: 5, CategoryID: 1, Name: "gizmo", PriceUSD: 6.663, PriceStars: 333, IsActive: true, Stock: 10},
	}}

	driftView, err := NewCartService(driftCartMock, driftProductMock, service.NewExchangeService(50, 92.5, 5.13)).Get(context.Background(), 42)
	if err != nil {
		t.Fatalf("unexpected error for drifting cart: %v", err)
	}
	if math.Abs(driftView.TotalUSD-19.989) > 1e-9 {
		t.Fatalf("drifting TotalUSD=%f, want 19.989", driftView.TotalUSD)
	}
	if driftView.TotalTONNano != 3896491228 {
		t.Fatalf("drifting TotalTONNano=%d, want 3896491228 (once-at-end), not 3896491227 (per item)", driftView.TotalTONNano)
	}

	// No exchange service wired: TotalTONNano stays 0.
	bare := NewCartService(cartMock, productMock)
	view, err = bare.Get(context.Background(), 42)
	if err != nil {
		t.Fatalf("unexpected error without exchange: %v", err)
	}
	if view.TotalTONNano != 0 {
		t.Fatalf("TotalTONNano without exchange service=%d, want 0", view.TotalTONNano)
	}

	// TON rate 0 (TON payments disabled): TotalTONNano stays 0.
	zeroRate := NewCartService(cartMock, productMock, service.NewExchangeService(50, 92.5, 0))
	view, err = zeroRate.Get(context.Background(), 42)
	if err != nil {
		t.Fatalf("unexpected error with zero rate: %v", err)
	}
	if view.TotalTONNano != 0 {
		t.Fatalf("TotalTONNano with zero rate=%d, want 0", view.TotalTONNano)
	}
}
