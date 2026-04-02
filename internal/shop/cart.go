package shop

import (
	"context"
	"fmt"

	"shop_bot/internal/service"
	"shop_bot/internal/storage"
)

// CartView is an aggregated view of a user's cart with computed totals.
type CartView struct {
	Items      []CartItemView
	TotalUSD   float64
	TotalStars int
}

// CartItemView pairs a product with its quantity in the cart.
type CartItemView struct {
	Product  storage.Product
	Quantity int
}

// CartService provides business logic for managing a user's shopping cart.
type CartService struct {
	cart     storage.CartStore
	products storage.ProductStore
	exchange *service.ExchangeService
}

// NewCartService creates a new CartService backed by the given stores.
func NewCartService(cs storage.CartStore, ps storage.ProductStore, exchange ...*service.ExchangeService) *CartService {
	var ex *service.ExchangeService
	if len(exchange) > 0 {
		ex = exchange[0]
	}
	return &CartService{cart: cs, products: ps, exchange: ex}
}

// Add adds one unit of the given product to the user's cart.
// Returns ErrProductOutOfStock if the product is not available for purchase.
func (s *CartService) Add(ctx context.Context, userID, productID int64) error {
	p, err := s.products.GetProduct(ctx, productID)
	if err != nil {
		return err
	}
	if !(p.IsActive && p.Stock > 0) {
		return storage.ErrProductOutOfStock
	}
	return s.cart.AddItem(ctx, userID, productID)
}

// Get returns an aggregated view of the user's cart including product details
// and computed totals (TotalUSD and TotalStars).
func (s *CartService) Get(ctx context.Context, userID int64) (*CartView, error) {
	items, err := s.cart.GetItems(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("cart service: get items: %w", err)
	}

	view := &CartView{
		Items: make([]CartItemView, 0, len(items)),
	}

	for _, ci := range items {
		p, err := s.products.GetProduct(ctx, ci.ProductID)
		if err != nil {
			return nil, fmt.Errorf("cart service: get product %d: %w", ci.ProductID, err)
		}

		if s.exchange != nil {
			p.PriceStars = s.exchange.ConvertUSDToStars(p.PriceUSD)
		}

		view.Items = append(view.Items, CartItemView{
			Product:  *p,
			Quantity: ci.Quantity,
		})
		view.TotalUSD += p.PriceUSD * float64(ci.Quantity)
		view.TotalStars += p.PriceStars * ci.Quantity
	}

	return view, nil
}

// ChangeQuantity adjusts the quantity of a product in the user's cart by delta.
func (s *CartService) ChangeQuantity(ctx context.Context, userID, productID int64, delta int) error {
	if delta == 0 {
		return nil
	}

	items, err := s.cart.GetItems(ctx, userID)
	if err != nil {
		return fmt.Errorf("cart service: get items for quantity change: %w", err)
	}

	currentQty := 0
	for _, item := range items {
		if item.ProductID == productID {
			currentQty = item.Quantity
			break
		}
	}

	newQty := currentQty + delta
	if newQty <= 0 {
		return s.cart.RemoveItem(ctx, userID, productID)
	}

	if delta > 0 {
		p, err := s.products.GetProduct(ctx, productID)
		if err != nil {
			return fmt.Errorf("cart service: get product %d for quantity change: %w", productID, err)
		}
		if !(p.IsActive && p.Stock >= newQty) {
			return storage.ErrProductOutOfStock
		}
	}

	if currentQty == 0 {
		if err := s.cart.AddItem(ctx, userID, productID); err != nil {
			return fmt.Errorf("cart service: add item for quantity change: %w", err)
		}
		if newQty == 1 {
			return nil
		}
	}

	return s.cart.UpdateQuantity(ctx, userID, productID, newQty)
}

// Remove deletes a specific product from the user's cart.
func (s *CartService) Remove(ctx context.Context, userID, productID int64) error {
	return s.cart.RemoveItem(ctx, userID, productID)
}

// Clear removes all items from the user's cart.
func (s *CartService) Clear(ctx context.Context, userID int64) error {
	return s.cart.ClearCart(ctx, userID)
}
