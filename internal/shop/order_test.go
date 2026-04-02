package shop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"shop_bot/internal/storage"

	"pgregory.net/rapid"
)

// mockOrderStore is a minimal mock for storage.OrderStore used by OrderService tests.
type mockOrderStore struct {
	orders          map[int64]*storage.Order
	nextID          int64
	createdItems    []storage.OrderItem
	updatedID       int64
	updatedFromStat string
	updatedStat     string
	updatedPM       string
	updatedPID      string
	err             error
}

func newMockOrderStore() *mockOrderStore {
	return &mockOrderStore{
		orders: make(map[int64]*storage.Order),
		nextID: 1,
	}
}

func (m *mockOrderStore) CreateOrder(_ context.Context, order *storage.Order, items []storage.OrderItem) (int64, error) {
	if m.err != nil {
		return 0, m.err
	}
	id := m.nextID
	m.nextID++
	order.ID = id
	m.orders[id] = order
	m.createdItems = items
	return id, nil
}

func (m *mockOrderStore) GetOrder(_ context.Context, id int64) (*storage.Order, error) {
	if m.err != nil {
		return nil, m.err
	}
	o, ok := m.orders[id]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return o, nil
}

func (m *mockOrderStore) GetUserOrders(_ context.Context, userID int64) ([]storage.Order, error) {
	if m.err != nil {
		return nil, m.err
	}
	var result []storage.Order
	for _, o := range m.orders {
		if o.UserID == userID {
			result = append(result, *o)
		}
	}
	return result, nil
}

func (m *mockOrderStore) GetAllOrders(_ context.Context, statusFilter string) ([]storage.Order, error) {
	if m.err != nil {
		return nil, m.err
	}
	var result []storage.Order
	for _, o := range m.orders {
		if statusFilter == "" || o.Status == statusFilter {
			result = append(result, *o)
		}
	}
	return result, nil
}

func (m *mockOrderStore) CancelOrder(_ context.Context, orderID, userID int64) error {
	if m.err != nil {
		return m.err
	}
	o, ok := m.orders[orderID]
	if !ok || o.UserID != userID {
		return storage.ErrNotFound
	}
	o.Status = storage.OrderStatusCancelled
	return nil
}

func (m *mockOrderStore) UpdateOrderStatus(_ context.Context, id int64, fromStatus, status, paymentMethod, paymentID string) error {
	if m.err != nil {
		return m.err
	}
	o, ok := m.orders[id]
	if !ok || o.Status != fromStatus {
		return storage.ErrOrderStatusConflict
	}
	o.Status = status
	o.PaymentMethod = paymentMethod
	o.PaymentID = paymentID
	m.updatedID = id
	m.updatedFromStat = fromStatus
	m.updatedStat = status
	m.updatedPM = paymentMethod
	m.updatedPID = paymentID
	return nil
}

// mockClearCartStore tracks ClearCart calls for verifying cart clearing.
type mockClearCartStore struct {
	cleared bool
	userID  int64
	err     error
}

func (m *mockClearCartStore) AddItem(_ context.Context, _, _ int64) error { return nil }
func (m *mockClearCartStore) GetItems(_ context.Context, _ int64) ([]storage.CartItem, error) {
	return nil, nil
}
func (m *mockClearCartStore) UpdateQuantity(_ context.Context, _, _ int64, _ int) error { return nil }
func (m *mockClearCartStore) RemoveItem(_ context.Context, _, _ int64) error            { return nil }
func (m *mockClearCartStore) GetAbandonedCarts(_ context.Context, _ time.Duration) ([]int64, error) {
	return nil, nil
}
func (m *mockClearCartStore) MarkRecoverySent(_ context.Context, _ int64) error { return nil }
func (m *mockClearCartStore) ClearCart(_ context.Context, userID int64) error {
	if m.err != nil {
		return m.err
	}
	m.cleared = true
	m.userID = userID
	return nil
}

// isActiveProductStore is a minimal ProductStore stub that reports every product
// as in-stock. Used by OrderService tests that do not exercise stock-checking logic.
type isActiveProductStore struct{}

