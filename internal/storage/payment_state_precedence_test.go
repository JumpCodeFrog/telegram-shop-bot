package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestDeliveryPreservesPartiallyRefundedState(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "delivery-refund.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, _ := seedLedgerOrder(t, db, 100)
	ctx := context.Background()
	if err := store.UpdateOrderStatus(ctx, orderID, OrderStatusPending, OrderStatusPaid, "stars", "capture"); err != nil {
		t.Fatal(err)
	}
	if err := NewSQLPaymentLedgerStore(db).RecordRefund(ctx, Refund{
		OrderID: orderID, Provider: "stars", ExternalID: "partial",
		PaymentExternalID: "capture", AmountMinor: 40, Currency: "XTR", Scale: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateOrderStatus(ctx, orderID, OrderStatusPaid, OrderStatusDelivered, "", ""); err != nil {
		t.Fatal(err)
	}
	order, _ := store.GetOrder(ctx, orderID)
	if order.Status != OrderStatusDelivered || order.PaymentState != PaymentStatePartiallyRefunded {
		t.Fatalf("order=%+v", order)
	}
}

func TestRefundDoesNotResolveNeedsReview(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "review-refund.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, _ := seedLedgerOrder(t, db, 100)
	ctx := context.Background()
	if err := store.UpdateOrderStatus(ctx, orderID, OrderStatusPending, OrderStatusPaid, "stars", "capture"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUnexpectedPayment(ctx, orderID, "stars", "second", "second_charge"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatal(err)
	}
	if err := NewSQLPaymentLedgerStore(db).RecordRefund(ctx, Refund{
		OrderID: orderID, Provider: "stars", ExternalID: "refund",
		PaymentExternalID: "capture", AmountMinor: 100, Currency: "XTR", Scale: 0,
	}); err != nil {
		t.Fatal(err)
	}
	order, _ := store.GetOrder(ctx, orderID)
	if order.PaymentState != PaymentStateNeedsReview {
		t.Fatalf("payment_state=%s", order.PaymentState)
	}
}

func TestCancelDoesNotHideNeedsReviewCapture(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "review-cancel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, _ := seedLedgerOrder(t, db, 100)
	ctx := context.Background()
	if err := store.RecordUnexpectedPayment(ctx, orderID, "stars", "late", "provider_confirmed"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatal(err)
	}
	if err := store.CancelOrder(ctx, orderID, 42); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancel error=%v", err)
	}
	order, _ := store.GetOrder(ctx, orderID)
	if order.Status != OrderStatusPending || order.PaymentState != PaymentStateNeedsReview {
		t.Fatalf("order=%+v", order)
	}
}
