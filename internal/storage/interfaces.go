package storage

import (
	"context"
	"time"
)

type UserStore interface {
	Upsert(ctx context.Context, user *User) error
	GetByTelegramID(ctx context.Context, telegramID int64) (*User, error)
}

type ProductStore interface {
	GetCategories(ctx context.Context) ([]Category, error)
	GetProductsByCategory(ctx context.Context, categoryID int64) ([]Product, error)
	GetProductsByCategoryPaged(ctx context.Context, categoryID int64, limit, offset int) ([]Product, int, error)
	GetProduct(ctx context.Context, id int64) (*Product, error)
	CreateProduct(ctx context.Context, p *Product) (int64, error)
	UpdateProduct(ctx context.Context, p *Product) error
	DeleteProduct(ctx context.Context, id int64) error
	SearchProducts(ctx context.Context, query string) ([]Product, error)
	CreateCategory(ctx context.Context, cat *Category) (int64, error)
	UpdateCategory(ctx context.Context, cat *Category) error
	DeleteCategory(ctx context.Context, id int64) error
	GetCategory(ctx context.Context, id int64) (*Category, error)
}

type CartStore interface {
	AddItem(ctx context.Context, userID, productID int64) error
	UpdateQuantity(ctx context.Context, userID, productID int64, quantity int) error
	RemoveItem(ctx context.Context, userID, productID int64) error
	ClearCart(ctx context.Context, userID int64) error
	GetAbandonedCarts(ctx context.Context, olderThan time.Duration) ([]int64, error)
	MarkRecoverySent(ctx context.Context, userID int64) error
	GetItems(ctx context.Context, userID int64) ([]CartItem, error)
	// CountActiveCarts returns the number of distinct users with items in cart.
	CountActiveCarts(ctx context.Context) (int64, error)
}

type OrderStore interface {
	CreateOrder(ctx context.Context, order *Order, items []OrderItem) (int64, error)
	GetOrder(ctx context.Context, id int64) (*Order, error)
	GetUserOrders(ctx context.Context, userID int64) ([]Order, error)
	GetAllOrders(ctx context.Context, statusFilter string) ([]Order, error)
	UpdateOrderStatus(ctx context.Context, id int64, fromStatus, status, paymentMethod, paymentID string) error
	CancelOrder(ctx context.Context, orderID, userID int64) error
}

type PromoStore interface {
	GetPromoByCode(ctx context.Context, code string) (*PromoCode, error)
	UsePromo(ctx context.Context, promoID, userID, orderID int64) error
	HasUserUsedPromo(ctx context.Context, promoID, userID int64) (bool, error)
	CreatePromo(ctx context.Context, p *PromoCode) (int64, error)
	// CreatePersonal issues a single-use promo bound to one Telegram user,
	// valid for validDays days from now.
	CreatePersonal(ctx context.Context, code string, discountPct int, boundUserID int64, validDays int) error
	ListPromos(ctx context.Context) ([]PromoCode, error)
	DeactivatePromo(ctx context.Context, id int64) error
}

type AnalyticsStore interface {
	GetRevenueSummary(ctx context.Context) (*RevenueSummary, error)
	GetRevenueByDays(ctx context.Context, days int) ([]DailyRevenue, error)
	GetTopProducts(ctx context.Context, limit int) ([]ProductStats, error)
	GetPaymentMethodStats(ctx context.Context) ([]PaymentMethodStat, error)
	TopBuyers(ctx context.Context, limit int) ([]TopBuyer, error)
	PromoUsage(ctx context.Context) ([]PromoUsageStat, error)
}

type ReviewStore interface {
	Upsert(ctx context.Context, r Review) error // ON CONFLICT(product_id,user_id) DO UPDATE rating,text
	ProductRating(ctx context.Context, productID int64) (avg float64, count int64, err error)
	ListByProduct(ctx context.Context, productID int64, limit int) ([]Review, error)
	ListRecent(ctx context.Context, limit int) ([]Review, error)
	Delete(ctx context.Context, id int64) error
}

type ProductPhotoStore interface {
	Add(ctx context.Context, productID int64, fileID string) error // max 10 → ErrTooManyPhotos
	List(ctx context.Context, productID int64) ([]ProductPhoto, error)
	Delete(ctx context.Context, id int64) error
}

type SubscriptionStore interface {
	Upsert(ctx context.Context, s Subscription) error // UNIQUE(user_id, product_id): продление двигает expires_at, сбрасывает reminded
	ListActiveByUser(ctx context.Context, userID int64) ([]Subscription, error)
	SetStatusByCharge(ctx context.Context, chargeID, status string) error
	DueForReminder(ctx context.Context, within time.Duration) ([]Subscription, error)
	MarkReminded(ctx context.Context, id int64) error
	ExpireOverdue(ctx context.Context) (int64, error)
}

// UISettingsStore is declared in ui_settings.go to keep all its code in one file.