func (isActiveProductStore) GetProduct(_ context.Context, id int64) (*storage.Product, error) {
	return &storage.Product{ID: id, IsActive: true, Stock: 10}, nil
}
func (isActiveProductStore) GetCategories(context.Context) ([]storage.Category, error) {
	return nil, nil
}
func (isActiveProductStore) GetProductsByCategory(context.Context, int64) ([]storage.Product, error) {
	return nil, nil
}
func (isActiveProductStore) GetProductsByCategoryPaged(context.Context, int64, int, int) ([]storage.Product, int, error) {
	return nil, 0, nil
}
func (isActiveProductStore) CreateProduct(context.Context, *storage.Product) (int64, error) {
	return 0, nil
}
func (isActiveProductStore) UpdateProduct(context.Context, *storage.Product) error { return nil }
func (isActiveProductStore) DeleteProduct(context.Context, int64) error            { return nil }
func (isActiveProductStore) SearchProducts(context.Context, string) ([]storage.Product, error) {
	return nil, nil
}
func (isActiveProductStore) CreateCategory(context.Context, *storage.Category) (int64, error) {
	return 0, nil
}
func (isActiveProductStore) UpdateCategory(context.Context, *storage.Category) error { return nil }
func (isActiveProductStore) DeleteCategory(context.Context, int64) error             { return nil }
func (isActiveProductStore) GetCategory(context.Context, int64) (*storage.Category, error) {
	return nil, storage.ErrNotFound
}

