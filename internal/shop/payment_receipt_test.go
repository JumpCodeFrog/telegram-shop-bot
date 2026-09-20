package shop

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"shop_bot/internal/storage"
)

func TestConfirmPaymentReceiptRejectsStarsMismatchWithoutMutation(t *testing.T) {
	orders := &mockOrderStore{orders: map[int64]*storage.Order{
		7: {ID: 7, UserID: 42, Status: storage.OrderStatusPending, TotalStars: 100},
	}}
	svc := NewOrderService(orders, &mockCartStore{}, &mockProductStore{}, PaymentDeps{}, slog.Default())
	tests := []PaymentReceipt{
		{OrderID: 7, Provider: "stars", ExternalID: "charge", PayerID: 99, Currency: "XTR", AmountMinor: 100, Scale: 0},
		{OrderID: 7, Provider: "stars", ExternalID: "charge", PayerID: 42, Currency: "USD", AmountMinor: 100, Scale: 0},
		{OrderID: 7, Provider: "stars", ExternalID: "charge", PayerID: 42, Currency: "XTR", AmountMinor: 99, Scale: 0},
		{OrderID: 7, Provider: "stars", ExternalID: "", PayerID: 42, Currency: "XTR", AmountMinor: 100, Scale: 0},
	}
	for _, receipt := range tests {
		if _, err := svc.ConfirmPaymentReceipt(context.Background(), receipt); !errors.Is(err, storage.ErrPaymentReceiptMismatch) {
			t.Fatalf("receipt=%+v err=%v", receipt, err)
		}
	}
	if orders.orders[7].Status != storage.OrderStatusPending {
		t.Fatalf("order mutated: %+v", orders.orders[7])
	}
}

func TestConfirmPaymentReceiptRejectsZeroAmount(t *testing.T) {
	orders := &mockOrderStore{orders: map[int64]*storage.Order{7: {ID: 7, UserID: 42, Status: storage.OrderStatusPending, PaymentState: storage.PaymentStatePending, TotalStars: 0}}}
	svc := NewOrderService(orders, &mockCartStore{}, &mockProductStore{}, PaymentDeps{}, slog.Default())
	_, err := svc.ConfirmPaymentReceipt(context.Background(), PaymentReceipt{OrderID: 7, Provider: "stars", ExternalID: "zero", PayerID: 42, Currency: "XTR", AmountMinor: 0})
	if !errors.Is(err, storage.ErrPaymentReceiptMismatch) || orders.orders[7].Status != storage.OrderStatusPending {
		t.Fatalf("error=%v order=%+v", err, orders.orders[7])
	}
}

