package shop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"pgregory.net/rapid"

	"shop_bot/internal/service"
	"shop_bot/internal/storage"
)

// lowStockProductStore reports a fixed stock level for every product.
type lowStockProductStore struct {
	isActiveProductStore
	stock int
}

func (s lowStockProductStore) GetProduct(_ context.Context, id int64) (*storage.Product, error) {
	return &storage.Product{ID: id, Name: "Widget", IsActive: true, Stock: s.stock}, nil
}

// TestCreateFromCart_InsufficientStock verifies C3: a product with stock below
// the requested quantity is rejected with a typed error carrying the product
// name and counts, still matching ErrProductOutOfStock for legacy callers.
func TestCreateFromCart_InsufficientStock(t *testing.T) {
	svc := NewOrderService(newMockOrderStore(), &mockClearCartStore{}, lowStockProductStore{stock: 2}, PaymentDeps{}, slog.Default())

	view := &CartView{
		Items: []CartItemView{{Product: storage.Product{ID: 1, Name: "Widget", PriceUSD: 5}, Quantity: 3}},
	}
	_, err := svc.CreateFromCart(context.Background(), 42, view, nil)

	var stockErr *ErrInsufficientStock
	if !errors.As(err, &stockErr) {
		t.Fatalf("expected *ErrInsufficientStock, got %v", err)
	}
	if stockErr.ProductName != "Widget" || stockErr.Have != 2 || stockErr.Want != 3 {
		t.Fatalf("unexpected error fields: %+v", stockErr)
	}
	if !errors.Is(err, storage.ErrProductOutOfStock) {
		t.Fatalf("expected error to wrap ErrProductOutOfStock, got %v", err)
	}
}

