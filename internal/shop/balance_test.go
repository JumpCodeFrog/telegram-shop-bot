package shop

// ConfirmBalancePayment tests: the internal balance rail debits the buyer's
// USD balance and settles through the same fact path as the external rails.
// The storage layer owns no cross-store transaction (updateOrderStatusOnce
// opens and commits its own tx), so the service runs debit-then-settle and
// compensates the debit with a credit on ANY settle failure — including the
// already-paid conflict, which is how a concurrent double-tap can never
// double-charge the buyer.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"

	"shop_bot/internal/storage"
)

// newBalanceShopEnv builds a real-SQLite order service with the balance
// store wired, a credited buyer (telegram id 42) and one product.
func newBalanceShopEnv(t *testing.T, credit float64) (*OrderService, storage.BalanceStore, *storage.SQLOrderStore, *storage.DB, int64) {
	t.Helper()
	db, err := storage.New(filepath.Join(t.TempDir(), "shop-balance.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if _, err := db.Conn().ExecContext(ctx,
		`INSERT INTO users (telegram_id, username) VALUES (42, 'buyer')`); err != nil {
		t.Fatal(err)
	}
	balances := storage.NewSQLBalanceStore(db.Conn())
	if credit != 0 {
		if _, err := balances.AdjustBalance(ctx, 42, credit, "grant", 9001); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Conn().ExecContext(ctx, `INSERT INTO categories (name) VALUES ('bal')`); err != nil {
		t.Fatal(err)
	}
	res, err := db.Conn().ExecContext(ctx,
		`INSERT INTO products (category_id, name, price_usd, price_stars, stock, is_active)
		 VALUES (1, 'Gadget', 10.00, 500, 2, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	productID, _ := res.LastInsertId()
	orders := storage.NewSQLOrderStore(db)
	svc := NewOrderService(orders, storage.NewCartStore(db.Conn()), storage.NewSQLProductStore(db),
		PaymentDeps{Balances: balances}, slog.Default())
	return svc, balances, orders, db, productID
}

func seedBalanceOrder(t *testing.T, orders *storage.SQLOrderStore, productID int64, totalUSD float64) int64 {
	t.Helper()
	orderID, err := orders.CreateOrder(context.Background(), &storage.Order{
		UserID: 42, TotalUSD: totalUSD, Status: storage.OrderStatusPending,
	}, []storage.OrderItem{{ProductID: productID, Quantity: 1, PriceUSD: totalUSD}})
	if err != nil {
		t.Fatal(err)
	}
	return orderID
}

func TestConfirmBalancePaymentHappyPath(t *testing.T) {
	svc, balances, orders, db, productID := newBalanceShopEnv(t, 25.00)
	orderID := seedBalanceOrder(t, orders, productID, 10.00)
	ctx := context.Background()

	outcome, err := svc.ConfirmBalancePayment(ctx, orderID, 42)
	if err != nil {
		t.Fatalf("ConfirmBalancePayment: %v", err)
	}
	if outcome == nil || outcome.Order == nil || outcome.Order.Status != storage.OrderStatusPaid {
		t.Fatalf("outcome=%+v, want paid order", outcome)
	}

	// The buyer's balance dropped by exactly the order total.
	if got, _ := balances.GetBalance(ctx, 42); got != 15.00 {
		t.Fatalf("balance = %v, want 15.00", got)
	}

	// The settlement wrote the synthetic internal fact through the ledger:
	// provider balance, deterministic ExternalID, the buyer as positive
	// payer, USD cents at scale 2, marked succeeded via the settled event.
	var attempts int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts
		WHERE order_id=? AND provider='balance' AND external_id=? AND payer_id=42
		  AND amount_minor=1000 AND currency='USD' AND scale=2 AND status='succeeded'`,
		orderID, fmt.Sprintf("balance:%d", orderID)).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	var events int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events
		WHERE order_id=? AND provider='balance' AND disposition='settled'`, orderID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	var method, paymentID string
	if err := db.Conn().QueryRow(`SELECT payment_method, payment_id FROM orders WHERE id=?`, orderID).
		Scan(&method, &paymentID); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || events != 1 || method != storage.PaymentMethodBalance ||
		paymentID != fmt.Sprintf("balance:%d", orderID) {
		t.Fatalf("attempts=%d events=%d method=%q payment_id=%q", attempts, events, method, paymentID)
	}

	// The debit is audited in balance_txs; stock moved exactly once.
	var txs, stock int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM balance_txs
		WHERE amount_usd = -10.00 AND type = ?`, fmt.Sprintf("order_payment:%d", orderID)).Scan(&txs); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT stock FROM products WHERE id=?`, productID).Scan(&stock); err != nil {
		t.Fatal(err)
	}
	if txs != 1 || stock != 1 {
		t.Fatalf("balance_txs=%d stock=%d, want 1/1", txs, stock)
	}
}

func TestConfirmBalancePaymentReplayDoesNotDoubleDebit(t *testing.T) {
	svc, balances, orders, db, productID := newBalanceShopEnv(t, 25.00)
	orderID := seedBalanceOrder(t, orders, productID, 10.00)
	ctx := context.Background()

	if _, err := svc.ConfirmBalancePayment(ctx, orderID, 42); err != nil {
		t.Fatal(err)
	}
	// Second call: the pre-debit precondition (order still pending) fails, so
	// the conflict surfaces BEFORE any balance mutation.
	if _, err := svc.ConfirmBalancePayment(ctx, orderID, 42); !errors.Is(err, storage.ErrOrderStatusConflict) {
		t.Fatalf("replay err=%v, want ErrOrderStatusConflict", err)
	}
	if got, _ := balances.GetBalance(ctx, 42); got != 15.00 {
		t.Fatalf("balance after replay = %v, want still 15.00", got)
	}
	var attempts, debits int
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, orderID).Scan(&attempts)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM balance_txs WHERE amount_usd < 0`).Scan(&debits)
	if attempts != 1 || debits != 1 {
		t.Fatalf("attempts=%d debit txs=%d, want 1/1", attempts, debits)
	}
}

