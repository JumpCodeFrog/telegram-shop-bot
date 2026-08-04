package shop

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"strconv"

	"shop_bot/internal/service"
	"shop_bot/internal/storage"
)

// Referral award policy for the first paid order of a referred user.
const (
	referrerBonusPoints      = 100
	referralPromoDiscountPct = 10
	referralPromoValidDays   = 30
	referralPromoPrefix      = "REF-"
)

// ErrInsufficientStock reports that a product cannot cover the requested
// quantity. It wraps storage.ErrProductOutOfStock so existing errors.Is checks
// keep working while callers gain the product name and counts for messaging.
type ErrInsufficientStock struct {
	ProductName string
	Have        int
	Want        int
}

func (e *ErrInsufficientStock) Error() string {
	return fmt.Sprintf("insufficient stock for %q: have %d, want %d", e.ProductName, e.Have, e.Want)
}

func (e *ErrInsufficientStock) Unwrap() error { return storage.ErrProductOutOfStock }

// PaymentOutcome describes everything that happened while confirming a
// payment. Sending user-facing messages based on it is the bot layer's job.
type PaymentOutcome struct {
	Order            *storage.Order
	PointsAwarded    int64
	NewLevel         string // "" = level unchanged
	ReferralReferrer int64  // referrer's Telegram ID; 0 = no referral bonus this time
	ReferrerPoints   int64
	NewUserPromo     string // "" = no personal promo issued
}

// LoyaltyEngine computes cashback and level upgrades (implemented by
// service.LoyaltyService).
type LoyaltyEngine interface {
	CalculateCashback(amountUSD float64, level string, isPremium bool) int
	CheckAndUpgradeLevel(ctx context.Context, userID int64, currentLevel string, totalPts int) (string, bool)
}

// LoyaltyPointsStore persists loyalty points (implemented by storage.LoyaltyStoreImpl).
type LoyaltyPointsStore interface {
	AddPoints(ctx context.Context, userID int64, pts int, reason string, refID string) error
	GetPoints(ctx context.Context, userID int64) (int, string, error)
}

// ReferralAwarder idempotently records the one-time first-purchase referral
// award (implemented by storage.ReferralStore).
type ReferralAwarder interface {
	AwardFirstPurchase(ctx context.Context, referredUserID, referrerID, points int64, promoCode string) (awarded bool, referrerTelegramID int64, err error)
}

// PersonalPromoIssuer issues single-use personal promo codes (implemented by
// storage.SQLPromoStore via PromoStore).
type PersonalPromoIssuer interface {
	CreatePersonal(ctx context.Context, code string, discountPct int, boundUserID int64, validDays int) error
}

// ProductCacheInvalidator drops cached product data after an out-of-band stock
// change. Implemented by storage.CachedProductStore (no-op without Redis).
type ProductCacheInvalidator interface {
	Invalidate(ctx context.Context, productIDs ...int64)
}

// PaymentDeps carries the optional dependencies ConfirmPayment uses for its
// post-payment side effects. The zero value disables all side effects, which
// keeps unit tests focused on the order state machine.
type PaymentDeps struct {
	Users     storage.UserStore
	Loyalty   LoyaltyEngine
	Points    LoyaltyPointsStore
	Referrals ReferralAwarder
	Promos    PersonalPromoIssuer
	Cache     ProductCacheInvalidator
	// Metrics is optional; when set, CreateFromCart increments OrdersCreated.
	Metrics *service.MetricsService
}

// OrderService provides business logic for managing orders.
type OrderService struct {
	orders   storage.OrderStore
	cart     storage.CartStore
	products storage.ProductStore
	payments PaymentDeps
	logger   *slog.Logger
}

// NewOrderService creates a new OrderService backed by the given stores.
// deps may be the zero value: then ConfirmPayment only flips the order status
// without loyalty/referral/cache side effects.
func NewOrderService(os storage.OrderStore, cs storage.CartStore, ps storage.ProductStore, deps PaymentDeps, logger *slog.Logger) *OrderService {
	return &OrderService{orders: os, cart: cs, products: ps, payments: deps, logger: logger}
}

