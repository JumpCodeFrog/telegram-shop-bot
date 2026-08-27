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

	PaymentMethodStars  = "stars"
	PaymentMethodCrypto = "crypto"
	// PaymentReviewProviderUnknown is a provider-neutral operator inbox for
	// legacy paid rows whose original payment rail cannot be established.
	PaymentReviewProviderUnknown = "unknown"

	OrderStatePlaced    = "placed"
	OrderStateCancelled = "cancelled"
	OrderStateCompleted = "completed"

	PaymentStatePending           = "pending"
	PaymentStateSettled           = "settled"
	PaymentStatePartiallyRefunded = "partially_refunded"
	PaymentStateRefunded          = "refunded"
	PaymentStateCancelled         = "cancelled"
	PaymentStateNeedsReview       = "needs_review"

	FulfillmentStateUnfulfilled = "unfulfilled"
	FulfillmentStateFulfilled   = "fulfilled"

	PaymentEventCaptured         = "captured"
	PaymentEventRefunded         = "refunded"
	PaymentEventChargeback       = "chargeback"
	PaymentEventIdentityConflict = "identity_conflict"

	PaymentDispositionObserved    = "observed"
	PaymentDispositionSettled     = "settled"
	PaymentDispositionNeedsReview = "needs_review"
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
	// StepSubType asks whether the product is a one-off purchase or a
	// 30-day Stars subscription. Appended last so persisted FSM states
	// serialized with older step numbers keep their meaning.
	StepSubType
)

// AddProductState holds the in-progress data for a multi-step add-product dialog.
// EditProductID != 0 means the dialog only adds photos to an existing product
// (photos are persisted directly, the other fields stay unused).
type AddProductState struct {
	Step          AddProductStep `json:"step"`
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	PriceUSD      float64        `json:"price_usd"`
	PriceStars    int            `json:"price_stars"`
	Stock         int            `json:"stock"`
	Photos        []string       `json:"photos"`
	EditProductID int64          `json:"edit_product_id"`
	CategoryID    int64          `json:"category_id"`
	// SubPeriodDays is 30 for a Stars subscription product, 0 for a regular one.
	SubPeriodDays int       `json:"sub_period_days"`
	CreatedAt     time.Time `json:"created_at"`
}

// ReviewState tracks a buyer who has rated a delivered order and may now
// attach an optional review text (or skip it).
type ReviewState struct {
	OrderID int64 `json:"order_id"`
	Rating  int   `json:"rating"`
}

type User struct {
	ID           int64          `db:"id"`
	TelegramID   int64          `db:"telegram_id"`
	Username     string         `db:"username"`
	FirstName    string         `db:"first_name"`
	LanguageCode string         `db:"language_code"`
	IsPremium    bool           `db:"is_premium"`
	BalanceUSD   float64        `db:"balance_usd"`
	LoyaltyPts   int            `db:"loyalty_pts"`
	LoyaltyLevel string         `db:"loyalty_level"`
	ReferralCode sql.NullString `db:"referral_code"`
	ReferredBy   *int64         `db:"referred_by"`
	CreatedAt    time.Time      `db:"created_at"`
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
	SubPeriodDays  int       `db:"sub_period_days"`
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
	ID                     int64     `db:"id"`
	UserID                 int64     `db:"user_id"`
	Status                 string    `db:"status"` // pending|paid|cancelled
	OrderState             string    `db:"order_state"`
	PaymentState           string    `db:"payment_state"`
	FulfillmentState       string    `db:"fulfillment_state"`
	TotalUSD               float64   `db:"total_usd"`
	TotalStars             int       `db:"total_stars"`
	PaymentMethod          string    `db:"payment_method"`
	PaymentID              string    `db:"payment_id"`
	DiscountPct            int       `db:"discount_pct"`
	PromoCode              string    `db:"promo_code"`
	SubscriptionProductID  int64     `db:"subscription_product_id"`
	SubscriptionPeriodDays int       `db:"subscription_period_days"`
	CreatedAt              time.Time `db:"created_at"`
	UpdatedAt              time.Time `db:"updated_at"`
	Items                  []OrderItem
}

