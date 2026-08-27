package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestRefundOfNeedsReviewCaptureIsDurableAndQuarantined(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "review-capture-refund.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, orderID, _ := seedLedgerOrder(t, db, 100)
	ctx := context.Background()
	if err := store.UpdateOrderStatus(ctx, orderID, OrderStatusPending, OrderStatusPaid, "stars", "capture"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUnexpectedPayment(ctx, orderID, "stars", "second-capture", "second_charge"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("unexpected capture error=%v", err)
	}

	ledger := NewSQLPaymentLedgerStore(db)
	refund := Refund{
		OrderID: orderID, Provider: "stars", ExternalID: "second-refund",
		PaymentExternalID: "second-capture", AmountMinor: 100, Currency: "XTR", Scale: 0,
	}
	if err := ledger.RecordRefund(ctx, refund); err != nil {
		t.Fatal(err)
	}
	if err := ledger.RecordRefund(ctx, refund); err != nil {
		t.Fatalf("exact replay error=%v", err)
	}

	var refunds, events int
	var status, disposition, state string
	if err := db.Conn().QueryRow(`SELECT COUNT(*), MIN(status) FROM refunds WHERE provider='stars' AND external_id='second-refund'`).Scan(&refunds, &status); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*), MIN(disposition) FROM payment_events WHERE provider='stars' AND event_kind='refunded' AND external_id='second-refund'`).Scan(&events, &disposition); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if refunds != 1 || events != 1 || status != "succeeded" || disposition != PaymentDispositionNeedsReview || state != PaymentStateNeedsReview {
		t.Fatalf("refunds=%d events=%d status=%s disposition=%s state=%s", refunds, events, status, disposition, state)
	}

	conflict := refund
	conflict.AmountMinor = 99
	if err := ledger.RecordRefund(ctx, conflict); !errors.Is(err, ErrPaymentIdentityConflict) {
		t.Fatalf("conflicting replay error=%v", err)
	}
	if err := ledger.RecordRefund(ctx, conflict); !errors.Is(err, ErrPaymentIdentityConflict) {
		t.Fatalf("conflicting replay retry error=%v", err)
	}
	var anomalies int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies
		WHERE proposed_order_id=? AND event_kind='refunded' AND external_id='second-refund'
		  AND related_external_id='second-capture' AND reason='refund_identity_conflict'`, orderID).Scan(&anomalies); err != nil {
		t.Fatal(err)
	}
	if anomalies != 1 {
		t.Fatalf("conflicting replay anomalies=%d", anomalies)
	}
}

