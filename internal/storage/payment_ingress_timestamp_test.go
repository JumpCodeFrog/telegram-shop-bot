package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestProviderCaptureIngressRejectsConflictingProviderTimestamp(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "capture-timestamp-conflict.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, _ := seedLedgerOrder(t, db, 100)
	ctx := context.Background()
	originalTime := time.Unix(1_700_000_000, 0).UTC()
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID,
		OrderStatusPending, OrderStatusPaid, PaymentFact{
			Provider: PaymentMethodStars, ExternalID: "timestamp-capture", PayerID: 42,
			AmountMinor: 100, Currency: "XTR", Scale: 0, OccurredAt: originalTime,
		}); err != nil {
		t.Fatal(err)
	}
	conflict := PaymentFact{
		Provider: PaymentMethodStars, ExternalID: "timestamp-capture", PayerID: 42,
		AmountMinor: 100, Currency: "XTR", Scale: 0, OccurredAt: originalTime.Add(24 * time.Hour),
	}
	if preview, err := store.PreviewProviderCaptureIngress(ctx, orderID, conflict); err != nil || preview != PaymentIngressQuarantine {
		t.Fatalf("preview=%q err=%v", preview, err)
	}
	if err := store.IngestProviderCapture(ctx, orderID, conflict,
		PaymentIngressAudit{Actor: "operator:test", Reason: "timestamp mismatch"}); !errors.Is(err, ErrPaymentIdentityConflict) {
		t.Fatalf("ingest error=%v", err)
	}
	var persisted time.Time
	if err := db.Conn().QueryRow(`SELECT occurred_at FROM payment_attempts
		WHERE provider='stars' AND external_id='timestamp-capture'`).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if !persisted.Equal(originalTime) {
		t.Fatalf("persisted=%v original=%v", persisted, originalTime)
	}
}

func TestProviderRefundIngressRejectsConflictingProviderTimestamp(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "refund-timestamp-conflict.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, _ := seedLedgerOrder(t, db, 100)
	ctx := context.Background()
	originalTime := time.Unix(1_700_000_000, 0).UTC()
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID,
		OrderStatusPending, OrderStatusPaid, PaymentFact{
			Provider: PaymentMethodStars, ExternalID: "timestamp-refund", PayerID: 42,
			AmountMinor: 100, Currency: "XTR", Scale: 0, OccurredAt: originalTime,
		}); err != nil {
		t.Fatal(err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	refund := Refund{
		OrderID: orderID, Provider: PaymentMethodStars,
		ExternalID: "timestamp-refund", PaymentExternalID: "timestamp-refund", PayerID: 42,
		AmountMinor: 100, Currency: "XTR", Scale: 0, OccurredAt: originalTime.Add(time.Minute),
	}
	audit := PaymentIngressAudit{Actor: "operator:test", Reason: "authoritative refund"}
	if err := ledger.IngestProviderRefund(ctx, refund, audit); err != nil {
		t.Fatal(err)
	}
	conflict := refund
	conflict.OccurredAt = refund.OccurredAt.Add(24 * time.Hour)
	if preview, err := ledger.PreviewProviderRefundIngress(ctx, conflict); err != nil || preview != PaymentIngressQuarantine {
		t.Fatalf("preview=%q err=%v", preview, err)
	}
	if err := ledger.IngestProviderRefund(ctx, conflict, audit); !errors.Is(err, ErrPaymentIdentityConflict) {
		t.Fatalf("ingest error=%v", err)
	}
	var persisted time.Time
	if err := db.Conn().QueryRow(`SELECT completed_at FROM refunds
		WHERE provider='stars' AND external_id='timestamp-refund'`).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if !persisted.Equal(refund.OccurredAt) {
		t.Fatalf("persisted=%v original=%v", persisted, refund.OccurredAt)
	}
}