func TestConfirmBalancePaymentInsufficientFunds(t *testing.T) {
	svc, balances, orders, db, productID := newBalanceShopEnv(t, 5.00)
	orderID := seedBalanceOrder(t, orders, productID, 10.00)
	ctx := context.Background()

	if _, err := svc.ConfirmBalancePayment(ctx, orderID, 42); !errors.Is(err, storage.ErrInsufficientFunds) {
		t.Fatalf("err=%v, want ErrInsufficientFunds", err)
	}
	if got, _ := balances.GetBalance(ctx, 42); got != 5.00 {
		t.Fatalf("balance = %v, want unchanged 5.00", got)
	}
	order, _ := svc.GetOrder(ctx, orderID)
	if order.Status != storage.OrderStatusPending {
		t.Fatalf("order status = %q, want still pending", order.Status)
	}
	var attempts, events, txs int
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, orderID).Scan(&attempts)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events WHERE order_id=?`, orderID).Scan(&events)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM balance_txs WHERE amount_usd < 0`).Scan(&txs)
	if attempts != 0 || events != 0 || txs != 0 {
		t.Fatalf("attempts=%d events=%d debit txs=%d, want 0/0/0", attempts, events, txs)
	}
}

func TestConfirmBalancePaymentRejectsNonPendingAndForeignOrders(t *testing.T) {
	svc, balances, orders, _, productID := newBalanceShopEnv(t, 25.00)
	ctx := context.Background()

	orderID := seedBalanceOrder(t, orders, productID, 10.00)
	// Foreign buyer: the order belongs to telegram user 42.
	if _, err := svc.ConfirmBalancePayment(ctx, orderID, 43); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("foreign buyer err=%v, want ErrNotFound", err)
	}
	// Cancelled order.
	if err := svc.CancelOrder(ctx, orderID, 42); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfirmBalancePayment(ctx, orderID, 42); !errors.Is(err, storage.ErrOrderStatusConflict) {
		t.Fatalf("cancelled order err=%v, want ErrOrderStatusConflict", err)
	}
	if got, _ := balances.GetBalance(ctx, 42); got != 25.00 {
		t.Fatalf("balance = %v, want unchanged 25.00", got)
	}
}

