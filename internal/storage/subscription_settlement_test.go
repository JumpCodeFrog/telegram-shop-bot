package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func pendingSubscriptionOrder(t *testing.T, db *DB, userID int64) (*SQLOrderStore, int64, int64) {
	t.Helper()
	ctx := context.Background()
	res, err := db.Conn().ExecContext(ctx, `INSERT INTO categories (name) VALUES (?)`, fmt.Sprintf("sub-%d", userID))
	if err != nil {
		t.Fatal(err)
	}
	categoryID, _ := res.LastInsertId()
	res, err = db.Conn().ExecContext(ctx, `
		INSERT INTO products
			(category_id, name, price_usd, price_stars, stock, is_active, sub_period_days)
		VALUES (?, 'Plan', 5, 100, 50, 1, 30)`, categoryID)
	if err != nil {
		t.Fatal(err)
	}
	productID, _ := res.LastInsertId()
	store := NewSQLOrderStore(db)
	orderID, err := store.CreateOrder(ctx, &Order{
		UserID: userID, TotalUSD: 5, TotalStars: 100, Status: OrderStatusPending,
		SubscriptionProductID: productID, SubscriptionPeriodDays: 30,
	}, []OrderItem{{ProductID: productID, ProductName: "Plan", Quantity: 1, PriceUSD: 5}})
	if err != nil {
		t.Fatal(err)
	}
	return store, orderID, productID
}

func subscriptionFor(orderID, userID, productID int64, charge string) Subscription {
	return Subscription{
		UserID: userID, ProductID: productID, OrderID: orderID,
		ChargeID: charge, Status: SubStatusActive,
		ExpiresAt: time.Now().UTC().Add(30 * 24 * time.Hour),
	}
}

func TestAtomicSubscriptionSettlementCommitsEntitlementAndCapture(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "atomic-sub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, productID := pendingSubscriptionOrder(t, db, 42)
	sub := subscriptionFor(orderID, 42, productID, "initial")
	if err := store.UpdateOrderStatusWithSubscription(context.Background(), orderID,
		OrderStatusPending, OrderStatusPaid, PaymentMethodStars, sub.ChargeID, sub); err != nil {
		t.Fatal(err)
	}
	order, err := store.GetOrder(context.Background(), orderID)
	if err != nil {
		t.Fatal(err)
	}
	var attempts, subscriptions, stock int
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=? AND status='succeeded'`, orderID).Scan(&attempts)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM subscriptions WHERE order_id=? AND status='active'`, orderID).Scan(&subscriptions)
	_ = db.Conn().QueryRow(`SELECT stock FROM products WHERE id=?`, productID).Scan(&stock)
	if order.Status != OrderStatusPaid || order.PaymentState != PaymentStateSettled ||
		attempts != 1 || subscriptions != 1 || stock != 49 {
		t.Fatalf("order=%+v attempts=%d subscriptions=%d stock=%d", order, attempts, subscriptions, stock)
	}
}

func TestAtomicSubscriptionSettlementFailureQuarantinesCapture(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "atomic-sub-failure.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, productID := pendingSubscriptionOrder(t, db, 42)
	if _, err := db.Conn().Exec(`
		CREATE TRIGGER fail_subscription_insert BEFORE INSERT ON subscriptions
		BEGIN SELECT RAISE(ABORT, 'injected entitlement failure'); END`); err != nil {
		t.Fatal(err)
	}
	sub := subscriptionFor(orderID, 42, productID, "captured-but-unfulfilled")
	fact := PaymentFact{
		Provider: PaymentMethodStars, ExternalID: sub.ChargeID,
		AmountMinor: 100, Currency: "XTR", Scale: 0,
		EntitlementExpiresAt: sub.ExpiresAt,
	}
	err = store.UpdateOrderStatusWithSubscriptionFact(context.Background(), orderID,
		OrderStatusPending, OrderStatusPaid, fact, sub)
	if !errors.Is(err, ErrPaymentNeedsReview) || !errors.Is(err, ErrSubscriptionEntitlement) {
		t.Fatalf("error=%v", err)
	}
	order, _ := store.GetOrder(context.Background(), orderID)
	var reviewAttempts, subscriptions, stock int
	var persistedExpiry time.Time
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=? AND status='needs_review'`, orderID).Scan(&reviewAttempts)
	if err := db.Conn().QueryRow(`SELECT entitlement_expires_at FROM payment_attempts
		WHERE order_id=? AND status='needs_review'`, orderID).Scan(&persistedExpiry); err != nil {
		t.Fatal(err)
	}
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM subscriptions WHERE order_id=?`, orderID).Scan(&subscriptions)
	_ = db.Conn().QueryRow(`SELECT stock FROM products WHERE id=?`, productID).Scan(&stock)
	if order.Status != OrderStatusPending || order.PaymentState != PaymentStateNeedsReview ||
		reviewAttempts != 1 || subscriptions != 0 || stock != 50 || !persistedExpiry.Equal(sub.ExpiresAt) {
		t.Fatalf("order=%+v review_attempts=%d subscriptions=%d stock=%d expiry=%v want=%v",
			order, reviewAttempts, subscriptions, stock, persistedExpiry, sub.ExpiresAt)
	}
}