func TestCreateFromCart_Success(t *testing.T) {
	os := newMockOrderStore()
	cs := &mockClearCartStore{}
	svc := NewOrderService(os, cs, isActiveProductStore{}, slog.Default())

	view := &CartView{
		Items: []CartItemView{
			{Product: storage.Product{ID: 1, PriceUSD: 10.0, PriceStars: 100}, Quantity: 2},
			{Product: storage.Product{ID: 2, PriceUSD: 5.0, PriceStars: 50}, Quantity: 1},
		},
		TotalUSD:   25.0,
		TotalStars: 250,
	}

	orderID, err := svc.CreateFromCart(context.Background(), 42, view, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if orderID != 1 {
		t.Fatalf("expected orderID 1, got %d", orderID)
	}

	// Verify order was created with correct fields
	order := os.orders[orderID]
	if order.UserID != 42 {
		t.Errorf("expected userID 42, got %d", order.UserID)
	}
	if order.Status != storage.OrderStatusPending {
		t.Errorf("expected status %q, got %q", storage.OrderStatusPending, order.Status)
	}
	if order.TotalUSD != 25.0 {
		t.Errorf("expected TotalUSD 25.0, got %f", order.TotalUSD)
	}
	if order.TotalStars != 250 {
		t.Errorf("expected TotalStars 250, got %d", order.TotalStars)
	}

	// Verify order items
	if len(os.createdItems) != 2 {
		t.Fatalf("expected 2 order items, got %d", len(os.createdItems))
	}
	if os.createdItems[0].ProductID != 1 || os.createdItems[0].Quantity != 2 || os.createdItems[0].PriceUSD != 10.0 {
		t.Errorf("item 0 mismatch: %+v", os.createdItems[0])
	}

	// Verify cart was cleared
	if !cs.cleared {
		t.Error("expected cart to be cleared")
	}
	if cs.userID != 42 {
		t.Errorf("expected cart cleared for userID 42, got %d", cs.userID)
	}
}

func TestCreateFromCart_EmptyCart(t *testing.T) {
	os := newMockOrderStore()
	cs := &mockClearCartStore{}
	svc := NewOrderService(os, cs, isActiveProductStore{}, slog.Default())

	view := &CartView{Items: []CartItemView{}}

	_, err := svc.CreateFromCart(context.Background(), 42, view, nil)
	if err != storage.ErrEmptyCart {
		t.Fatalf("expected ErrEmptyCart, got %v", err)
	}
}

func TestConfirmPayment(t *testing.T) {
	os := newMockOrderStore()
	os.orders[1] = &storage.Order{ID: 1, UserID: 42, Status: storage.OrderStatusPending}
	cs := &mockClearCartStore{}
	svc := NewOrderService(os, cs, isActiveProductStore{}, slog.Default())

	err := svc.ConfirmPayment(context.Background(), 1, storage.PaymentMethodStars, "pay_123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	order := os.orders[1]
	if order.Status != storage.OrderStatusPaid {
		t.Errorf("expected status %q, got %q", storage.OrderStatusPaid, order.Status)
	}
	if order.PaymentMethod != storage.PaymentMethodStars {
		t.Errorf("expected payment method %q, got %q", storage.PaymentMethodStars, order.PaymentMethod)
	}
	if order.PaymentID != "pay_123" {
		t.Errorf("expected payment ID %q, got %q", "pay_123", order.PaymentID)
	}
}

func TestSetDelivered(t *testing.T) {
	os := newMockOrderStore()
	os.orders[1] = &storage.Order{ID: 1, UserID: 42, Status: storage.OrderStatusPaid}
	cs := &mockClearCartStore{}
	svc := NewOrderService(os, cs, isActiveProductStore{}, slog.Default())

	order, err := svc.SetDelivered(context.Background(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if order.Status != storage.OrderStatusDelivered {
		t.Errorf("expected status %q, got %q", storage.OrderStatusDelivered, order.Status)
	}
}

func TestSetDelivered_NotFound(t *testing.T) {
	os := newMockOrderStore()
	cs := &mockClearCartStore{}
	svc := NewOrderService(os, cs, isActiveProductStore{}, slog.Default())

	_, err := svc.SetDelivered(context.Background(), 999)
	if err == nil {
		t.Fatal("expected error for non-existent order")
	}
}

func TestGetUserOrders(t *testing.T) {
	os := newMockOrderStore()
	os.orders[1] = &storage.Order{ID: 1, UserID: 42, Status: storage.OrderStatusPaid}
	os.orders[2] = &storage.Order{ID: 2, UserID: 42, Status: storage.OrderStatusPending}
	os.orders[3] = &storage.Order{ID: 3, UserID: 99, Status: storage.OrderStatusPaid}
	cs := &mockClearCartStore{}
	svc := NewOrderService(os, cs, isActiveProductStore{}, slog.Default())

	orders, err := svc.GetUserOrders(context.Background(), 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orders) != 2 {
		t.Fatalf("expected 2 orders for user 42, got %d", len(orders))
	}
	for _, o := range orders {
		if o.UserID != 42 {
			t.Errorf("expected userID 42, got %d", o.UserID)
		}
	}
}

func TestGetAllOrders_NoFilter(t *testing.T) {
	os := newMockOrderStore()
	os.orders[1] = &storage.Order{ID: 1, UserID: 42, Status: storage.OrderStatusPaid}
	os.orders[2] = &storage.Order{ID: 2, UserID: 42, Status: storage.OrderStatusPending}
	cs := &mockClearCartStore{}
	svc := NewOrderService(os, cs, isActiveProductStore{}, slog.Default())

	orders, err := svc.GetAllOrders(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orders) != 2 {
		t.Fatalf("expected 2 orders, got %d", len(orders))
	}
}

func TestGetAllOrders_WithFilter(t *testing.T) {
	os := newMockOrderStore()
	os.orders[1] = &storage.Order{ID: 1, UserID: 42, Status: storage.OrderStatusPaid}
	os.orders[2] = &storage.Order{ID: 2, UserID: 42, Status: storage.OrderStatusPending}
	cs := &mockClearCartStore{}
	svc := NewOrderService(os, cs, isActiveProductStore{}, slog.Default())

	orders, err := svc.GetAllOrders(context.Background(), storage.OrderStatusPaid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orders) != 1 {
		t.Fatalf("expected 1 paid order, got %d", len(orders))
	}
	if orders[0].Status != storage.OrderStatusPaid {
		t.Errorf("expected status %q, got %q", storage.OrderStatusPaid, orders[0].Status)
	}
}

// Feature: shop_bot, Property 8: Создание заказа из корзины
func TestProperty_CreateFromCart(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		userID := rapid.Int64Range(1, 1_000_000).Draw(t, "userID")

		numItems := rapid.IntRange(1, 10).Draw(t, "numItems")
		items := make([]CartItemView, numItems)
		var expectedTotalUSD float64
		var expectedTotalStars int

		for i := 0; i < numItems; i++ {
			productID := rapid.Int64Range(1, 10000).Draw(t, fmt.Sprintf("productID_%d", i))
			priceUSD := float64(rapid.IntRange(1, 10000).Draw(t, fmt.Sprintf("priceUSD_cents_%d", i))) / 100.0
			priceStars := rapid.IntRange(1, 5000).Draw(t, fmt.Sprintf("priceStars_%d", i))
			qty := rapid.IntRange(1, 20).Draw(t, fmt.Sprintf("qty_%d", i))

			items[i] = CartItemView{
				Product: storage.Product{
					ID:         productID,
					PriceUSD:   priceUSD,
					PriceStars: priceStars,
				},
				Quantity: qty,
			}
			expectedTotalUSD += priceUSD * float64(qty)
			expectedTotalStars += priceStars * qty
		}

		cartView := &CartView{
			Items:      items,
			TotalUSD:   expectedTotalUSD,
			TotalStars: expectedTotalStars,
		}

		os := newMockOrderStore()
		cs := &mockClearCartStore{}
		svc := NewOrderService(os, cs, isActiveProductStore{}, slog.Default())

		orderID, err := svc.CreateFromCart(context.Background(), userID, cartView, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if orderID < 1 {
			t.Fatalf("expected positive orderID, got %d", orderID)
		}

		order, ok := os.orders[orderID]
		if !ok {
			t.Fatal("order not found in store")
		}
		if order.Status != storage.OrderStatusPending {
			t.Fatalf("expected status %q, got %q", storage.OrderStatusPending, order.Status)
		}
		if order.UserID != userID {
			t.Fatalf("expected userID %d, got %d", userID, order.UserID)
		}

		if len(os.createdItems) != numItems {
			t.Fatalf("expected %d order items, got %d", numItems, len(os.createdItems))
		}
		for i, oi := range os.createdItems {
			if oi.ProductID != items[i].Product.ID {
				t.Errorf("item %d: expected productID %d, got %d", i, items[i].Product.ID, oi.ProductID)
			}
			if oi.Quantity != items[i].Quantity {
				t.Errorf("item %d: expected quantity %d, got %d", i, items[i].Quantity, oi.Quantity)
			}
			if oi.PriceUSD != items[i].Product.PriceUSD {
				t.Errorf("item %d: expected priceUSD %f, got %f", i, items[i].Product.PriceUSD, oi.PriceUSD)
			}
		}
	})
}

// Feature: shop_bot, Property 9: Очистка корзины после создания заказа
func TestProperty_ClearCartAfterOrder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		userID := rapid.Int64Range(1, 1_000_000).Draw(t, "userID")

		numItems := rapid.IntRange(1, 5).Draw(t, "numItems")
		items := make([]CartItemView, numItems)
		for i := 0; i < numItems; i++ {
			items[i] = CartItemView{
				Product: storage.Product{
					ID:         rapid.Int64Range(1, 10000).Draw(t, fmt.Sprintf("productID_%d", i)),
					PriceUSD:   float64(rapid.IntRange(1, 10000).Draw(t, fmt.Sprintf("priceUSD_%d", i))) / 100.0,
					PriceStars: rapid.IntRange(1, 5000).Draw(t, fmt.Sprintf("priceStars_%d", i)),
				},
				Quantity: rapid.IntRange(1, 10).Draw(t, fmt.Sprintf("qty_%d", i)),
			}
		}

		cartView := &CartView{Items: items, TotalUSD: 1.0, TotalStars: 1}

		os := newMockOrderStore()
		cs := &mockClearCartStore{}
		svc := NewOrderService(os, cs, isActiveProductStore{}, slog.Default())

		_, err := svc.CreateFromCart(context.Background(), userID, cartView, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !cs.cleared {
			t.Fatal("expected cart to be cleared after order creation")
		}
		if cs.userID != userID {
			t.Fatalf("expected cart cleared for userID %d, got %d", userID, cs.userID)
		}
	})
}

// Feature: shop_bot, Property 10: Подтверждение оплаты обновляет статус заказа
func TestProperty_ConfirmPayment(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		orderID := rapid.Int64Range(1, 1_000_000).Draw(t, "orderID")
		method := rapid.SampledFrom([]string{storage.PaymentMethodStars, storage.PaymentMethodCrypto}).Draw(t, "method")
		paymentID := rapid.StringMatching(`[a-zA-Z0-9]{5,30}`).Draw(t, "paymentID")

		os := newMockOrderStore()
		os.orders[orderID] = &storage.Order{
			ID:     orderID,
			UserID: rapid.Int64Range(1, 1_000_000).Draw(t, "userID"),
			Status: storage.OrderStatusPending,
		}
		cs := &mockClearCartStore{}
		svc := NewOrderService(os, cs, isActiveProductStore{}, slog.Default())

		err := svc.ConfirmPayment(context.Background(), orderID, method, paymentID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		order := os.orders[orderID]
		if order.Status != storage.OrderStatusPaid {
			t.Fatalf("expected status %q, got %q", storage.OrderStatusPaid, order.Status)
		}
		if order.PaymentMethod != method {
			t.Fatalf("expected payment method %q, got %q", method, order.PaymentMethod)
		}
		if order.PaymentID != paymentID {
			t.Fatalf("expected payment ID %q, got %q", paymentID, order.PaymentID)
		}
	})
}

