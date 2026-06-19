package shop

import (
	"context"
	"fmt"
	"log/slog"

	"shop_bot/internal/storage"
)

// OrderService provides business logic for managing orders.
type OrderService struct {
	orders   storage.OrderStore
	cart     storage.CartStore
	products storage.ProductStore
	promos   storage.PromoStore
	logger   *slog.Logger
}

// NewOrderService creates a new OrderService backed by the given stores.
func NewOrderService(os storage.OrderStore, cs storage.CartStore, ps storage.ProductStore, promo storage.PromoStore, logger *slog.Logger) *OrderService {
	return &OrderService{orders: os, cart: cs, products: ps, promos: promo, logger: logger}
}

// CreateFromCart creates a new order from the given CartView. If promo is
// non-nil, the discount is applied to the totals. The order is created with
// status "pending" and the user's cart is cleared afterwards.
// Returns ErrEmptyCart if the CartView has no items.
// Returns ErrProductOutOfStock (wrapped with product name) if any item is out of stock.
func (s *OrderService) CreateFromCart(ctx context.Context, userID int64, cartView *CartView, promo *storage.PromoCode) (int64, error) {
	if len(cartView.Items) == 0 {
		return 0, storage.ErrEmptyCart
	}

	// Re-check stock status for all cart items at order creation time.
	for _, ci := range cartView.Items {
		p, err := s.products.GetProduct(ctx, ci.Product.ID)
		if err != nil {
			return 0, fmt.Errorf("order service: get product %d: %w", ci.Product.ID, err)
		}
		if !(p.IsActive && p.Stock > 0) {
			return 0, fmt.Errorf("товар «%s» недоступен для заказа: %w", p.Name, storage.ErrProductOutOfStock)
		}
	}

	totalUSD := cartView.TotalUSD
	totalStars := cartView.TotalStars
	discountPct := 0
	promoCode := ""

	if promo != nil {
		discountPct = promo.Discount
		promoCode = promo.Code
		totalUSD = totalUSD * float64(100-discountPct) / 100
		totalStars = totalStars * (100 - discountPct) / 100
	}

	order := &storage.Order{
		UserID:      userID,
		TotalUSD:    totalUSD,
		TotalStars:  totalStars,
		Status:      storage.OrderStatusPending,
		DiscountPct: discountPct,
		PromoCode:   promoCode,
	}

	items := make([]storage.OrderItem, len(cartView.Items))
	for i, ci := range cartView.Items {
		items[i] = storage.OrderItem{
			ProductID:   ci.Product.ID,
			ProductName: ci.Product.Name,
			Quantity:    ci.Quantity,
			PriceUSD:    ci.Product.PriceUSD,
		}
	}

	orderID, err := s.orders.CreateOrder(ctx, order, items)
	if err != nil {
		return 0, fmt.Errorf("order service: create order: %w", err)
	}

	// ClearCart is best-effort: the order is already committed. A failure here
	// leaves the cart stale but does not affect order correctness. Returning an
	// error here would cause the caller to treat order creation as failed and
	// retry, producing a duplicate order.
	if err := s.cart.ClearCart(ctx, userID); err != nil {
		s.logger.Warn("clear cart for user after order", "user_id", userID, "order_id", orderID, "error", err)
	}

	return orderID, nil
}

// ConfirmPayment transitions the order from "pending" to "paid". Returns
// ErrOrderStatusConflict if the order is already paid or in another terminal
// state, making this call idempotent and safe under concurrent webhooks.
func (s *OrderService) ConfirmPayment(ctx context.Context, orderID int64, method, paymentID string) error {
	return storage.WithinTransaction(ctx, s.orders.Conn(), func(ctx context.Context) error {
		// 1. Transition order status
		if err := s.orders.UpdateOrderStatus(ctx, orderID, storage.OrderStatusPending, storage.OrderStatusPaid, method, paymentID); err != nil {
			return err
		}

		// 2. Load order to get metadata (user_id, promo_code, items)
		order, err := s.orders.GetOrder(ctx, orderID)
		if err != nil {
			return fmt.Errorf("order service: confirm payment: get order: %w", err)
		}

		// 3. Decrement stock for each item
		for _, item := range order.Items {
			if err := s.products.DecrementStock(ctx, item.ProductID, item.Quantity); err != nil {
				return fmt.Errorf("order service: confirm payment: decrement stock: %w", err)
			}
		}

		// 4. Record promo usage if applicable
		if order.PromoCode != "" {
			promo, err := s.promos.GetPromoByCode(ctx, order.PromoCode)
			if err == nil {
				if err := s.promos.UsePromo(ctx, promo.ID, order.UserID, order.ID); err != nil {
					return fmt.Errorf("order service: confirm payment: use promo: %w", err)
				}
			} else if err != storage.ErrNotFound {
				return fmt.Errorf("order service: confirm payment: find promo: %w", err)
			}
		}

		return nil
	})
}

// GetOrder returns a single order by ID.
func (s *OrderService) GetOrder(ctx context.Context, orderID int64) (*storage.Order, error) {
	return s.orders.GetOrder(ctx, orderID)
}

// SetDelivered transitions the order from "paid" to "delivered" and returns the
// updated order. Returns ErrOrderStatusConflict if the order is not in "paid"
// status (e.g. already delivered, still pending, or cancelled).
func (s *OrderService) SetDelivered(ctx context.Context, orderID int64) (*storage.Order, error) {
	if err := s.orders.UpdateOrderStatus(ctx, orderID, storage.OrderStatusPaid, storage.OrderStatusDelivered, "", ""); err != nil {
		return nil, fmt.Errorf("order service: set delivered: %w", err)
	}
	return s.orders.GetOrder(ctx, orderID)
}

// GetUserOrders returns all orders for the given user.
func (s *OrderService) GetUserOrders(ctx context.Context, userID int64) ([]storage.Order, error) {
	return s.orders.GetUserOrders(ctx, userID)
}

// GetAllOrders returns all orders, optionally filtered by status.
func (s *OrderService) GetAllOrders(ctx context.Context, statusFilter string) ([]storage.Order, error) {
	return s.orders.GetAllOrders(ctx, statusFilter)
}

// CancelOrder cancels a pending order belonging to the given user.
// Returns ErrNotFound if the order is not found, belongs to another user, or is not pending.
func (s *OrderService) CancelOrder(ctx context.Context, orderID, userID int64) error {
	return s.orders.CancelOrder(ctx, orderID, userID)
}