// CreateFromCart creates a new order from the given CartView. If promo is
// non-nil, the discount is applied to the totals. The order is created with
// status "pending" and the user's cart is cleared afterwards.
// Returns ErrEmptyCart if the CartView has no items.
// Returns *ErrInsufficientStock (wrapping ErrProductOutOfStock) if any item
// cannot be covered by the current stock.
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
		if !p.IsActive || p.Stock < ci.Quantity {
			return 0, fmt.Errorf("order service: %w", &ErrInsufficientStock{ProductName: p.Name, Have: p.Stock, Want: ci.Quantity})
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

	if s.payments.Metrics != nil {
		s.payments.Metrics.OrdersCreated.Inc()
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

// ConfirmPayment transitions the order from "pending" to "paid" and applies
// the post-payment side effects: product cache invalidation, loyalty cashback
// with a possible level upgrade, and the one-time referral award for the
// buyer's first paid order. Returns ErrOrderStatusConflict if the order is
// already paid or in another terminal state, making this call idempotent and
// safe under concurrent webhooks: side effects run only in the single call
// that wins the status transition.
// Side-effect failures are logged, never returned — the payment itself is
// already confirmed, and the outcome reports only what actually happened.
func (s *OrderService) ConfirmPayment(ctx context.Context, orderID int64, method, paymentID string) (*PaymentOutcome, error) {
	if err := s.orders.UpdateOrderStatus(ctx, orderID, storage.OrderStatusPending, storage.OrderStatusPaid, method, paymentID); err != nil {
		return nil, err
	}

	order, err := s.orders.GetOrder(ctx, orderID)
	if err != nil {
		// Payment is confirmed but we cannot report on it. Surface the error:
		// a retry will hit ErrOrderStatusConflict and be treated as idempotent.
		return nil, fmt.Errorf("order service: load confirmed order %d: %w", orderID, err)
	}
	out := &PaymentOutcome{Order: order}

	if s.payments.Cache != nil && len(order.Items) > 0 {
		ids := make([]int64, len(order.Items))
		for i, it := range order.Items {
			ids[i] = it.ProductID
		}
		s.payments.Cache.Invalidate(ctx, ids...)
	}

	s.applyLoyaltyAndReferral(ctx, order, out)
	return out, nil
}

// applyLoyaltyAndReferral awards cashback points, checks for a level upgrade
// and applies the first-purchase referral bonus. Best-effort by design.
func (s *OrderService) applyLoyaltyAndReferral(ctx context.Context, order *storage.Order, out *PaymentOutcome) {
	d := s.payments
	if d.Users == nil || d.Points == nil || d.Loyalty == nil {
		return
	}

	user, err := d.Users.GetByTelegramID(ctx, order.UserID)
	if err != nil || user == nil {
		s.logger.Warn("confirm payment: buyer lookup failed, skipping loyalty/referral",
			"order_id", order.ID, "user_id", order.UserID, "error", err)
		return
	}
	orderRef := strconv.FormatInt(order.ID, 10)

	if pts := d.Loyalty.CalculateCashback(order.TotalUSD, user.LoyaltyLevel, user.IsPremium); pts > 0 {
		if err := d.Points.AddPoints(ctx, user.ID, pts, "purchase", orderRef); err != nil {
			s.logger.Error("confirm payment: add cashback points", "order_id", order.ID, "user_id", user.ID, "error", err)
		} else {
			out.PointsAwarded = int64(pts)
			if totalPts, level, err := d.Points.GetPoints(ctx, user.ID); err != nil {
				s.logger.Error("confirm payment: load points for level check", "user_id", user.ID, "error", err)
			} else if newLevel, upgraded := d.Loyalty.CheckAndUpgradeLevel(ctx, user.ID, level, totalPts); upgraded {
				out.NewLevel = newLevel
			}
		}
	}

	if user.ReferredBy == nil || d.Referrals == nil || d.Promos == nil {
		return
	}
	code, err := randomReferralPromoCode()
	if err != nil {
		s.logger.Error("confirm payment: generate referral promo code", "order_id", order.ID, "error", err)
		return
	}
	awarded, referrerTG, err := d.Referrals.AwardFirstPurchase(ctx, user.ID, *user.ReferredBy, referrerBonusPoints, code)
	if err != nil {
		s.logger.Error("confirm payment: referral award", "order_id", order.ID, "user_id", user.ID, "error", err)
		return
	}
	if !awarded {
		return // not the first paid order — bonus was granted earlier
	}
	out.ReferralReferrer = referrerTG
	out.ReferrerPoints = referrerBonusPoints
	if err := d.Points.AddPoints(ctx, *user.ReferredBy, referrerBonusPoints, "referral", orderRef); err != nil {
		s.logger.Error("confirm payment: add referrer points", "referrer_id", *user.ReferredBy, "error", err)
	}
	if err := d.Promos.CreatePersonal(ctx, code, referralPromoDiscountPct, order.UserID, referralPromoValidDays); err != nil {
		s.logger.Error("confirm payment: create personal promo", "order_id", order.ID, "user_id", order.UserID, "error", err)
	} else {
		out.NewUserPromo = code
	}
}

// refCodeAlphabet has 32 characters (no 0/O/1/I lookalikes); 256 % 32 == 0,
// so indexing random bytes modulo its length is bias-free.
const refCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// randomReferralPromoCode returns "REF-" + 8 crypto-random characters.
func randomReferralPromoCode() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i, b := range buf {
		buf[i] = refCodeAlphabet[int(b)%len(refCodeAlphabet)]
	}
	return referralPromoPrefix + string(buf), nil
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
