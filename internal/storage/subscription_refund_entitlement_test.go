package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestFullSubscriptionRefundWithoutEntitlementRowIsDiscoverableReview(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "missing-entitlement-refund.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, orderID, _, initial := seedSubscriptionLedgerOrder(t, db, 42, 100)
	if _, err := db.Conn().Exec(`DELETE FROM subscriptions WHERE order_id=?`, orderID); err != nil {
		t.Fatal(err)
	}
	err = NewSQLPaymentLedgerStore(db).RecordRefund(context.Background(), Refund{
		OrderID: orderID, Provider: PaymentMethodStars, ExternalID: "missing-entitlement-refund",
		PaymentExternalID: initial.ChargeID, AmountMinor: 100, Currency: "XTR", Scale: 0,
	})
	if !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("refund error=%v", err)
	}
	order, err := store.GetOrder(context.Background(), orderID)
	if err != nil {
		t.Fatal(err)
	}
	var reviewEvents int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events
		WHERE order_id=? AND event_kind='refunded' AND disposition='needs_review'`, orderID).Scan(&reviewEvents); err != nil {
		t.Fatal(err)
	}
	if order.PaymentState != PaymentStateNeedsReview || reviewEvents != 1 {
		t.Fatalf("payment_state=%s review_events=%d", order.PaymentState, reviewEvents)
	}
}

func TestFullInitialSubscriptionRefundRevokesEntitlement(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "initial-subscription-refund.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, orderID, productID, initial := seedSubscriptionLedgerOrder(t, db, 42, 100)
	before := time.Now().UTC()
	if err := NewSQLPaymentLedgerStore(db).RecordRefund(context.Background(), Refund{
		OrderID: orderID, Provider: PaymentMethodStars, ExternalID: "initial-refund",
		PaymentExternalID: initial.ChargeID, AmountMinor: 100, Currency: "XTR", Scale: 0,
	}); err != nil {
		t.Fatal(err)
	}

	var status string
	var expiry time.Time
	if err := db.Conn().QueryRow(`SELECT status, expires_at FROM subscriptions
		WHERE user_id=? AND product_id=?`, initial.UserID, productID).Scan(&status, &expiry); err != nil {
		t.Fatal(err)
	}
	if status != SubStatusExpired || expiry.After(time.Now().UTC()) || expiry.Before(before.Add(-time.Second)) {
		t.Fatalf("status=%s expiry=%v before=%v", status, expiry, before)
	}
}

func TestRefundedRenewalsRollEntitlementBackOnePaidPeriodAtATime(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "renewal-refund-rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, orderID, productID, initial := seedSubscriptionLedgerOrder(t, db, 42, 100)
	ctx := context.Background()
	first := initial
	first.ChargeID = "renewal-one"
	first.ExpiresAt = initial.ExpiresAt.Add(30 * 24 * time.Hour)
	if err := store.RecordSubscriptionRenewal(ctx, orderID, PaymentMethodStars, first.ChargeID, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ChargeID = "renewal-two"
	second.ExpiresAt = first.ExpiresAt.Add(30 * 24 * time.Hour)
	if err := store.RecordSubscriptionRenewal(ctx, orderID, PaymentMethodStars, second.ChargeID, second); err != nil {
		t.Fatal(err)
	}

	ledger := NewSQLPaymentLedgerStore(db)
	if err := ledger.RecordRefund(ctx, Refund{
		OrderID: orderID, Provider: PaymentMethodStars, ExternalID: "refund-two",
		PaymentExternalID: second.ChargeID, AmountMinor: 100, Currency: "XTR", Scale: 0,
	}); err != nil {
		t.Fatal(err)
	}
	assertSubscriptionExpiry(t, db, initial.UserID, productID, first.ExpiresAt, SubStatusActive)

	if err := ledger.RecordRefund(ctx, Refund{
		OrderID: orderID, Provider: PaymentMethodStars, ExternalID: "refund-one",
		PaymentExternalID: first.ChargeID, AmountMinor: 100, Currency: "XTR", Scale: 0,
	}); err != nil {
		t.Fatal(err)
	}
	assertSubscriptionExpiry(t, db, initial.UserID, productID, initial.ExpiresAt, SubStatusActive)
}

func TestPartialRenewalRefundKeepsGrantedEntitlement(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "partial-renewal-refund.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, orderID, productID, initial := seedSubscriptionLedgerOrder(t, db, 42, 100)
	renewal := initial
	renewal.ChargeID = "partial-renewal"
	renewal.ExpiresAt = initial.ExpiresAt.Add(30 * 24 * time.Hour)
	ctx := context.Background()
	if err := store.RecordSubscriptionRenewal(ctx, orderID, PaymentMethodStars, renewal.ChargeID, renewal); err != nil {
		t.Fatal(err)
	}
	if err := NewSQLPaymentLedgerStore(db).RecordRefund(ctx, Refund{
		OrderID: orderID, Provider: PaymentMethodStars, ExternalID: "partial-refund",
		PaymentExternalID: renewal.ChargeID, AmountMinor: 40, Currency: "XTR", Scale: 0,
	}); err != nil {
		t.Fatal(err)
	}
	assertSubscriptionExpiry(t, db, initial.UserID, productID, renewal.ExpiresAt, SubStatusActive)
}

func assertSubscriptionExpiry(t *testing.T, db *DB, userID, productID int64, want time.Time, wantStatus string) {
	t.Helper()
	var status string
	var got time.Time
	if err := db.Conn().QueryRow(`SELECT status, expires_at FROM subscriptions
		WHERE user_id=? AND product_id=?`, userID, productID).Scan(&status, &got); err != nil {
		t.Fatal(err)
	}
	if status != wantStatus || got.Unix() != want.Unix() {
		t.Fatalf("status=%s expiry=%v want_status=%s want_expiry=%v", status, got, wantStatus, want)
	}
}
