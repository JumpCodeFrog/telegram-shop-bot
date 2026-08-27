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
	_ = db.Conn().QueryRow(`SELECT expires_at FROM subscriptions WHERE order_id=?`, orderID).Scan(&second)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, orderID).Scan(&attempts)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies`).Scan(&anomalies)
	if !first.Equal(second) || attempts != 1 || anomalies != 0 {
		t.Fatalf("first=%v second=%v attempts=%d anomalies=%d", first, second, attempts, anomalies)
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
	rawConflict := "entitlement_expires_at:" + receipt.SubscriptionExpiresAt.UTC().Format(time.RFC3339Nano)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies
		WHERE external_id='initial-provider-expiry' AND raw_payload=?`, rawConflict).Scan(&anomalies)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts
		WHERE provider='stars' AND external_id='initial-provider-expiry'`).Scan(&attempts)
	_ = db.Conn().QueryRow(`SELECT entitlement_expires_at FROM payment_attempts
		WHERE provider='stars' AND external_id='initial-provider-expiry'`).Scan(&persistedExpiry)
	if anomalies != 1 || attempts != 1 || !persistedExpiry.Equal(originalExpiry) {
		t.Fatalf("anomalies=%d attempts=%d persisted=%v original=%v",
			anomalies, attempts, persistedExpiry, originalExpiry)
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
	orderID, err := store.CreateOrder(ctx, &storage.Order{
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
	orderID, _ = res.LastInsertId()
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