// TestCreateFromCart_StockCoversQuantity verifies the boundary: stock exactly
// equal to the requested quantity is accepted.
func TestCreateFromCart_StockCoversQuantity(t *testing.T) {
	svc := NewOrderService(newMockOrderStore(), &mockClearCartStore{}, lowStockProductStore{stock: 3}, PaymentDeps{}, slog.Default())

	view := &CartView{
		Items: []CartItemView{{Product: storage.Product{ID: 1, Name: "Widget", PriceUSD: 5}, Quantity: 3}},
	}
	if _, err := svc.CreateFromCart(context.Background(), 42, view, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// failingTB is the subset of testing.TB the helpers use. *rapid.T is not a
// testing.TB (it predates the Go 1.25+ TB additions), but it does provide
// these two methods, so both *testing.T and *rapid.T fit.
type failingTB interface {
	Helper()
	Fatalf(format string, args ...any)
}

// paymentTestEnv is a fully wired OrderService over a real SQLite database.
type paymentTestEnv struct {
	svc        *OrderService
	db         *storage.DB
	buyerTG    int64 // buyer's Telegram ID
	referrerTG int64 // referrer's Telegram ID
	buyerID    int64 // internal users.id
	referrerID int64 // internal users.id
	productID  int64
	orderID    int64
	stock      int
	qty        int
	totalUSD   float64
}

// newPaymentTestEnv seeds a referrer, a referred buyer, a product with the
// given stock and a pending order for qty units.
func newPaymentTestEnv(t failingTB, dir string, stock, qty int, priceUSD float64) *paymentTestEnv {
	t.Helper()
	ctx := context.Background()

	db, err := storage.New(filepath.Join(dir, "shop.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	conn := db.Conn()

	env := &paymentTestEnv{db: db, buyerTG: 1002, referrerTG: 1001, stock: stock, qty: qty}

	res, err := conn.Exec(`INSERT INTO users (telegram_id, username, first_name) VALUES (?, 'ref', 'Ref')`, env.referrerTG)
	if err != nil {
		t.Fatalf("seed referrer: %v", err)
	}
	env.referrerID, _ = res.LastInsertId()

	res, err = conn.Exec(`INSERT INTO users (telegram_id, username, first_name, referred_by) VALUES (?, 'buyer', 'Buyer', ?)`, env.buyerTG, env.referrerID)
	if err != nil {
		t.Fatalf("seed buyer: %v", err)
	}
	env.buyerID, _ = res.LastInsertId()

	if _, err := conn.Exec(`INSERT INTO categories (name) VALUES ('Test')`); err != nil {
		t.Fatalf("seed category: %v", err)
	}
	res, err = conn.Exec(`INSERT INTO products (category_id, name, price_usd, price_stars, stock, is_active) VALUES (1, 'Widget', ?, 10, ?, 1)`, priceUSD, stock)
	if err != nil {
		t.Fatalf("seed product: %v", err)
	}
	env.productID, _ = res.LastInsertId()

	orderStore := storage.NewSQLOrderStore(db)
	env.totalUSD = priceUSD * float64(qty)
	env.orderID, err = orderStore.CreateOrder(ctx, &storage.Order{
		UserID:     env.buyerTG,
		TotalUSD:   env.totalUSD,
		TotalStars: 10 * qty,
		Status:     storage.OrderStatusPending,
	}, []storage.OrderItem{{ProductID: env.productID, ProductName: "Widget", Quantity: qty, PriceUSD: priceUSD}})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}

	loyaltyStore := storage.NewLoyaltyStore(conn)
	env.svc = NewOrderService(orderStore, storage.NewCartStore(conn), storage.NewSQLProductStore(db), PaymentDeps{
		Users:     storage.NewUserStore(conn),
		Loyalty:   service.NewLoyaltyService(loyaltyStore, 1),
		Points:    loyaltyStore,
		Referrals: storage.NewReferralStore(conn),
		Promos:    storage.NewSQLPromoStore(db),
	}, slog.Default())
	return env
}

func (env *paymentTestEnv) countRow(t failingTB, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := env.db.Conn().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

// Feature: shop_bot v2, Task F1: параллельный двойной ConfirmPayment одного
// заказа даёт ровно одно списание стока, одно начисление баллов и один
// реферальный бонус.
func TestProperty_ConcurrentDoubleConfirmPayment(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		qty := rapid.IntRange(1, 5).Draw(rt, "qty")
		stock := qty + rapid.IntRange(0, 5).Draw(rt, "extraStock")
		priceUSD := float64(rapid.IntRange(1, 100).Draw(rt, "priceUSD"))

		env := newPaymentTestEnv(rt, t.TempDir(), stock, qty, priceUSD)
		defer env.db.Close()
		ctx := context.Background()

		var wg sync.WaitGroup
		outcomes := make([]*PaymentOutcome, 2)
		errs := make([]error, 2)
		for i := range 2 {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				outcomes[i], errs[i] = env.svc.ConfirmPayment(ctx, env.orderID, "stars", fmt.Sprintf("charge-%d", i))
			}(i)
		}
		wg.Wait()

		// Exactly one confirmation wins; the low-level compatibility API keeps
		// its CAS conflict contract. Receipt ingestion handles distinct facts.
		var winner *PaymentOutcome
		var nonWinners int
		for i := range 2 {
			switch {
			case errs[i] == nil:
				winner = outcomes[i]
			case errors.Is(errs[i], storage.ErrOrderStatusConflict):
				nonWinners++
			default:
				rt.Fatalf("unexpected error from ConfirmPayment[%d]: %v", i, errs[i])
			}
		}
		if winner == nil || nonWinners != 1 {
			rt.Fatalf("expected exactly one winner and one non-winner, got outcomes=%v errs=%v", outcomes, errs)
		}
		if winner == nil {
			return
		}

		// Stock decremented exactly once.
		if got := env.countRow(rt, `SELECT stock FROM products WHERE id = ?`, env.productID); got != int64(stock-qty) {
			rt.Fatalf("stock = %d, want %d", got, stock-qty)
		}
		// Exactly one cashback accrual for the buyer.
		if got := env.countRow(rt, `SELECT COUNT(*) FROM loyalty_txs WHERE user_id = ? AND reason = 'purchase'`, env.buyerID); got != 1 {
			rt.Fatalf("purchase loyalty_txs = %d, want 1", got)
		}
		wantPts := int64(env.totalUSD) // bronze: 1% of USD * 100 pts per USD
		if winner.PointsAwarded != wantPts {
			rt.Fatalf("PointsAwarded = %d, want %d", winner.PointsAwarded, wantPts)
		}
		// Exactly one referral award and one referrer accrual of 100 pts.
		if got := env.countRow(rt, `SELECT COUNT(*) FROM referral_awards WHERE referred_user_id = ?`, env.buyerID); got != 1 {
			rt.Fatalf("referral_awards rows = %d, want 1", got)
		}
		if got := env.countRow(rt, `SELECT loyalty_pts FROM users WHERE id = ?`, env.referrerID); got != 100 {
			rt.Fatalf("referrer loyalty_pts = %d, want 100", got)
		}
		if winner.ReferralReferrer != env.referrerTG || winner.ReferrerPoints != 100 {
			rt.Fatalf("referral outcome = (%d, %d), want (%d, 100)", winner.ReferralReferrer, winner.ReferrerPoints, env.referrerTG)
		}
		if !strings.HasPrefix(winner.NewUserPromo, "REF-") {
			rt.Fatalf("NewUserPromo = %q, want REF- prefix", winner.NewUserPromo)
		}
		// The personal promo is single-use, 10%, bound to the buyer's Telegram ID.
		promo, err := storage.NewSQLPromoStore(env.db).GetPromoByCode(ctx, winner.NewUserPromo)
		if err != nil {
			rt.Fatalf("personal promo lookup: %v", err)
		}
		if promo.Discount != 10 || promo.MaxUses != 1 || promo.BoundUserID == nil || *promo.BoundUserID != env.buyerTG {
			rt.Fatalf("personal promo = %+v, want 10%% single-use bound to %d", promo, env.buyerTG)
		}
	})
}

// TestConfirmPayment_SecondOrderNoReferralBonus verifies that only the first
// paid order triggers the referral award: a repeat purchase changes neither
// referral_awards nor the referrer's points.
func TestConfirmPayment_SecondOrderNoReferralBonus(t *testing.T) {
	env := newPaymentTestEnv(t, t.TempDir(), 10, 1, 20)
	defer env.db.Close()
	ctx := context.Background()

	first, err := env.svc.ConfirmPayment(ctx, env.orderID, "stars", "charge-1")
	if err != nil {
		t.Fatalf("first ConfirmPayment: %v", err)
	}
	if first.ReferralReferrer != env.referrerTG || first.NewUserPromo == "" {
		t.Fatalf("first order should award referral bonus, got %+v", first)
	}

	orderStore := storage.NewSQLOrderStore(env.db)
	secondID, err := orderStore.CreateOrder(ctx, &storage.Order{
		UserID:     env.buyerTG,
		TotalUSD:   20,
		TotalStars: 10,
		Status:     storage.OrderStatusPending,
	}, []storage.OrderItem{{ProductID: env.productID, ProductName: "Widget", Quantity: 1, PriceUSD: 20}})
	if err != nil {
		t.Fatalf("create second order: %v", err)
	}

	second, err := env.svc.ConfirmPayment(ctx, secondID, "stars", "charge-2")
	if err != nil {
		t.Fatalf("second ConfirmPayment: %v", err)
	}
	if second.ReferralReferrer != 0 || second.NewUserPromo != "" || second.ReferrerPoints != 0 {
		t.Fatalf("second order must not award referral bonus, got %+v", second)
	}
	if got := env.countRow(t, `SELECT COUNT(*) FROM referral_awards WHERE referred_user_id = ?`, env.buyerID); got != 1 {
		t.Fatalf("referral_awards rows = %d, want 1", got)
	}
	if got := env.countRow(t, `SELECT loyalty_pts FROM users WHERE id = ?`, env.referrerID); got != 100 {
		t.Fatalf("referrer loyalty_pts = %d, want 100", got)
	}
	// Cashback is still awarded for the second purchase.
	if second.PointsAwarded == 0 {
		t.Fatalf("second order should still award cashback points")
	}
}