func TestProviderConfirmedOverRefundIsDurablyQuarantined(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "over-refund.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, orderID, _ := seedLedgerOrder(t, db, 100)
	ctx := context.Background()
	if err := store.UpdateOrderStatus(ctx, orderID, OrderStatusPending, OrderStatusPaid, "stars", "capture"); err != nil {
		t.Fatal(err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	over := Refund{
		OrderID: orderID, Provider: "stars", ExternalID: "refund-over",
		PaymentExternalID: "capture", AmountMinor: 101, Currency: "XTR", Scale: 0,
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := ledger.RecordRefund(ctx, over); !errors.Is(err, ErrRefundExceedsPayment) {
			t.Fatalf("attempt=%d over-refund error=%v", attempt, err)
		}
	}

	var refunds, events, anomalies int
	var state string
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM refunds WHERE external_id='refund-over'`).Scan(&refunds)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events WHERE event_kind='refunded' AND external_id='refund-over'`).Scan(&events)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies
		WHERE proposed_order_id=? AND event_kind='refunded' AND external_id='refund-over'
		  AND related_external_id='capture' AND reason='refund_exceeds_payment'`, orderID).Scan(&anomalies)
	_ = db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state)
	if refunds != 0 || events != 0 || anomalies != 1 || state != PaymentStateNeedsReview {
		t.Fatalf("refunds=%d events=%d anomalies=%d state=%s", refunds, events, anomalies, state)
	}
}

func TestReusedRefundIdentityQuarantinesConflictingOrder(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "refund-identity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, first, _ := seedLedgerOrder(t, db, 100)
	second, err := store.CreateOrder(context.Background(), &Order{UserID: 43, TotalStars: 100, Status: OrderStatusPending}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.UpdateOrderStatus(ctx, first, OrderStatusPending, OrderStatusPaid, "stars", "capture-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateOrderStatus(ctx, second, OrderStatusPending, OrderStatusPaid, "stars", "capture-b"); err != nil {
		t.Fatal(err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	if err := ledger.RecordRefund(ctx, Refund{
		OrderID: first, Provider: "stars", ExternalID: "refund-shared",
		PaymentExternalID: "capture-a", AmountMinor: 25, Currency: "XTR", Scale: 0,
	}); err != nil {
		t.Fatal(err)
	}
	conflict := Refund{
		OrderID: second, Provider: "stars", ExternalID: "refund-shared",
		PaymentExternalID: "capture-b", AmountMinor: 25, Currency: "XTR", Scale: 0,
	}
	if err := ledger.RecordRefund(ctx, conflict); !errors.Is(err, ErrPaymentIdentityConflict) {
		t.Fatalf("identity conflict error=%v", err)
	}

	var firstRefunds, secondRefunds, anomalies int
	var secondState string
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM refunds WHERE order_id=? AND external_id='refund-shared'`, first).Scan(&firstRefunds)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM refunds WHERE order_id=? AND external_id='refund-shared'`, second).Scan(&secondRefunds)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies
		WHERE proposed_order_id=? AND event_kind='refunded' AND external_id='refund-shared'
		  AND related_external_id='capture-b' AND reason='refund_identity_conflict'`, second).Scan(&anomalies)
	_ = db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, second).Scan(&secondState)
	if firstRefunds != 1 || secondRefunds != 0 || anomalies != 1 || secondState != PaymentStateNeedsReview {
		t.Fatalf("first=%d second=%d anomalies=%d state=%s", firstRefunds, secondRefunds, anomalies, secondState)
	}
}

func TestInvalidProviderConfirmedRefundFactsAreDurablyQuarantined(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Refund)
		want   error
	}{
		{name: "zero amount", mutate: func(r *Refund) { r.AmountMinor = 0 }, want: ErrInvalidMoney},
		{name: "negative amount", mutate: func(r *Refund) { r.AmountMinor = -1 }, want: ErrInvalidMoney},
		{name: "scale out of range", mutate: func(r *Refund) { r.Scale = 10 }, want: ErrPaymentReceiptMismatch},
		{name: "blank refund identity", mutate: func(r *Refund) { r.ExternalID = "" }, want: ErrInvalidMoney},
		{name: "blank capture identity", mutate: func(r *Refund) { r.PaymentExternalID = "" }, want: ErrInvalidMoney},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, err := New(filepath.Join(t.TempDir(), "invalid-refund.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()
			store, orderID, _ := seedLedgerOrder(t, db, 100)
			if err := store.UpdateOrderStatus(ctx, orderID, OrderStatusPending, OrderStatusPaid, "stars", "capture"); err != nil {
				t.Fatal(err)
			}
			refund := Refund{
				OrderID: orderID, Provider: "stars", ExternalID: "refund",
				PaymentExternalID: "capture", AmountMinor: 100, Currency: "XTR", Scale: 0,
			}
			tc.mutate(&refund)
			ledger := NewSQLPaymentLedgerStore(db)
			for attempt := 0; attempt < 2; attempt++ {
				if err := ledger.RecordRefund(ctx, refund); !errors.Is(err, tc.want) {
					t.Fatalf("attempt=%d error=%v want=%v", attempt, err, tc.want)
				}
			}
			var anomalies int
			var state string
			_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies
				WHERE proposed_order_id=? AND event_kind='refunded' AND reason='refund_invalid_provider_fact'`, orderID).Scan(&anomalies)
			_ = db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state)
			if anomalies != 1 || state != PaymentStateNeedsReview {
				t.Fatalf("anomalies=%d state=%s", anomalies, state)
			}
		})
	}
}