func TestConfirmBalancePaymentRejectsSubscriptionOrders(t *testing.T) {
	svc, balances, orders, _, productID := newBalanceShopEnv(t, 25.00)
	ctx := context.Background()

	// Subscriptions settle through the Stars entitlement path only; the
	// balance rail must refuse them before touching money.
	orderID, err := orders.CreateOrder(ctx, &storage.Order{
		UserID: 42, TotalUSD: 2.00, TotalStars: 100, Status: storage.OrderStatusPending,
		SubscriptionProductID: productID, SubscriptionPeriodDays: 30,
	}, []storage.OrderItem{{ProductID: productID, Quantity: 1, PriceUSD: 2.00}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfirmBalancePayment(ctx, orderID, 42); !errors.Is(err, ErrBalanceSubscriptionUnsupported) {
		t.Fatalf("subscription order err=%v, want ErrBalanceSubscriptionUnsupported", err)
	}
	if got, _ := balances.GetBalance(ctx, 42); got != 25.00 {
		t.Fatalf("balance = %v, want unchanged 25.00", got)
	}
}

func TestConfirmBalancePaymentCompensatesOnSettleFailure(t *testing.T) {
	svc, balances, orders, db, productID := newBalanceShopEnv(t, 25.00)
	ctx := context.Background()

	// Exhaust the stock AFTER order creation so the settle transaction fails
	// inside updateOrderStatusOnce (stock guard) — after the debit happened.
	orderID := seedBalanceOrder(t, orders, productID, 10.00)
	if _, err := db.Conn().ExecContext(ctx, `UPDATE products SET stock = 0 WHERE id = ?`, productID); err != nil {
		t.Fatal(err)
	}

	_, err := svc.ConfirmBalancePayment(ctx, orderID, 42)
	if !errors.Is(err, storage.ErrProductOutOfStock) {
		t.Fatalf("settle failure err=%v, want ErrProductOutOfStock", err)
	}
	// The debit was compensated by an exact credit: balance unchanged.
	if got, _ := balances.GetBalance(ctx, 42); got != 25.00 {
		t.Fatalf("balance after compensation = %v, want 25.00", got)
	}
	// Both legs are audited: the debit and the settlement_failed credit.
	var debitTxs, creditTxs int
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM balance_txs WHERE amount_usd = -10.00 AND type = ?`,
		fmt.Sprintf("order_payment:%d", orderID)).Scan(&debitTxs)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM balance_txs WHERE amount_usd = 10.00 AND type = ?`,
		fmt.Sprintf("settlement_failed:%d", orderID)).Scan(&creditTxs)
	if debitTxs != 1 || creditTxs != 1 {
		t.Fatalf("debit txs=%d compensation txs=%d, want 1/1", debitTxs, creditTxs)
	}
	order, _ := svc.GetOrder(ctx, orderID)
	if order.Status != storage.OrderStatusPending {
		t.Fatalf("order status = %q, want still pending after compensation", order.Status)
	}
	var attempts int
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, orderID).Scan(&attempts)
	if attempts != 0 {
		t.Fatalf("attempts=%d, want 0 (the settle tx rolled back)", attempts)
	}
}

// TestConfirmBalancePaymentCompensatesOnConflictAfterDebit pins the
// double-tap race with pure mocks: the precondition snapshot passes, the
// debit lands, then the settle loses the CAS (a concurrent rail won) — the
// buyer must be credited back exactly once.
func TestConfirmBalancePaymentCompensatesOnConflictAfterDebit(t *testing.T) {
	orders := &conflictSettleOrderStore{mockOrderStore: newMockOrderStore()}
	orders.orders[7] = &storage.Order{ID: 7, UserID: 42, Status: storage.OrderStatusPending, TotalUSD: 10.00}
	balances := &mockBalanceStore{balance: 25.00}
	svc := NewOrderService(orders, &mockCartStore{}, &mockProductStore{},
		PaymentDeps{Balances: balances}, slog.Default())

	if _, err := svc.ConfirmBalancePayment(context.Background(), 7, 42); !errors.Is(err, storage.ErrOrderStatusConflict) {
		t.Fatalf("err=%v, want ErrOrderStatusConflict", err)
	}
	if len(balances.adjusts) != 2 || balances.adjusts[0] != -10.00 || balances.adjusts[1] != 10.00 {
		t.Fatalf("adjusts=%v, want [-10 +10]", balances.adjusts)
	}
	if balances.balance != 25.00 {
		t.Fatalf("balance = %v, want compensated 25.00", balances.balance)
	}
}