func TestExactSubscriptionReplayRepairsMissingEntitlementWithoutReplayingStock(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "sub-repair.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, productID := pendingSubscriptionOrder(t, db, 42)
	sub := subscriptionFor(orderID, 42, productID, "same-charge")
	ctx := context.Background()
	if err := store.UpdateOrderStatusWithSubscription(ctx, orderID, OrderStatusPending, OrderStatusPaid, "stars", sub.ChargeID, sub); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`DELETE FROM subscriptions WHERE order_id=?`, orderID); err != nil {
		t.Fatal(err)
	}
	err = store.UpdateOrderStatusWithSubscription(ctx, orderID, OrderStatusPending, OrderStatusPaid, "stars", sub.ChargeID, sub)
	if !errors.Is(err, ErrOrderStatusConflict) {
		t.Fatalf("exact replay error=%v", err)
	}
	var attempts, subscriptions, stock int
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, orderID).Scan(&attempts)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM subscriptions WHERE order_id=?`, orderID).Scan(&subscriptions)
	_ = db.Conn().QueryRow(`SELECT stock FROM products WHERE id=?`, productID).Scan(&stock)
	if attempts != 1 || subscriptions != 1 || stock != 49 {
		t.Fatalf("attempts=%d subscriptions=%d stock=%d", attempts, subscriptions, stock)
	}
}

func TestSubscriptionOrderReservationIsConcurrentAndReleasable(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "sub-reservation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, firstOrder, productID := pendingSubscriptionOrder(t, db, 42)
	ctx := context.Background()
	newOrder := func() error {
		_, err := store.CreateOrder(ctx, &Order{
			UserID: 42, TotalUSD: 5, TotalStars: 100, Status: OrderStatusPending,
			SubscriptionProductID: productID, SubscriptionPeriodDays: 30,
		}, []OrderItem{{ProductID: productID, Quantity: 1}})
		return err
	}
	if err := newOrder(); !errors.Is(err, ErrSubscriptionOrderConflict) {
		t.Fatalf("second pending error=%v", err)
	}
	if err := store.CancelOrder(ctx, firstOrder, 42); err != nil {
		t.Fatal(err)
	}
	if err := newOrder(); err != nil {
		t.Fatalf("reservation not released: %v", err)
	}

	// A fresh database exercises the unique index under concurrent insert races.
	db2, err := New(filepath.Join(t.TempDir(), "sub-reservation-race.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	_, seedOrder, raceProduct := pendingSubscriptionOrder(t, db2, 77)
	if err := NewSQLOrderStore(db2).CancelOrder(ctx, seedOrder, 77); err != nil {
		t.Fatal(err)
	}
	raceStore := NewSQLOrderStore(db2)
	const contenders = 20
	var wg sync.WaitGroup
	errs := make([]error, contenders)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = raceStore.CreateOrder(ctx, &Order{
				UserID: 77, TotalUSD: 5, TotalStars: 100, Status: OrderStatusPending,
				SubscriptionProductID: raceProduct, SubscriptionPeriodDays: 30,
			}, []OrderItem{{ProductID: raceProduct, Quantity: 1}})
		}(i)
	}
	wg.Wait()
	succeeded, conflicts := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrSubscriptionOrderConflict):
			conflicts++
		default:
			t.Fatalf("unexpected race errors=%v", errs)
		}
	}
	if succeeded != 1 || conflicts != contenders-1 {
		t.Fatalf("success=%d conflicts=%d errors=%v", succeeded, conflicts, errs)
	}
}

func TestNeedsReviewSubscriptionOrderKeepsReservation(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "sub-review-reservation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, productID := pendingSubscriptionOrder(t, db, 42)
	if err := store.RecordUnexpectedPayment(context.Background(), orderID, "stars", "unresolved", "test"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatal(err)
	}
	_, err = store.CreateOrder(context.Background(), &Order{
		UserID: 42, TotalStars: 100, Status: OrderStatusPending,
		SubscriptionProductID: productID, SubscriptionPeriodDays: 30,
	}, []OrderItem{{ProductID: productID, Quantity: 1}})
	if !errors.Is(err, ErrSubscriptionOrderConflict) {
		t.Fatalf("needs_review reservation error=%v", err)
	}
}

func TestExpiredCanceledSubscriptionCanStartNewContract(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "sub-new-contract.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, oldOrder, productID := pendingSubscriptionOrder(t, db, 42)
	oldSub := subscriptionFor(oldOrder, 42, productID, "old-charge")
	oldSub.ExpiresAt = time.Now().UTC().Add(-time.Hour)
	oldSub.Status = SubStatusCanceled
	if err := NewSQLSubscriptionStore(db).Upsert(context.Background(), oldSub); err != nil {
		t.Fatal(err)
	}
	if err := store.CancelOrder(context.Background(), oldOrder, 42); err != nil {
		t.Fatal(err)
	}
	newOrder, err := store.CreateOrder(context.Background(), &Order{
		UserID: 42, TotalStars: 100, Status: OrderStatusPending,
		SubscriptionProductID: productID, SubscriptionPeriodDays: 30,
	}, []OrderItem{{ProductID: productID, Quantity: 1}})
	if err != nil {
		t.Fatal(err)
	}
	newSub := subscriptionFor(newOrder, 42, productID, "new-charge")
	if err := store.UpdateOrderStatusWithSubscription(context.Background(), newOrder,
		OrderStatusPending, OrderStatusPaid, "stars", newSub.ChargeID, newSub); err != nil {
		t.Fatal(err)
	}
	var status, charge string
	var gotOrder int64
	if err := db.Conn().QueryRow(`SELECT status, telegram_charge_id, order_id FROM subscriptions WHERE user_id=42 AND product_id=?`, productID).Scan(&status, &charge, &gotOrder); err != nil {
		t.Fatal(err)
	}
	if status != SubStatusActive || charge != "new-charge" || gotOrder != newOrder {
		t.Fatalf("status=%s charge=%s order=%d", status, charge, gotOrder)
	}
}