// TestConfirmPayment_AlreadyPaid verifies that confirming a payment on an
// already-paid order returns ErrOrderStatusConflict (idempotency guard).
func TestConfirmPayment_AlreadyPaid(t *testing.T) {
	os := newMockOrderStore()
	os.orders[1] = &storage.Order{ID: 1, UserID: 42, Status: storage.OrderStatusPaid}
	svc := NewOrderService(os, &mockClearCartStore{}, isActiveProductStore{}, slog.Default())

	err := svc.ConfirmPayment(context.Background(), 1, "stars", "charge_123")
	if !errors.Is(err, storage.ErrOrderStatusConflict) {
		t.Fatalf("expected ErrOrderStatusConflict, got %v", err)
	}
}

// TestSetDelivered_WrongStatus verifies that marking a pending order as
// delivered returns ErrOrderStatusConflict.
func TestSetDelivered_WrongStatus(t *testing.T) {
	os := newMockOrderStore()
	os.orders[1] = &storage.Order{ID: 1, UserID: 42, Status: storage.OrderStatusPending}
	svc := NewOrderService(os, &mockClearCartStore{}, isActiveProductStore{}, slog.Default())

	_, err := svc.SetDelivered(context.Background(), 1)
	if !errors.Is(err, storage.ErrOrderStatusConflict) {
		t.Fatalf("expected ErrOrderStatusConflict, got %v", err)
	}
}

// TestCreateFromCart_ClearCartFails_OrderStillReturned verifies that a ClearCart
// failure does not propagate as an error: the order is committed and the caller
// receives a valid orderID.
func TestCreateFromCart_ClearCartFails_OrderStillReturned(t *testing.T) {
	os := newMockOrderStore()
	cs := &mockClearCartStore{err: fmt.Errorf("db timeout")}
	svc := NewOrderService(os, cs, isActiveProductStore{}, slog.Default())

	view := &CartView{
		Items:      []CartItemView{{Product: storage.Product{ID: 1, PriceUSD: 5.0, PriceStars: 50}, Quantity: 1}},
		TotalUSD:   5.0,
		TotalStars: 50,
	}

	orderID, err := svc.CreateFromCart(context.Background(), 42, view, nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if orderID <= 0 {
		t.Fatalf("expected valid orderID, got %d", orderID)
	}
}