// OrderEvent is an append-only business timeline entry. State columns on the
// order are the current projection; events explain how that projection arose.
type OrderEvent struct {
	ID         int64
	OrderID    int64
	EventType  string
	FromState  string
	ToState    string
	Metadata   string
	OccurredAt time.Time
}

type PaymentAttempt struct {
	ID                   int64
	OrderID              int64
	Provider             string
	ExternalID           string
	PayerID              int64
	AmountMinor          int64
	Currency             string
	Scale                int
	Status               string
	EntitlementExpiresAt sql.NullTime
	OccurredAt           time.Time
	CreatedAt            time.Time
}

// PaymentFact is the validated, non-secret provider capture written to the
// immutable ledger. Expected order money remains a separate comparison.
type PaymentFact struct {
	Provider             string
	ExternalID           string
	PayerID              int64
	AmountMinor          int64
	Currency             string
	Scale                int
	EntitlementExpiresAt time.Time
	OccurredAt           time.Time
}

type PaymentEvent struct {
	ID               int64
	OrderID          int64
	PaymentAttemptID sql.NullInt64
	Provider         string
	EventKind        string
	ExternalID       string
	AmountMinor      int64
	Currency         string
	Scale            int
	Disposition      string
	OccurredAt       time.Time
	CreatedAt        time.Time
}

// PaymentAnomaly preserves a normalized signed provider fact that cannot be
// safely attached to an order automatically.
type PaymentAnomaly struct {
	ID                int64
	Fingerprint       string
	ProposedOrderID   int64
	Provider          string
	EventKind         string
	ExternalID        string
	RelatedExternalID string
	PayerID           int64
	AmountMinor       int64
	Currency          string
	Scale             int
	RawAmount         string
	RawPayload        string
	Reason            string
	OccurredAt        time.Time
}

type Refund struct {
	ID                int64
	OrderID           int64
	Provider          string
	ExternalID        string
	PaymentExternalID string
	PayerID           int64
	AmountMinor       int64
	Currency          string
	Scale             int
	Status            string
	RequestedAt       time.Time
	CompletedAt       sql.NullTime
	CreatedAt         time.Time
	OccurredAt        time.Time
}

const (
	PaymentReviewTargetEvent   = "payment_event"
	PaymentReviewTargetAnomaly = "payment_anomaly"
	PaymentReviewTargetOrder   = "order"
)

// PaymentReviewTarget is an opaque local ledger row an operator must inspect
// and explicitly acknowledge. Provider identities and payloads stay hidden.
type PaymentReviewTarget struct {
	Kind       string
	ID         int64
	ReasonCode string
}

type PaymentReviewCase struct {
	OrderID          int64
	Provider         string
	PaymentState     string
	Targets          []PaymentReviewTarget
	RemainingTargets int
}

type PaymentReviewResolution struct {
	OrderID       int64
	Provider      string
	EventIDs      []int64
	AnomalyIDs    []int64
	OrderTargetID int64
	// Decision is required when an anomaly has no matching immutable provider
	// row, or when a provider-neutral legacy order is terminally cancelled.
	// Keeping it explicit prevents either case from becoming settled revenue.
	Decision              string
	Actor                 string
	Reason                string
	ResultingPaymentState string
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
	// BoundUserID is the Telegram user ID the promo is personally bound to.
	// nil = public promo. Bound promos must be rejected for any other user.
	BoundUserID *int64    `db:"bound_user_id"`
	IsActive    bool      `db:"is_active"`
	CreatedAt   time.Time `db:"created_at"`
}

// Status mapping for display
var StatusDisplay = map[string]string{
	OrderStatusPending:   "⏳ Ожидает оплаты",
	OrderStatusPaid:      "✅ Оплачен",
	OrderStatusDelivered: "📦 Доставлен",
	OrderStatusCancelled: "❌ Отменён",
}