// conflictSettleOrderStore loses the settle CAS every time while still
// serving the pending order snapshot (the double-tap race).
type conflictSettleOrderStore struct {
	*mockOrderStore
}

func (c *conflictSettleOrderStore) UpdateOrderStatus(_ context.Context, _ int64, _, _, _, _ string) error {
	return storage.ErrOrderStatusConflict
}

// orderBalanceTxSum returns the order's net ledger effect: the signed sum
// of its order_payment debits and settlement_failed compensations.
func orderBalanceTxSum(t *testing.T, db *storage.DB, orderID int64) float64 {
	t.Helper()
	var sum float64
	if err := db.Conn().QueryRow(`SELECT COALESCE(SUM(amount_usd), 0) FROM balance_txs
		WHERE type IN (?, ?)`,
		fmt.Sprintf("order_payment:%d", orderID), fmt.Sprintf("settlement_failed:%d", orderID)).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	return sum
}

// TestConfirmBalancePaymentOrphanDebitRetapSkipsSecondDebit pins the crash
// window: a prior tap debited and the process died before settle, leaving
// the order pending with an orphan debit that every pre-debit guard still
// passes. The re-tap must settle on the orphan debit, not debit a second
// time.
func TestConfirmBalancePaymentOrphanDebitRetapSkipsSecondDebit(t *testing.T) {
	svc, balances, orders, db, productID := newBalanceShopEnv(t, 25.00)
	orderID := seedBalanceOrder(t, orders, productID, 10.00)
	ctx := context.Background()

	// The orphan debit: same row shape the production debit writes.
	if _, err := balances.AdjustBalance(ctx, 42, -10.00, fmt.Sprintf("order_payment:%d", orderID), 0); err != nil {
		t.Fatal(err)
	}

	outcome, err := svc.ConfirmBalancePayment(ctx, orderID, 42)
	if err != nil {
		t.Fatalf("ConfirmBalancePayment: %v", err)
	}
	if outcome == nil || outcome.Order == nil || outcome.Order.Status != storage.OrderStatusPaid {
		t.Fatalf("outcome=%+v, want paid order", outcome)
	}
	if got := orderBalanceTxSum(t, db, orderID); got != -10.00 {
		t.Fatalf("order net balance txs = %v, want -10.00 (single debit)", got)
	}
	if got, _ := balances.GetBalance(ctx, 42); got != 15.00 {
		t.Fatalf("balance = %v, want 15.00", got)
	}
	var attempts int
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, orderID).Scan(&attempts)
	if attempts != 1 {
		t.Fatalf("attempts=%d, want 1", attempts)
	}
}

