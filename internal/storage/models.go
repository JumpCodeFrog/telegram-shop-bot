package storage

import (
	"database/sql"
	"time"
)

const (
	OrderStatusPending   = "pending"
	OrderStatusPaid      = "paid"
	OrderStatusDelivered = "delivered"
	OrderStatusCancelled = "cancelled"

	PaymentMethodStars   = "stars"
	PaymentMethodCrypto  = "crypto"
	PaymentMethodBalance = "balance"
)

// addProductStep enumerates the steps of the "add product" dialog.
type AddProductStep int

const (
	StepName AddProductStep = iota
	StepDescription
	StepPriceUSD
	StepPriceStars
	StepStock
	StepPhoto
	StepCategory
)

// AddProductState holds the in-progress data for a multi-step add-product dialog.
type AddProductState struct {
	Step        AddProductStep `json:"step"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	PriceUSD    float64        `json:"price_usd"`
	PriceStars  int            `json:"price_stars"`
	Stock       int            `json:"stock"`
	PhotoURL    string         `json:"photo_url"`
	CreatedAt   time.Time      `json:"created_at"`
}

type User struct {
	ID           int64     `db:"id"`
	TelegramID   int64     `db:"telegram_id"`
	Username     string    `db:"username"`
	FirstName    string    `db:"first_name"`
	LanguageCode string    `db:"language_code"`
	IsPremium    bool      `db:"is_premium"`
	BalanceUSD   float64   `db:"balance_usd"`
	LoyaltyPts   int       `db:"loyalty_pts"`
	LoyaltyLevel string    `db:"loyalty_level"`
	ReferralCode sql.NullString `db:"referral_code"`
	ReferredBy   *int64    `db:"referred_by"`
	CreatedAt    time.Time `db:"created_at"`
}

type Category struct {
	ID            int64  `db:"id"`
	Name          string `db:"name"`
	Emoji         string `db:"emoji"`
	CustomEmojiID string `db:"custom_emoji_id"`
	SortOrder     int    `db:"sort_order"`
	IsActive      bool   `db:"is_active"`
}

type Product struct {
	ID             int64     `db:"id"`
	CategoryID     int64     `db:"category_id"`
	Name           string    `db:"name"`
	Description    string    `db:"description"`
	PhotoURL       string    `db:"photo_url"`
	PriceUSD       float64   `db:"price_usd"`
	PriceStars     int       `db:"price_stars"`
	Stock          int       `db:"stock"`
	IsDigital      bool      `db:"is_digital"`
	DigitalContent string    `db:"digital_content"`
	IsActive       bool      `db:"is_active"`
	CreatedAt      time.Time `db:"created_at"`
}

type CartItem struct {
	ID        int64     `db:"id"`
	UserID    int64     `db:"user_id"`
	ProductID int64     `db:"product_id"`
	Quantity  int       `db:"quantity"`
	AddedAt   time.Time `db:"added_at"`

	// Joined fields
	ProductName  string  `db:"product_name"`
	ProductPrice float64 `db:"product_price"`
}

type Order struct {
	ID            int64     `db:"id"`
	UserID        int64     `db:"user_id"`
	Status        string    `db:"status"` // pending|paid|cancelled
	TotalUSD      float64   `db:"total_usd"`
	TotalStars    int       `db:"total_stars"`
	PaymentMethod string    `db:"payment_method"`
	PaymentID     string    `db:"payment_id"`
	DiscountPct   int       `db:"discount_pct"`
	PromoCode     string    `db:"promo_code"`
	CreatedAt     time.Time `db:"created_at"`
	UpdatedAt     time.Time `db:"updated_at"`
	Items         []OrderItem
}

type OrderItem struct {
	ID          int64   `db:"id"`
	OrderID     int64   `db:"order_id"`
	ProductID   int64   `db:"product_id"`
	ProductName string  `db:"product_name"`
	Quantity    int     `db:"quantity"`
	PriceUSD    float64 `db:"price_usd"`
}

type PromoCode struct {
	ID         int64      `db:"id"`
	Code       string     `db:"code"`
	Discount   int        `db:"discount_pct"`
	MaxUses    int        `db:"max_uses"`
	UsedCount  int        `db:"used_count"`
	ExpiresAt  *time.Time `db:"expires_at"`
	CategoryID *int64     `db:"category_id"`
	IsActive   bool       `db:"is_active"`
	CreatedAt  time.Time  `db:"created_at"`
}

type Transaction struct {
	ID        int64     `db:"id"`
	UserID    int64     `db:"user_id"`
	AmountUSD float64   `db:"amount_usd"`
	Type      string    `db:"type"` // topup|payment|refund
	RefID     string    `db:"ref_id"`
	CreatedAt time.Time `db:"created_at"`
}

// Status mapping for display
var StatusDisplay = map[string]string{
	OrderStatusPending:   "⏳ Ожидает оплаты",
	OrderStatusPaid:      "✅ Оплачен",
	OrderStatusDelivered: "📦 Доставлен",
	OrderStatusCancelled: "❌ Отменён",
}