func TestMismatchedProviderReceiptIsDurablyQuarantinedWithActualFacts(t *testing.T) {
	db, err := storage.New(filepath.Join(t.TempDir(), "receipt-anomaly.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := storage.NewSQLOrderStore(db)
	orderID, err := store.CreateOrder(context.Background(), &storage.Order{
		UserID: 42, TotalStars: 100, Status: storage.OrderStatusPending,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewOrderService(store, storage.NewCartStore(db.Conn()), storage.NewSQLProductStore(db), PaymentDeps{}, slog.Default())
	receipt := PaymentReceipt{
		OrderID: orderID, Provider: "stars", ExternalID: "wrong-amount",
		PayerID: 42, Currency: "XTR", AmountMinor: 77, Scale: 0,
	}
	if _, err := svc.ConfirmPaymentReceipt(context.Background(), receipt); !errors.Is(err, storage.ErrPaymentNeedsReview) {
		t.Fatalf("error=%v", err)
	}
	var amount, payer, anomalies, attempts int64
	var currency string
	if err := db.Conn().QueryRow(`
		SELECT amount_minor, payer_id, currency FROM payment_anomalies
		WHERE provider='stars' AND external_id='wrong-amount'`).Scan(&amount, &payer, &currency); err != nil {
		t.Fatal(err)
	}
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies`).Scan(&anomalies)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, orderID).Scan(&attempts)
	order, _ := store.GetOrder(context.Background(), orderID)
	if amount != 77 || payer != 42 || currency != "XTR" || anomalies != 1 || attempts != 0 ||
		order.Status != storage.OrderStatusPending || order.PaymentState != storage.PaymentStateNeedsReview {
		t.Fatalf("amount=%d payer=%d currency=%s anomalies=%d attempts=%d order=%+v", amount, payer, currency, anomalies, attempts, order)
	}
	// An exact provider retry is a durable no-op.
	if _, err := svc.ConfirmPaymentReceipt(context.Background(), receipt); !errors.Is(err, storage.ErrPaymentNeedsReview) {
		t.Fatalf("retry error=%v", err)
	}
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies`).Scan(&anomalies)
	if anomalies != 1 {
		t.Fatalf("anomalies after retry=%d", anomalies)
	}
}

func TestUnknownOrderReceiptIsDurablyQuarantined(t *testing.T) {
	db, err := storage.New(filepath.Join(t.TempDir(), "orphan-receipt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := storage.NewSQLOrderStore(db)
	svc := NewOrderService(store, storage.NewCartStore(db.Conn()), storage.NewSQLProductStore(db), PaymentDeps{}, slog.Default())
	receipt := PaymentReceipt{
		OrderID: 999, Provider: "crypto", ExternalID: "orphan-invoice",
		Currency: "USDT", AmountMinor: 1234, Scale: 2,
	}
	if _, err := svc.ConfirmPaymentReceipt(context.Background(), receipt); !errors.Is(err, storage.ErrPaymentNeedsReview) {
		t.Fatalf("error=%v", err)
	}
	var proposed, amount int64
	if err := db.Conn().QueryRow(`
		SELECT proposed_order_id, amount_minor FROM payment_anomalies
		WHERE provider='crypto' AND external_id='orphan-invoice'`).Scan(&proposed, &amount); err != nil {
		t.Fatal(err)
	}
	if proposed != 999 || amount != 1234 {
		t.Fatalf("proposed=%d amount=%d", proposed, amount)
	}
}

func TestValidateSubscriptionCart(t *testing.T) {
	sub := storage.Product{ID: 1, SubPeriodDays: 30}
	regular := storage.Product{ID: 2}
	valid := &CartView{Items: []CartItemView{{Product: sub, Quantity: 1}}}
	if err := ValidateSubscriptionCart(valid); err != nil {
		t.Fatal(err)
	}
	for _, view := range []*CartView{
		{Items: []CartItemView{{Product: sub, Quantity: 2}}},
		{Items: []CartItemView{{Product: sub, Quantity: 1}, {Product: regular, Quantity: 1}}},
	} {
		if err := ValidateSubscriptionCart(view); !errors.Is(err, storage.ErrInvalidSubscriptionCart) {
			t.Fatalf("view=%+v error=%v", view, err)
		}
	}
}

func TestConcurrentDistinctReceiptsAreBothDurable(t *testing.T) {
	db, err := storage.New(filepath.Join(t.TempDir(), "distinct-receipts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.Conn().Exec(`INSERT INTO categories (name) VALUES ('test')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`INSERT INTO products (category_id,name,price_usd,price_stars,stock,is_active) VALUES (1,'item',1,10,10,1)`); err != nil {
		t.Fatal(err)
	}
	store := storage.NewSQLOrderStore(db)
	orderID, err := store.CreateOrder(ctx, &storage.Order{UserID: 42, TotalStars: 10, Status: storage.OrderStatusPending}, []storage.OrderItem{{ProductID: 1, ProductName: "item", Quantity: 1}})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewOrderService(store, storage.NewCartStore(db.Conn()), storage.NewSQLProductStore(db), PaymentDeps{}, slog.Default())
	receipts := []PaymentReceipt{
		{OrderID: orderID, Provider: "stars", ExternalID: "charge-a", PayerID: 42, Currency: "XTR", AmountMinor: 10},
		{OrderID: orderID, Provider: "stars", ExternalID: "charge-b", PayerID: 42, Currency: "XTR", AmountMinor: 10},
	}
	var wg sync.WaitGroup
	errs := make([]error, len(receipts))
	for i := range receipts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = svc.ConfirmPaymentReceipt(ctx, receipts[i])
		}(i)
	}
	wg.Wait()
	success, review := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			success++
		case errors.Is(err, storage.ErrPaymentNeedsReview):
			review++
		default:
			t.Fatalf("unexpected errors = %v", errs)
		}
	}
	var attempts int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id = ?`, orderID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	order, err := store.GetOrder(ctx, orderID)
	if err != nil {
		t.Fatal(err)
	}
	if success != 1 || review != 1 || attempts != 2 || order.PaymentState != storage.PaymentStateNeedsReview {
		t.Fatalf("success=%d review=%d attempts=%d order=%+v errs=%v", success, review, attempts, order, errs)
	}
}

func TestProviderCaptureAfterCancellationIsDurable(t *testing.T) {
	db, err := storage.New(filepath.Join(t.TempDir(), "capture-after-cancel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := storage.NewSQLOrderStore(db)
	ctx := context.Background()
	orderID, err := store.CreateOrder(ctx, &storage.Order{UserID: 42, TotalStars: 10, Status: storage.OrderStatusPending}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CancelOrder(ctx, orderID, 42); err != nil {
		t.Fatal(err)
	}
	svc := NewOrderService(store, storage.NewCartStore(db.Conn()), storage.NewSQLProductStore(db), PaymentDeps{}, slog.Default())
	_, err = svc.ConfirmPaymentReceipt(ctx, PaymentReceipt{OrderID: orderID, Provider: "stars", ExternalID: "late-charge", PayerID: 42, Currency: "XTR", AmountMinor: 10})
	if !errors.Is(err, storage.ErrPaymentNeedsReview) {
		t.Fatalf("error=%v", err)
	}
	var attempts int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=? AND external_id='late-charge'`, orderID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	order, _ := store.GetOrder(ctx, orderID)
	if attempts != 1 || order.Status != storage.OrderStatusCancelled || order.PaymentState != storage.PaymentStateNeedsReview {
		t.Fatalf("attempts=%d order=%+v", attempts, order)
	}
}

func TestNeedsReviewOrderCannotAutoSettleOnAnotherCapture(t *testing.T) {
	db, err := storage.New(filepath.Join(t.TempDir(), "unresolved-capture.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	store := storage.NewSQLOrderStore(db)
	orderID, err := store.CreateOrder(ctx, &storage.Order{UserID: 42, TotalStars: 10, Status: storage.OrderStatusPending}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUnexpectedPayment(ctx, orderID, "stars", "first-unresolved", "provider_confirmed"); !errors.Is(err, storage.ErrPaymentNeedsReview) {
		t.Fatal(err)
	}
	svc := NewOrderService(store, storage.NewCartStore(db.Conn()), storage.NewSQLProductStore(db), PaymentDeps{}, slog.Default())
	_, err = svc.ConfirmPaymentReceipt(ctx, PaymentReceipt{OrderID: orderID, Provider: "stars", ExternalID: "second", PayerID: 42, Currency: "XTR", AmountMinor: 10})
	if !errors.Is(err, storage.ErrPaymentNeedsReview) {
		t.Fatalf("error=%v", err)
	}
	var attempts int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, orderID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	order, _ := store.GetOrder(ctx, orderID)
	if attempts != 2 || order.Status != storage.OrderStatusPending || order.PaymentState != storage.PaymentStateNeedsReview {
		t.Fatalf("attempts=%d order=%+v", attempts, order)
	}
}

type renewalOrderStore struct {
	*mockOrderStore
	calls int
	err   error
	fact  storage.PaymentFact
}

func (s *renewalOrderStore) RecordSubscriptionRenewal(_ context.Context, orderID int64, provider, externalID string, sub storage.Subscription) error {
	s.calls++
	return s.err
}

func (s *renewalOrderStore) RecordSubscriptionRenewalFact(_ context.Context, _ int64, fact storage.PaymentFact, _ storage.Subscription) error {
	s.calls++
	s.fact = fact
	return s.err
}

func TestRecordSubscriptionRenewalValidatesAndDelegates(t *testing.T) {
	orders := &renewalOrderStore{mockOrderStore: &mockOrderStore{orders: map[int64]*storage.Order{
		7: {ID: 7, UserID: 42, Status: storage.OrderStatusPaid, PaymentState: storage.PaymentStateSettled, TotalStars: 100, SubscriptionProductID: 3, SubscriptionPeriodDays: 30},
	}}}
	svc := NewOrderService(orders, &mockCartStore{}, &mockProductStore{}, PaymentDeps{}, slog.Default())
	receipt := PaymentReceipt{OrderID: 7, Provider: "stars", ExternalID: "renewal", PayerID: 42, Currency: "XTR", AmountMinor: 100, Scale: 0, SubscriptionExpiresAt: time.Now().Add(30 * 24 * time.Hour)}
	order, err := svc.RecordSubscriptionRenewal(context.Background(), receipt)
	if err != nil || order == nil || orders.calls != 1 || orders.fact.Provider != storage.PaymentMethodStars ||
		orders.fact.ExternalID != receipt.ExternalID || orders.fact.AmountMinor != receipt.AmountMinor ||
		orders.fact.Currency != receipt.Currency || orders.fact.Scale != receipt.Scale ||
		!orders.fact.EntitlementExpiresAt.Equal(receipt.SubscriptionExpiresAt) {
		t.Fatalf("order=%+v calls=%d err=%v", order, orders.calls, err)
	}
	receipt.AmountMinor++
	if _, err := svc.RecordSubscriptionRenewal(context.Background(), receipt); !errors.Is(err, storage.ErrPaymentReceiptMismatch) {
		t.Fatalf("mismatch error = %v", err)
	}
	if orders.calls != 1 {
		t.Fatalf("invalid receipt delegated; calls=%d", orders.calls)
	}
}

func TestCryptoSettlementPreservesValidatedProviderCurrency(t *testing.T) {
	db, err := storage.New(filepath.Join(t.TempDir(), "crypto-provider-fact.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := storage.NewSQLOrderStore(db)
	orderID, err := store.CreateOrder(context.Background(), &storage.Order{
		UserID: 42, TotalUSD: 12.34, Status: storage.OrderStatusPending,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewOrderService(store, storage.NewCartStore(db.Conn()), storage.NewSQLProductStore(db), PaymentDeps{}, slog.Default())
	if _, err := svc.ConfirmPaymentReceipt(context.Background(), PaymentReceipt{
		OrderID: orderID, Provider: "crypto", ExternalID: "crypto-usdt",
		Currency: "USDT", AmountMinor: 1234, Scale: 2,
	}); err != nil {
		t.Fatal(err)
	}
	var currency string
	var amount int64
	if err := db.Conn().QueryRow(`SELECT currency, amount_minor FROM payment_attempts WHERE external_id='crypto-usdt'`).Scan(&currency, &amount); err != nil {
		t.Fatal(err)
	}
	if currency != "USDT" || amount != 1234 {
		t.Fatalf("currency=%s amount=%d", currency, amount)
	}
}

func TestInitialSubscriptionExactReplayKeepsPersistedExpiry(t *testing.T) {
	db, err := storage.New(filepath.Join(t.TempDir(), "initial-sub-replay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.Conn().Exec(`INSERT INTO categories (name) VALUES ('plans')`); err != nil {
		t.Fatal(err)
	}
	res, err := db.Conn().Exec(`INSERT INTO products
		(category_id,name,price_usd,price_stars,stock,is_active,sub_period_days)
		VALUES (1,'Plan',5,100,10,1,30)`)
	if err != nil {
		t.Fatal(err)
	}
	productID, _ := res.LastInsertId()
	store := storage.NewSQLOrderStore(db)
	orderID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, TotalStars: 100, Status: storage.OrderStatusPending,
		SubscriptionProductID: productID, SubscriptionPeriodDays: 30,
	}, []storage.OrderItem{{ProductID: productID, Quantity: 1}})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewOrderService(store, storage.NewCartStore(db.Conn()), storage.NewSQLProductStore(db), PaymentDeps{}, slog.Default())
	receipt := PaymentReceipt{OrderID: orderID, Provider: "stars", ExternalID: "initial-replay", PayerID: 42, Currency: "XTR", AmountMinor: 100, OccurredAt: time.Now().UTC()}
	if _, err := svc.ConfirmPaymentReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	var first time.Time
	_ = db.Conn().QueryRow(`SELECT expires_at FROM subscriptions WHERE order_id=?`, orderID).Scan(&first)
	time.Sleep(5 * time.Millisecond)
	if _, err := svc.ConfirmPaymentReceipt(ctx, receipt); !errors.Is(err, storage.ErrOrderStatusConflict) {
		t.Fatalf("replay error=%v", err)
	}
	var second time.Time
	var attempts, anomalies int
	var paymentState string
	_ = db.Conn().QueryRow(`SELECT expires_at FROM subscriptions WHERE order_id=?`, orderID).Scan(&second)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, orderID).Scan(&attempts)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies`).Scan(&anomalies)
	_ = db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&paymentState)
	if !first.Equal(second) || attempts != 1 || anomalies != 0 || paymentState != storage.PaymentStateSettled {
		t.Fatalf("first=%v second=%v attempts=%d anomalies=%d payment_state=%s", first, second, attempts, anomalies, paymentState)
	}
}

func TestInitialSubscriptionFallbackUsesProviderOccurrence(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	order := &storage.Order{ID: 7, UserID: 42, SubscriptionProductID: 3, SubscriptionPeriodDays: 30}
	receipt := PaymentReceipt{Provider: storage.PaymentMethodStars, ExternalID: "delayed-initial",
		OccurredAt: now.Add(-10 * 24 * time.Hour)}
	sub, err := subscriptionFromReceipt(order, receipt, true)
	if err != nil {
		t.Fatal(err)
	}
	want := receipt.OccurredAt.Add(30 * 24 * time.Hour)
	if !sub.ExpiresAt.Equal(want) {
		t.Fatalf("expiry=%v want provider-anchored %v", sub.ExpiresAt, want)
	}
	receipt.OccurredAt = time.Time{}
	if _, err := subscriptionFromReceipt(order, receipt, true); !errors.Is(err, storage.ErrPaymentReceiptMismatch) {
		t.Fatalf("missing provider occurrence error=%v", err)
	}
	receipt.OccurredAt = now.Add(-31 * 24 * time.Hour)
	if _, err := subscriptionFromReceipt(order, receipt, true); !errors.Is(err, storage.ErrPaymentReceiptMismatch) {
		t.Fatalf("expired provider period error=%v", err)
	}
}

func TestInitialSubscriptionReplayWithChangedProviderExpiryQuarantines(t *testing.T) {
	db, err := storage.New(filepath.Join(t.TempDir(), "initial-sub-expiry-conflict.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.Conn().Exec(`INSERT INTO categories (name) VALUES ('plans')`); err != nil {
		t.Fatal(err)
	}
	res, err := db.Conn().Exec(`INSERT INTO products
		(category_id,name,price_usd,price_stars,stock,is_active,sub_period_days)
		VALUES (1,'Plan',5,100,10,1,30)`)
	if err != nil {
		t.Fatal(err)
	}
	productID, _ := res.LastInsertId()
	store := storage.NewSQLOrderStore(db)
	orderID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, TotalStars: 100, Status: storage.OrderStatusPending,
		SubscriptionProductID: productID, SubscriptionPeriodDays: 30,
	}, []storage.OrderItem{{ProductID: productID, Quantity: 1}})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewOrderService(store, storage.NewCartStore(db.Conn()), storage.NewSQLProductStore(db), PaymentDeps{}, slog.Default())
	receipt := PaymentReceipt{
		OrderID: orderID, Provider: "stars", ExternalID: "initial-provider-expiry",
		PayerID: 42, Currency: "XTR", AmountMinor: 100,
		SubscriptionExpiresAt: time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Second),
	}
	if _, err := svc.ConfirmPaymentReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	originalExpiry := receipt.SubscriptionExpiresAt
	receipt.SubscriptionExpiresAt = receipt.SubscriptionExpiresAt.Add(24 * time.Hour)
	_, err = svc.ConfirmPaymentReceipt(ctx, receipt)
	if !errors.Is(err, storage.ErrPaymentNeedsReview) && !errors.Is(err, storage.ErrPaymentIdentityConflict) {
		t.Fatalf("error=%v", err)
	}
	var anomalies, attempts int
	var persistedExpiry time.Time
	var paymentState string
	rawConflict := "entitlement_expires_at:" + receipt.SubscriptionExpiresAt.UTC().Format(time.RFC3339Nano)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies
		WHERE external_id='initial-provider-expiry' AND raw_payload=?`, rawConflict).Scan(&anomalies)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts
		WHERE provider='stars' AND external_id='initial-provider-expiry'`).Scan(&attempts)
	_ = db.Conn().QueryRow(`SELECT entitlement_expires_at FROM payment_attempts
		WHERE provider='stars' AND external_id='initial-provider-expiry'`).Scan(&persistedExpiry)
	// The changed-expiry replay quarantines: the order must sit in
	// needs_review, not settled with the operator-unseen expiry.
	_ = db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&paymentState)
	if anomalies != 1 || attempts != 1 || !persistedExpiry.Equal(originalExpiry) ||
		paymentState != storage.PaymentStateNeedsReview {
		t.Fatalf("anomalies=%d attempts=%d persisted=%v original=%v payment_state=%s",
			anomalies, attempts, persistedExpiry, originalExpiry, paymentState)
	}
}

func TestMismatchedRenewalReceiptIsDurablyQuarantined(t *testing.T) {
	db, err := storage.New(filepath.Join(t.TempDir(), "renewal-mismatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	store := storage.NewSQLOrderStore(db)
	_, err = store.CreateOrder(ctx, &storage.Order{
		UserID: 42, TotalStars: 100, Status: storage.OrderStatusPaid,
		SubscriptionProductID: 3, SubscriptionPeriodDays: 30,
	}, nil)
	if err == nil {
		t.Fatal("invalid subscription fixture unexpectedly created")
	}
	// Use a direct legacy-shaped row: renewal validation must quarantine the
	// signed mismatch before delegating, independent of entitlement lookup.
	res, err := db.Conn().Exec(`INSERT INTO orders
		(user_id,total_stars,status,order_state,payment_state,fulfillment_state,subscription_period_days)
		VALUES (42,100,'paid','placed','settled','unfulfilled',30)`)
	if err != nil {
		t.Fatal(err)
	}
	orderID, _ := res.LastInsertId()
	svc := NewOrderService(store, storage.NewCartStore(db.Conn()), storage.NewSQLProductStore(db), PaymentDeps{}, slog.Default())
	_, err = svc.RecordSubscriptionRenewal(ctx, PaymentReceipt{
		OrderID: orderID, Provider: "stars", ExternalID: "bad-renewal", PayerID: 42,
		Currency: "XTR", AmountMinor: 99, SubscriptionExpiresAt: time.Now().Add(30 * 24 * time.Hour),
	})
	if !errors.Is(err, storage.ErrPaymentNeedsReview) {
		t.Fatalf("error=%v", err)
	}
	var anomalies int
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies WHERE external_id='bad-renewal'`).Scan(&anomalies)
	if anomalies != 1 {
		t.Fatalf("anomalies=%d", anomalies)
	}
}

// TestConfirmPaymentReceiptYooKassa pins the yookassa receipt contract:
// provider "yookassa", currency RUB, scale 2, kopecks equal to
// round(order.TotalRUB*100) and a non-empty external id (no payer check:
// yookassa receipts carry PayerID 0 like crypto). Mismatched receipts are
// rejected with the mismatch class without mutating the order (mock harness,
// the shape TestConfirmPaymentReceiptRejectsStarsMismatchWithoutMutation
// uses) and durably quarantined with the order left pending in needs review
// (SQL harness, the shape TestMismatchedProviderReceiptIsDurablyQuarantined-
// WithActualFacts uses). An exact replay of the settled receipt is an
// idempotent status conflict like the crypto replay.
func TestConfirmPaymentReceiptYooKassa(t *testing.T) {
	// Mismatch class, no order mutation (mock store has no anomaly recorder).
	orders := &mockOrderStore{orders: map[int64]*storage.Order{
		7: {ID: 7, UserID: 42, Status: storage.OrderStatusPending, TotalRUB: 1849.08},
		8: {ID: 8, UserID: 42, Status: storage.OrderStatusPending},
	}}
	svc := NewOrderService(orders, &mockCartStore{}, &mockProductStore{}, PaymentDeps{}, slog.Default())
	for _, receipt := range []PaymentReceipt{
		{OrderID: 7, Provider: "yookassa", ExternalID: "pay_1", Currency: "RUB", AmountMinor: 184907, Scale: 2},
		{OrderID: 7, Provider: "yookassa", ExternalID: "pay_1", Currency: "RUB", AmountMinor: 184909, Scale: 2},
		{OrderID: 7, Provider: "yookassa", ExternalID: "pay_1", Currency: "USD", AmountMinor: 184908, Scale: 2},
		{OrderID: 7, Provider: "yookassa", ExternalID: "pay_1", Currency: "RUB", AmountMinor: 184908, Scale: 0},
		{OrderID: 7, Provider: "yookassa", ExternalID: "", Currency: "RUB", AmountMinor: 184908, Scale: 2},
		{OrderID: 8, Provider: "yookassa", ExternalID: "pay_1", Currency: "RUB", AmountMinor: 184908, Scale: 2},
	} {
		if _, err := svc.ConfirmPaymentReceipt(context.Background(), receipt); !errors.Is(err, storage.ErrPaymentReceiptMismatch) {
			t.Fatalf("receipt=%+v err=%v", receipt, err)
		}
	}
	if orders.orders[7].Status != storage.OrderStatusPending || orders.orders[8].Status != storage.OrderStatusPending {
		t.Fatalf("orders mutated: 7=%+v 8=%+v", orders.orders[7], orders.orders[8])
	}

	// Durable quarantine with the receipt's actual facts (order 1849.08 RUB;
	// the last leg targets an order created while RUB was disabled, TotalRUB 0).
	db, err := storage.New(filepath.Join(t.TempDir(), "yookassa-receipt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	store := storage.NewSQLOrderStore(db)
	orderID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, TotalRUB: 1849.08, Status: storage.OrderStatusPending,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rubDisabledID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, Status: storage.OrderStatusPending,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc = NewOrderService(store, storage.NewCartStore(db.Conn()), storage.NewSQLProductStore(db), PaymentDeps{}, slog.Default())
	for _, receipt := range []PaymentReceipt{
		{OrderID: orderID, Provider: "yookassa", ExternalID: "yoo-low", Currency: "RUB", AmountMinor: 184907, Scale: 2},
		{OrderID: orderID, Provider: "yookassa", ExternalID: "yoo-high", Currency: "RUB", AmountMinor: 184909, Scale: 2},
		{OrderID: orderID, Provider: "yookassa", ExternalID: "yoo-usd", Currency: "USD", AmountMinor: 184908, Scale: 2},
		{OrderID: orderID, Provider: "yookassa", ExternalID: "yoo-scale", Currency: "RUB", AmountMinor: 184908, Scale: 0},
		{OrderID: orderID, Provider: "yookassa", ExternalID: "", Currency: "RUB", AmountMinor: 184908, Scale: 2},
		{OrderID: rubDisabledID, Provider: "yookassa", ExternalID: "yoo-rub-off", Currency: "RUB", AmountMinor: 184908, Scale: 2},
	} {
		want := storage.ErrPaymentNeedsReview
		if receipt.ExternalID == "" {
			// An identity-less fact cannot be quarantined durably; it is
			// rejected with the mismatch class instead.
			want = storage.ErrPaymentReceiptMismatch
		}
		if _, err := svc.ConfirmPaymentReceipt(ctx, receipt); !errors.Is(err, want) {
			t.Fatalf("receipt=%+v err=%v want=%v", receipt, err, want)
		}
	}
	for _, leg := range []struct {
		externalID string
		amount     int64
		currency   string
	}{
		{"yoo-low", 184907, "RUB"},
		{"yoo-high", 184909, "RUB"},
		{"yoo-usd", 184908, "USD"},
		{"yoo-scale", 184908, "RUB"},
		{"yoo-rub-off", 184908, "RUB"},
	} {
		var amount, payer int64
		var currency string
		if err := db.Conn().QueryRow(`
			SELECT amount_minor, payer_id, currency FROM payment_anomalies
			WHERE provider='yookassa' AND external_id=?`, leg.externalID).Scan(&amount, &payer, &currency); err != nil {
			t.Fatalf("anomaly %s: %v", leg.externalID, err)
		}
		if amount != leg.amount || payer != 0 || currency != leg.currency {
			t.Fatalf("anomaly %s: amount=%d payer=%d currency=%s", leg.externalID, amount, payer, currency)
		}
	}
	var anomalies int64
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies WHERE proposed_order_id=?`, orderID).Scan(&anomalies)
	if anomalies != 4 {
		t.Fatalf("order anomalies=%d, want 4 (the empty-external-id leg leaves no row)", anomalies)
	}
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies WHERE proposed_order_id=?`, rubDisabledID).Scan(&anomalies)
	if anomalies != 1 {
		t.Fatalf("rub-disabled order anomalies=%d, want 1", anomalies)
	}
	var orderAttempts, rubDisabledAttempts int64
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, orderID).Scan(&orderAttempts)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, rubDisabledID).Scan(&rubDisabledAttempts)
	if orderAttempts != 0 || rubDisabledAttempts != 0 {
		t.Fatalf("attempts: order=%d rub-disabled=%d, want 0/0", orderAttempts, rubDisabledAttempts)
	}
	mismatched, _ := store.GetOrder(ctx, orderID)
	disabled, _ := store.GetOrder(ctx, rubDisabledID)
	if mismatched.Status != storage.OrderStatusPending || mismatched.PaymentState != storage.PaymentStateNeedsReview ||
		disabled.Status != storage.OrderStatusPending || disabled.PaymentState != storage.PaymentStateNeedsReview {
		t.Fatalf("mismatched=%+v disabled=%+v", mismatched, disabled)
	}

	// Valid receipt settles the order; the attempt row carries the validated facts.
	settledID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, TotalRUB: 1849.08, Status: storage.OrderStatusPending,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt := PaymentReceipt{OrderID: settledID, Provider: "yookassa", ExternalID: "pay_1", Currency: "RUB", AmountMinor: 184908, Scale: 2}
	outcome, err := svc.ConfirmPaymentReceipt(ctx, receipt)
	if err != nil {
		t.Fatalf("valid receipt: %v", err)
	}
	if outcome == nil || outcome.Order == nil || outcome.Order.Status != storage.OrderStatusPaid {
		t.Fatalf("outcome=%+v", outcome)
	}
	var currency string
	var amount int64
	if err := db.Conn().QueryRow(`SELECT currency, amount_minor FROM payment_attempts WHERE external_id='pay_1'`).Scan(&currency, &amount); err != nil {
		t.Fatal(err)
	}
	if currency != "RUB" || amount != 184908 {
		t.Fatalf("attempt currency=%s amount=%d", currency, amount)
	}

	// Exact replay: idempotent status conflict; the order settles exactly once.
	if _, err := svc.ConfirmPaymentReceipt(ctx, receipt); !errors.Is(err, storage.ErrOrderStatusConflict) {
		t.Fatalf("replay error=%v", err)
	}
	var replayAttempts, replayAnomalies int64
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, settledID).Scan(&replayAttempts)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies WHERE proposed_order_id=?`, settledID).Scan(&replayAnomalies)
	settled, _ := store.GetOrder(ctx, settledID)
	if replayAttempts != 1 || replayAnomalies != 0 || settled.Status != storage.OrderStatusPaid ||
		settled.PaymentState != storage.PaymentStateSettled ||
		settled.PaymentMethod != storage.PaymentMethodYooKassa || settled.PaymentID != "pay_1" {
		t.Fatalf("attempts=%d anomalies=%d settled=%+v", replayAttempts, replayAnomalies, settled)
	}
}

// TestConfirmPaymentReceiptStripe pins the stripe receipt contract:
// provider "stripe", currency USD, scale 2, cents equal to
// round(order.TotalUSD*100) and a non-empty external id (no payer check:
// stripe receipts carry PayerID 0 like crypto). Mismatched receipts are
// rejected with the mismatch class without mutating the order (mock harness,
// the shape TestConfirmPaymentReceiptRejectsStarsMismatchWithoutMutation
// uses) and durably quarantined with the order left pending in needs review
// (SQL harness, the shape TestMismatchedProviderReceiptIsDurablyQuarantined-
// WithActualFacts uses). An exact replay of the settled receipt is an
// idempotent status conflict like the yookassa replay.
func TestConfirmPaymentReceiptStripe(t *testing.T) {
	// Mismatch class, no order mutation (mock store has no anomaly recorder).
	orders := &mockOrderStore{orders: map[int64]*storage.Order{
		7: {ID: 7, UserID: 42, Status: storage.OrderStatusPending, TotalUSD: 12.34},
		8: {ID: 8, UserID: 42, Status: storage.OrderStatusPending},
	}}
	svc := NewOrderService(orders, &mockCartStore{}, &mockProductStore{}, PaymentDeps{}, slog.Default())
	for _, receipt := range []PaymentReceipt{
		{OrderID: 7, Provider: "stripe", ExternalID: "cs_1", Currency: "USD", AmountMinor: 1233, Scale: 2},
		{OrderID: 7, Provider: "stripe", ExternalID: "cs_1", Currency: "USD", AmountMinor: 1235, Scale: 2},
		{OrderID: 7, Provider: "stripe", ExternalID: "cs_1", Currency: "RUB", AmountMinor: 1234, Scale: 2},
		{OrderID: 7, Provider: "stripe", ExternalID: "cs_1", Currency: "USD", AmountMinor: 1234, Scale: 0},
		{OrderID: 7, Provider: "stripe", ExternalID: "", Currency: "USD", AmountMinor: 1234, Scale: 2},
		{OrderID: 8, Provider: "stripe", ExternalID: "cs_1", Currency: "USD", AmountMinor: 1234, Scale: 2},
	} {
		if _, err := svc.ConfirmPaymentReceipt(context.Background(), receipt); !errors.Is(err, storage.ErrPaymentReceiptMismatch) {
			t.Fatalf("receipt=%+v err=%v", receipt, err)
		}
	}
	if orders.orders[7].Status != storage.OrderStatusPending || orders.orders[8].Status != storage.OrderStatusPending {
		t.Fatalf("orders mutated: 7=%+v 8=%+v", orders.orders[7], orders.orders[8])
	}

	// Durable quarantine with the receipt's actual facts (order 12.34 USD;
	// the last leg targets an order whose USD snapshot is zero).
	db, err := storage.New(filepath.Join(t.TempDir(), "stripe-receipt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	store := storage.NewSQLOrderStore(db)
	orderID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, TotalUSD: 12.34, Status: storage.OrderStatusPending,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	zeroUSDID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, Status: storage.OrderStatusPending,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc = NewOrderService(store, storage.NewCartStore(db.Conn()), storage.NewSQLProductStore(db), PaymentDeps{}, slog.Default())
	for _, receipt := range []PaymentReceipt{
		{OrderID: orderID, Provider: "stripe", ExternalID: "stripe-low", Currency: "USD", AmountMinor: 1233, Scale: 2},
		{OrderID: orderID, Provider: "stripe", ExternalID: "stripe-high", Currency: "USD", AmountMinor: 1235, Scale: 2},
		{OrderID: orderID, Provider: "stripe", ExternalID: "stripe-rub", Currency: "RUB", AmountMinor: 1234, Scale: 2},
		{OrderID: orderID, Provider: "stripe", ExternalID: "stripe-scale", Currency: "USD", AmountMinor: 1234, Scale: 0},
		{OrderID: orderID, Provider: "stripe", ExternalID: "", Currency: "USD", AmountMinor: 1234, Scale: 2},
		{OrderID: zeroUSDID, Provider: "stripe", ExternalID: "stripe-zero-usd", Currency: "USD", AmountMinor: 1234, Scale: 2},
	} {
		want := storage.ErrPaymentNeedsReview
		if receipt.ExternalID == "" {
			// An identity-less fact cannot be quarantined durably; it is
			// rejected with the mismatch class instead.
			want = storage.ErrPaymentReceiptMismatch
		}
		if _, err := svc.ConfirmPaymentReceipt(ctx, receipt); !errors.Is(err, want) {
			t.Fatalf("receipt=%+v err=%v want=%v", receipt, err, want)
		}
	}
	for _, leg := range []struct {
		externalID string
		amount     int64
		currency   string
	}{
		{"stripe-low", 1233, "USD"},
		{"stripe-high", 1235, "USD"},
		{"stripe-rub", 1234, "RUB"},
		{"stripe-scale", 1234, "USD"},
		{"stripe-zero-usd", 1234, "USD"},
	} {
		var amount, payer int64
		var currency string
		if err := db.Conn().QueryRow(`
			SELECT amount_minor, payer_id, currency FROM payment_anomalies
			WHERE provider='stripe' AND external_id=?`, leg.externalID).Scan(&amount, &payer, &currency); err != nil {
			t.Fatalf("anomaly %s: %v", leg.externalID, err)
		}
		if amount != leg.amount || payer != 0 || currency != leg.currency {
			t.Fatalf("anomaly %s: amount=%d payer=%d currency=%s", leg.externalID, amount, payer, currency)
		}
	}
	var anomalies int64
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies WHERE proposed_order_id=?`, orderID).Scan(&anomalies)
	if anomalies != 4 {
		t.Fatalf("order anomalies=%d, want 4 (the empty-external-id leg leaves no row)", anomalies)
	}
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies WHERE proposed_order_id=?`, zeroUSDID).Scan(&anomalies)
	if anomalies != 1 {
		t.Fatalf("zero-usd order anomalies=%d, want 1", anomalies)
	}
	var orderAttempts, zeroUSDAttempts int64
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, orderID).Scan(&orderAttempts)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, zeroUSDID).Scan(&zeroUSDAttempts)
	if orderAttempts != 0 || zeroUSDAttempts != 0 {
		t.Fatalf("attempts: order=%d zero-usd=%d, want 0/0", orderAttempts, zeroUSDAttempts)
	}
	mismatched, _ := store.GetOrder(ctx, orderID)
	zeroUSD, _ := store.GetOrder(ctx, zeroUSDID)
	if mismatched.Status != storage.OrderStatusPending || mismatched.PaymentState != storage.PaymentStateNeedsReview ||
		zeroUSD.Status != storage.OrderStatusPending || zeroUSD.PaymentState != storage.PaymentStateNeedsReview {
		t.Fatalf("mismatched=%+v zeroUSD=%+v", mismatched, zeroUSD)
	}

	// Valid receipt settles the order; the attempt row carries the validated facts.
	settledID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, TotalUSD: 12.34, Status: storage.OrderStatusPending,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt := PaymentReceipt{OrderID: settledID, Provider: "stripe", ExternalID: "cs_test_1", Currency: "USD", AmountMinor: 1234, Scale: 2}
	outcome, err := svc.ConfirmPaymentReceipt(ctx, receipt)
	if err != nil {
		t.Fatalf("valid receipt: %v", err)
	}
	if outcome == nil || outcome.Order == nil || outcome.Order.Status != storage.OrderStatusPaid {
		t.Fatalf("outcome=%+v", outcome)
	}
	var currency string
	var amount int64
	if err := db.Conn().QueryRow(`SELECT currency, amount_minor FROM payment_attempts WHERE external_id='cs_test_1'`).Scan(&currency, &amount); err != nil {
		t.Fatal(err)
	}
	if currency != "USD" || amount != 1234 {
		t.Fatalf("attempt currency=%s amount=%d", currency, amount)
	}
	var events int64
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events WHERE provider='stripe' AND external_id='cs_test_1'`).Scan(&events)
	if events != 1 {
		t.Fatalf("events=%d, want 1", events)
	}

	// Exact replay: idempotent status conflict; the order settles exactly once.
	if _, err := svc.ConfirmPaymentReceipt(ctx, receipt); !errors.Is(err, storage.ErrOrderStatusConflict) {
		t.Fatalf("replay error=%v", err)
	}
	var replayAttempts, replayAnomalies int64
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, settledID).Scan(&replayAttempts)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies WHERE proposed_order_id=?`, settledID).Scan(&replayAnomalies)
	settled, _ := store.GetOrder(ctx, settledID)
	if replayAttempts != 1 || replayAnomalies != 0 || settled.Status != storage.OrderStatusPaid ||
		settled.PaymentState != storage.PaymentStateSettled ||
		settled.PaymentMethod != storage.PaymentMethodStripe || settled.PaymentID != "cs_test_1" {
		t.Fatalf("attempts=%d anomalies=%d settled=%+v", replayAttempts, replayAnomalies, settled)
	}
}

// TestConfirmPaymentReceiptTON pins the ton receipt contract: provider
// "ton", currency TON, scale 9, nanotons >= order.TotalTonNano and a
// non-empty external id (no payer check: on-chain receipts carry PayerID 0
// like crypto). Settlement is overpay-tolerant — an on-chain transfer
// larger than the snapshot is real money received, so it settles and the
// ledger records the ACTUAL received amount; underpay quarantines. An
// order created while TON was disabled (TotalTonNano 0) never settles.
// Mismatched receipts are rejected with the mismatch class without mutating
// the order (mock harness) and durably quarantined with the order left
// pending in needs review (SQL harness). An exact replay of the settled
// receipt is an idempotent status conflict like the stripe replay.
func TestConfirmPaymentReceiptTON(t *testing.T) {
	// Mismatch class, no order mutation (mock store has no anomaly recorder).
	orders := &mockOrderStore{orders: map[int64]*storage.Order{
		7: {ID: 7, UserID: 42, Status: storage.OrderStatusPending, TotalTonNano: 1500000000},
		8: {ID: 8, UserID: 42, Status: storage.OrderStatusPending},
	}}
	svc := NewOrderService(orders, &mockCartStore{}, &mockProductStore{}, PaymentDeps{}, slog.Default())
	for _, receipt := range []PaymentReceipt{
		{OrderID: 7, Provider: "ton", ExternalID: "1:abc", Currency: "TON", AmountMinor: 1499999999, Scale: 9},
		{OrderID: 7, Provider: "ton", ExternalID: "1:abc", Currency: "USDT", AmountMinor: 1500000000, Scale: 9},
		{OrderID: 7, Provider: "ton", ExternalID: "1:abc", Currency: "TON", AmountMinor: 1500000000, Scale: 2},
		{OrderID: 7, Provider: "ton", ExternalID: "", Currency: "TON", AmountMinor: 1500000000, Scale: 9},
		{OrderID: 8, Provider: "ton", ExternalID: "1:abc", Currency: "TON", AmountMinor: 1500000000, Scale: 9},
	} {
		if _, err := svc.ConfirmPaymentReceipt(context.Background(), receipt); !errors.Is(err, storage.ErrPaymentReceiptMismatch) {
			t.Fatalf("receipt=%+v err=%v", receipt, err)
		}
	}
	if orders.orders[7].Status != storage.OrderStatusPending || orders.orders[8].Status != storage.OrderStatusPending {
		t.Fatalf("orders mutated: 7=%+v 8=%+v", orders.orders[7], orders.orders[8])
	}

	// Durable quarantine with the receipt's actual facts (order 1.5 TON;
	// the last leg targets an order created while TON was disabled,
	// TotalTonNano 0: a positive receipt against it must not settle).
	db, err := storage.New(filepath.Join(t.TempDir(), "ton-receipt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	store := storage.NewSQLOrderStore(db)
	orderID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, TotalTonNano: 1500000000, Status: storage.OrderStatusPending,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tonDisabledID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, Status: storage.OrderStatusPending,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc = NewOrderService(store, storage.NewCartStore(db.Conn()), storage.NewSQLProductStore(db), PaymentDeps{}, slog.Default())
	for _, receipt := range []PaymentReceipt{
		{OrderID: orderID, Provider: "ton", ExternalID: "ton-low", Currency: "TON", AmountMinor: 1499999999, Scale: 9},
		{OrderID: orderID, Provider: "ton", ExternalID: "ton-usdt", Currency: "USDT", AmountMinor: 1500000000, Scale: 9},
		{OrderID: orderID, Provider: "ton", ExternalID: "ton-scale", Currency: "TON", AmountMinor: 1500000000, Scale: 2},
		{OrderID: orderID, Provider: "ton", ExternalID: "", Currency: "TON", AmountMinor: 1500000000, Scale: 9},
		{OrderID: tonDisabledID, Provider: "ton", ExternalID: "ton-disabled", Currency: "TON", AmountMinor: 1500000000, Scale: 9},
	} {
		want := storage.ErrPaymentNeedsReview
		if receipt.ExternalID == "" {
			// An identity-less fact cannot be quarantined durably; it is
			// rejected with the mismatch class instead.
			want = storage.ErrPaymentReceiptMismatch
		}
		if _, err := svc.ConfirmPaymentReceipt(ctx, receipt); !errors.Is(err, want) {
			t.Fatalf("receipt=%+v err=%v want=%v", receipt, err, want)
		}
	}
	for _, leg := range []struct {
		externalID string
		amount     int64
		currency   string
	}{
		{"ton-low", 1499999999, "TON"},
		{"ton-usdt", 1500000000, "USDT"},
		{"ton-scale", 1500000000, "TON"},
		{"ton-disabled", 1500000000, "TON"},
	} {
		var amount, payer int64
		var currency string
		if err := db.Conn().QueryRow(`
			SELECT amount_minor, payer_id, currency FROM payment_anomalies
			WHERE provider='ton' AND external_id=?`, leg.externalID).Scan(&amount, &payer, &currency); err != nil {
			t.Fatalf("anomaly %s: %v", leg.externalID, err)
		}
		if amount != leg.amount || payer != 0 || currency != leg.currency {
			t.Fatalf("anomaly %s: amount=%d payer=%d currency=%s", leg.externalID, amount, payer, currency)
		}
	}
	var anomalies int64
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies WHERE proposed_order_id=?`, orderID).Scan(&anomalies)
	if anomalies != 3 {
		t.Fatalf("order anomalies=%d, want 3 (the empty-external-id leg leaves no row)", anomalies)
	}
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies WHERE proposed_order_id=?`, tonDisabledID).Scan(&anomalies)
	if anomalies != 1 {
		t.Fatalf("ton-disabled order anomalies=%d, want 1", anomalies)
	}
	var orderAttempts, tonDisabledAttempts int64
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, orderID).Scan(&orderAttempts)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, tonDisabledID).Scan(&tonDisabledAttempts)
	if orderAttempts != 0 || tonDisabledAttempts != 0 {
		t.Fatalf("attempts: order=%d ton-disabled=%d, want 0/0", orderAttempts, tonDisabledAttempts)
	}
	mismatched, _ := store.GetOrder(ctx, orderID)
	disabled, _ := store.GetOrder(ctx, tonDisabledID)
	if mismatched.Status != storage.OrderStatusPending || mismatched.PaymentState != storage.PaymentStateNeedsReview ||
		disabled.Status != storage.OrderStatusPending || disabled.PaymentState != storage.PaymentStateNeedsReview {
		t.Fatalf("mismatched=%+v disabled=%+v", mismatched, disabled)
	}

	// Exact-boundary receipt (== the snapshot) settles the order; the
	// attempt row carries the validated facts.
	settledID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, TotalTonNano: 1500000000, Status: storage.OrderStatusPending,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt := PaymentReceipt{OrderID: settledID, Provider: "ton", ExternalID: "1720000000001:abc", Currency: "TON", AmountMinor: 1500000000, Scale: 9}
	outcome, err := svc.ConfirmPaymentReceipt(ctx, receipt)
	if err != nil {
		t.Fatalf("valid receipt: %v", err)
	}
	if outcome == nil || outcome.Order == nil || outcome.Order.Status != storage.OrderStatusPaid {
		t.Fatalf("outcome=%+v", outcome)
	}
	var currency string
	var amount int64
	if err := db.Conn().QueryRow(`SELECT currency, amount_minor FROM payment_attempts WHERE external_id='1720000000001:abc'`).Scan(&currency, &amount); err != nil {
		t.Fatal(err)
	}
	if currency != "TON" || amount != 1500000000 {
		t.Fatalf("attempt currency=%s amount=%d", currency, amount)
	}
	var events int64
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events WHERE provider='ton' AND external_id='1720000000001:abc'`).Scan(&events)
	if events != 1 {
		t.Fatalf("events=%d, want 1", events)
	}

	// Overpay settles too, and the ledger records the ACTUAL received
	// amount (1600000001 nanotons), not the order snapshot: on-chain
	// overpayment is real money received.
	overpayID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, TotalTonNano: 1500000000, Status: storage.OrderStatusPending,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	overpayReceipt := PaymentReceipt{OrderID: overpayID, Provider: "ton", ExternalID: "1720000000002:def", Currency: "TON", AmountMinor: 1600000001, Scale: 9}
	overpayOutcome, err := svc.ConfirmPaymentReceipt(ctx, overpayReceipt)
	if err != nil {
		t.Fatalf("overpay receipt: %v", err)
	}
	if overpayOutcome == nil || overpayOutcome.Order == nil || overpayOutcome.Order.Status != storage.OrderStatusPaid {
		t.Fatalf("overpay outcome=%+v", overpayOutcome)
	}
	if err := db.Conn().QueryRow(`SELECT amount_minor FROM payment_attempts WHERE external_id='1720000000002:def'`).Scan(&amount); err != nil {
		t.Fatal(err)
	}
	if amount != 1600000001 {
		t.Fatalf("overpay attempt amount=%d, want the actual received 1600000001, not the 1500000000 snapshot", amount)
	}

	// Exact replay: idempotent status conflict; the order settles exactly once.
	if _, err := svc.ConfirmPaymentReceipt(ctx, receipt); !errors.Is(err, storage.ErrOrderStatusConflict) {
		t.Fatalf("replay error=%v", err)
	}
	var replayAttempts, replayAnomalies int64
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, settledID).Scan(&replayAttempts)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies WHERE proposed_order_id=?`, settledID).Scan(&replayAnomalies)
	settled, _ := store.GetOrder(ctx, settledID)
	if replayAttempts != 1 || replayAnomalies != 0 || settled.Status != storage.OrderStatusPaid ||
		settled.PaymentState != storage.PaymentStateSettled ||
		settled.PaymentMethod != storage.PaymentMethodTON || settled.PaymentID != "1720000000001:abc" {
		t.Fatalf("attempts=%d anomalies=%d settled=%+v", replayAttempts, replayAnomalies, settled)
	}
}

// TestConfirmPaymentReceiptNowpayments pins the nowpayments receipt
// contract, an exact mirror of the stripe contract: provider "nowpayments",
// currency USD, scale 2, cents equal to round(order.TotalUSD*100) and a
// non-empty external id (no payer check: nowpayments receipts carry
// PayerID 0 like stripe — the signed IPN echoes our own invoice, so the
// match is exact, no overpay tolerance). Mismatched receipts are rejected
// with the mismatch class without mutating the order (mock harness) and
// durably quarantined with the order left pending in needs review (SQL
// harness). An exact replay of the settled receipt is an idempotent status
// conflict like the stripe replay.
func TestConfirmPaymentReceiptNowpayments(t *testing.T) {
	// Mismatch class, no order mutation (mock store has no anomaly recorder).
	orders := &mockOrderStore{orders: map[int64]*storage.Order{
		7: {ID: 7, UserID: 42, Status: storage.OrderStatusPending, TotalUSD: 12.34},
		8: {ID: 8, UserID: 42, Status: storage.OrderStatusPending},
	}}
	svc := NewOrderService(orders, &mockCartStore{}, &mockProductStore{}, PaymentDeps{}, slog.Default())
	for _, receipt := range []PaymentReceipt{
		{OrderID: 7, Provider: "nowpayments", ExternalID: "5077125051", Currency: "USD", AmountMinor: 1233, Scale: 2},
		{OrderID: 7, Provider: "nowpayments", ExternalID: "5077125051", Currency: "USD", AmountMinor: 1235, Scale: 2},
		{OrderID: 7, Provider: "nowpayments", ExternalID: "5077125051", Currency: "RUB", AmountMinor: 1234, Scale: 2},
		{OrderID: 7, Provider: "nowpayments", ExternalID: "5077125051", Currency: "USD", AmountMinor: 1234, Scale: 0},
		{OrderID: 7, Provider: "nowpayments", ExternalID: "", Currency: "USD", AmountMinor: 1234, Scale: 2},
		{OrderID: 8, Provider: "nowpayments", ExternalID: "5077125051", Currency: "USD", AmountMinor: 1234, Scale: 2},
	} {
		if _, err := svc.ConfirmPaymentReceipt(context.Background(), receipt); !errors.Is(err, storage.ErrPaymentReceiptMismatch) {
			t.Fatalf("receipt=%+v err=%v", receipt, err)
		}
	}
	if orders.orders[7].Status != storage.OrderStatusPending || orders.orders[8].Status != storage.OrderStatusPending {
		t.Fatalf("orders mutated: 7=%+v 8=%+v", orders.orders[7], orders.orders[8])
	}

	// Durable quarantine with the receipt's actual facts (order 12.34 USD;
	// the last leg targets an order whose USD snapshot is zero).
	db, err := storage.New(filepath.Join(t.TempDir(), "nowpayments-receipt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	store := storage.NewSQLOrderStore(db)
	orderID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, TotalUSD: 12.34, Status: storage.OrderStatusPending,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	zeroUSDID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, Status: storage.OrderStatusPending,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc = NewOrderService(store, storage.NewCartStore(db.Conn()), storage.NewSQLProductStore(db), PaymentDeps{}, slog.Default())
	for _, receipt := range []PaymentReceipt{
		{OrderID: orderID, Provider: "nowpayments", ExternalID: "np-low", Currency: "USD", AmountMinor: 1233, Scale: 2},
		{OrderID: orderID, Provider: "nowpayments", ExternalID: "np-high", Currency: "USD", AmountMinor: 1235, Scale: 2},
		{OrderID: orderID, Provider: "nowpayments", ExternalID: "np-rub", Currency: "RUB", AmountMinor: 1234, Scale: 2},
		{OrderID: orderID, Provider: "nowpayments", ExternalID: "np-scale", Currency: "USD", AmountMinor: 1234, Scale: 0},
		{OrderID: orderID, Provider: "nowpayments", ExternalID: "", Currency: "USD", AmountMinor: 1234, Scale: 2},
		{OrderID: zeroUSDID, Provider: "nowpayments", ExternalID: "np-zero-usd", Currency: "USD", AmountMinor: 1234, Scale: 2},
	} {
		want := storage.ErrPaymentNeedsReview
		if receipt.ExternalID == "" {
			// An identity-less fact cannot be quarantined durably; it is
			// rejected with the mismatch class instead.
			want = storage.ErrPaymentReceiptMismatch
		}
		if _, err := svc.ConfirmPaymentReceipt(ctx, receipt); !errors.Is(err, want) {
			t.Fatalf("receipt=%+v err=%v want=%v", receipt, err, want)
		}
	}
	for _, leg := range []struct {
		externalID string
		amount     int64
		currency   string
	}{
		{"np-low", 1233, "USD"},
		{"np-high", 1235, "USD"},
		{"np-rub", 1234, "RUB"},
		{"np-scale", 1234, "USD"},
		{"np-zero-usd", 1234, "USD"},
	} {
		var amount, payer int64
		var currency string
		if err := db.Conn().QueryRow(`
			SELECT amount_minor, payer_id, currency FROM payment_anomalies
			WHERE provider='nowpayments' AND external_id=?`, leg.externalID).Scan(&amount, &payer, &currency); err != nil {
			t.Fatalf("anomaly %s: %v", leg.externalID, err)
		}
		if amount != leg.amount || payer != 0 || currency != leg.currency {
			t.Fatalf("anomaly %s: amount=%d payer=%d currency=%s", leg.externalID, amount, payer, currency)
		}
	}
	var anomalies int64
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies WHERE proposed_order_id=?`, orderID).Scan(&anomalies)
	if anomalies != 4 {
		t.Fatalf("order anomalies=%d, want 4 (the empty-external-id leg leaves no row)", anomalies)
	}
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies WHERE proposed_order_id=?`, zeroUSDID).Scan(&anomalies)
	if anomalies != 1 {
		t.Fatalf("zero-usd order anomalies=%d, want 1", anomalies)
	}
	var orderAttempts, zeroUSDAttempts int64
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, orderID).Scan(&orderAttempts)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, zeroUSDID).Scan(&zeroUSDAttempts)
	if orderAttempts != 0 || zeroUSDAttempts != 0 {
		t.Fatalf("attempts: order=%d zero-usd=%d, want 0/0", orderAttempts, zeroUSDAttempts)
	}
	mismatched, _ := store.GetOrder(ctx, orderID)
	zeroUSD, _ := store.GetOrder(ctx, zeroUSDID)
	if mismatched.Status != storage.OrderStatusPending || mismatched.PaymentState != storage.PaymentStateNeedsReview ||
		zeroUSD.Status != storage.OrderStatusPending || zeroUSD.PaymentState != storage.PaymentStateNeedsReview {
		t.Fatalf("mismatched=%+v zeroUSD=%+v", mismatched, zeroUSD)
	}

	// Valid receipt settles the order; the attempt row carries the validated
	// facts. The external id is the decimal string of NOWPayments'
	// payment_id.
	settledID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, TotalUSD: 12.34, Status: storage.OrderStatusPending,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt := PaymentReceipt{OrderID: settledID, Provider: "nowpayments", ExternalID: "5077125051", Currency: "USD", AmountMinor: 1234, Scale: 2}
	outcome, err := svc.ConfirmPaymentReceipt(ctx, receipt)
	if err != nil {
		t.Fatalf("valid receipt: %v", err)
	}
	if outcome == nil || outcome.Order == nil || outcome.Order.Status != storage.OrderStatusPaid {
		t.Fatalf("outcome=%+v", outcome)
	}
	var currency string
	var amount int64
	if err := db.Conn().QueryRow(`SELECT currency, amount_minor FROM payment_attempts WHERE external_id='5077125051'`).Scan(&currency, &amount); err != nil {
		t.Fatal(err)
	}
	if currency != "USD" || amount != 1234 {
		t.Fatalf("attempt currency=%s amount=%d", currency, amount)
	}
	var events int64
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events WHERE provider='nowpayments' AND external_id='5077125051'`).Scan(&events)
	if events != 1 {
		t.Fatalf("events=%d, want 1", events)
	}

	// Exact replay: idempotent status conflict; the order settles exactly once.
	if _, err := svc.ConfirmPaymentReceipt(ctx, receipt); !errors.Is(err, storage.ErrOrderStatusConflict) {
		t.Fatalf("replay error=%v", err)
	}
	var replayAttempts, replayAnomalies int64
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, settledID).Scan(&replayAttempts)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies WHERE proposed_order_id=?`, settledID).Scan(&replayAnomalies)
	settled, _ := store.GetOrder(ctx, settledID)
	if replayAttempts != 1 || replayAnomalies != 0 || settled.Status != storage.OrderStatusPaid ||
		settled.PaymentState != storage.PaymentStateSettled ||
		settled.PaymentMethod != storage.PaymentMethodNowpayments || settled.PaymentID != "5077125051" {
		t.Fatalf("attempts=%d anomalies=%d settled=%+v", replayAttempts, replayAnomalies, settled)
	}
}