// TestConfirmBalancePaymentCompensatedRetapDebitsAgain: a debit that was
// compensated returned the money legitimately, so a later re-tap must
// debit again — net: one paid order, one live debit.
func TestConfirmBalancePaymentCompensatedRetapDebitsAgain(t *testing.T) {
	svc, balances, orders, db, productID := newBalanceShopEnv(t, 25.00)
	orderID := seedBalanceOrder(t, orders, productID, 10.00)
	ctx := context.Background()

	if _, err := balances.AdjustBalance(ctx, 42, -10.00, fmt.Sprintf("order_payment:%d", orderID), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := balances.AdjustBalance(ctx, 42, 10.00, fmt.Sprintf("settlement_failed:%d", orderID), 0); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.ConfirmBalancePayment(ctx, orderID, 42); err != nil {
		t.Fatalf("ConfirmBalancePayment: %v", err)
	}
	// Two debits + one compensation: exactly one live debit for the paid order.
	if got := orderBalanceTxSum(t, db, orderID); got != -10.00 {
		t.Fatalf("order net balance txs = %v, want -10.00 (one live debit)", got)
	}
	var debitTxs int
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM balance_txs WHERE amount_usd = -10.00 AND type = ?`,
		fmt.Sprintf("order_payment:%d", orderID)).Scan(&debitTxs)
	if debitTxs != 2 {
		t.Fatalf("debit txs=%d, want 2 (orphan pair + re-tap)", debitTxs)
	}
	if got, _ := balances.GetBalance(ctx, 42); got != 15.00 {
		t.Fatalf("balance = %v, want 15.00", got)
	}
	order, _ := svc.GetOrder(ctx, orderID)
	if order.Status != storage.OrderStatusPaid {
		t.Fatalf("order status = %q, want paid", order.Status)
	}
}

// TestConfirmBalancePaymentDoubleOrphanRetapSettlesOnce: two crash loops
// left two orphan debits (net -20); the re-tap settles once and debits
// nothing more.
func TestConfirmBalancePaymentDoubleOrphanRetapSettlesOnce(t *testing.T) {
	svc, balances, orders, db, productID := newBalanceShopEnv(t, 25.00)
	orderID := seedBalanceOrder(t, orders, productID, 10.00)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := balances.AdjustBalance(ctx, 42, -10.00, fmt.Sprintf("order_payment:%d", orderID), 0); err != nil {
			t.Fatal(err)
		}
	}

	outcome, err := svc.ConfirmBalancePayment(ctx, orderID, 42)
	if err != nil {
		t.Fatalf("ConfirmBalancePayment: %v", err)
	}
	if outcome == nil || outcome.Order == nil || outcome.Order.Status != storage.OrderStatusPaid {
		t.Fatalf("outcome=%+v, want paid order", outcome)
	}
	if got := orderBalanceTxSum(t, db, orderID); got != -20.00 {
		t.Fatalf("order net balance txs = %v, want -20.00 (no third debit)", got)
	}
	var debitTxs int
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM balance_txs WHERE amount_usd < 0 AND type = ?`,
		fmt.Sprintf("order_payment:%d", orderID)).Scan(&debitTxs)
	if debitTxs != 2 {
		t.Fatalf("debit txs=%d, want 2 (the two orphans, no third)", debitTxs)
	}
	if got, _ := balances.GetBalance(ctx, 42); got != 5.00 {
		t.Fatalf("balance = %v, want 5.00", got)
	}
}

func TestConfirmBalancePaymentRequiresBalanceStore(t *testing.T) {
	orders := newMockOrderStore()
	orders.orders[7] = &storage.Order{ID: 7, UserID: 42, Status: storage.OrderStatusPending, TotalUSD: 10.00}
	svc := NewOrderService(orders, &mockCartStore{}, &mockProductStore{}, PaymentDeps{}, slog.Default())
	if _, err := svc.ConfirmBalancePayment(context.Background(), 7, 42); !errors.Is(err, ErrBalanceUnavailable) {
		t.Fatalf("err=%v, want ErrBalanceUnavailable", err)
	}
}

// mockBalanceStore is a minimal in-memory BalanceStore for shop unit tests.
type mockBalanceStore struct {
	balance   float64
	adjusts   []float64
	net       float64
	txTypes   map[string]bool
	getErr    error
	adjustErr error
	netErr    error
}

func (m *mockBalanceStore) BalanceTxExists(_ context.Context, _ int64, txType string) (bool, error) {
	return m.txTypes[txType], nil
}

func (m *mockBalanceStore) OrderBalanceNet(_ context.Context, _, _ int64) (float64, error) {
	return m.net, m.netErr
}

func (m *mockBalanceStore) GetBalance(_ context.Context, _ int64) (float64, error) {
	return m.balance, m.getErr
}

func (m *mockBalanceStore) AdjustBalance(_ context.Context, _ int64, deltaUSD float64, _ string, _ int64) (float64, error) {
	if m.adjustErr != nil {
		return m.balance, m.adjustErr
	}
	m.balance += deltaUSD
	m.adjusts = append(m.adjusts, deltaUSD)
	return m.balance, nil
}
