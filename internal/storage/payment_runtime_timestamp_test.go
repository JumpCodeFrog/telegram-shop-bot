package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSettledCaptureReplayWithDifferentProviderTimestampIsQuarantined(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "settled-timestamp-replay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, _ := seedLedgerOrder(t, db, 100)
	ctx := context.Background()
	t1 := time.Unix(1_700_000_000, 0).UTC()
	fact := PaymentFact{Provider: PaymentMethodStars, ExternalID: "settled-time", PayerID: 42,
		AmountMinor: 100, Currency: "XTR", Scale: 0, OccurredAt: t1}
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, fact); err != nil {
		t.Fatal(err)
	}
	fact.OccurredAt = t1.Add(time.Hour)
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, fact); !errors.Is(err, ErrPaymentIdentityConflict) {
		t.Fatalf("timestamp replay error=%v", err)
	}
	assertTimestampConflictAnomaly(t, db, orderID, fact.ExternalID, fact.OccurredAt)
}

func TestResolvedNeedsReviewReplayWithDifferentProviderTimestampReopensReview(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "resolved-timestamp-replay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, _ := seedLedgerOrder(t, db, 100)
	ctx := context.Background()
	t1 := time.Unix(1_700_000_000, 0).UTC()
	fact := PaymentFact{Provider: PaymentMethodStars, ExternalID: "review-time", PayerID: 42,
		AmountMinor: 100, Currency: "XTR", Scale: 0, OccurredAt: t1}
	if err := store.RecordUnexpectedPaymentFact(ctx, orderID, fact, "provider_confirmed"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatal(err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	if err := ledger.RecordRefund(ctx, Refund{OrderID: orderID, Provider: PaymentMethodStars,
		ExternalID: "review-time-refund", PaymentExternalID: fact.ExternalID, PayerID: 42,
		AmountMinor: 100, Currency: "XTR", Scale: 0, OccurredAt: t1.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	cases, err := ledger.ListPaymentReviews(ctx, PaymentMethodStars)
	if err != nil || len(cases) != 1 {
		t.Fatalf("cases=%+v err=%v", cases, err)
	}
	resolution := PaymentReviewResolution{OrderID: orderID, Provider: PaymentMethodStars,
		Actor: "operator:test", Reason: "capture compensated", ResultingPaymentState: PaymentStateCancelled}
	for _, target := range cases[0].Targets {
		switch target.Kind {
		case PaymentReviewTargetEvent:
			resolution.EventIDs = append(resolution.EventIDs, target.ID)
		case PaymentReviewTargetAnomaly:
			resolution.AnomalyIDs = append(resolution.AnomalyIDs, target.ID)
		}
	}
	if err := ledger.ResolvePaymentReview(ctx, resolution); err != nil {
		t.Fatal(err)
	}
	fact.OccurredAt = t1.Add(time.Hour)
	if err := store.RecordUnexpectedPaymentFact(ctx, orderID, fact, "provider_redelivery"); !errors.Is(err, ErrPaymentIdentityConflict) {
		t.Fatalf("timestamp replay error=%v", err)
	}
	assertTimestampConflictAnomaly(t, db, orderID, fact.ExternalID, fact.OccurredAt)
}

func TestSubscriptionRenewalReplayWithDifferentProviderTimestampIsQuarantined(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "renewal-timestamp-replay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, _, initial := seedSubscriptionLedgerOrder(t, db, 42, 100)
	ctx := context.Background()
	t1 := time.Unix(1_700_000_000, 0).UTC()
	renewal := initial
	renewal.ChargeID = "renewal-time"
	renewal.ExpiresAt = initial.ExpiresAt.Add(30 * 24 * time.Hour).UTC().Truncate(time.Second)
	fact := PaymentFact{Provider: PaymentMethodStars, ExternalID: renewal.ChargeID, PayerID: 42,
		AmountMinor: 100, Currency: "XTR", Scale: 0, EntitlementExpiresAt: renewal.ExpiresAt, OccurredAt: t1}
	if err := store.RecordSubscriptionRenewalFact(ctx, orderID, fact, renewal); err != nil {
		t.Fatal(err)
	}
	fact.OccurredAt = t1.Add(time.Hour)
	if err := store.RecordSubscriptionRenewalFact(ctx, orderID, fact, renewal); !errors.Is(err, ErrPaymentIdentityConflict) {
		t.Fatalf("timestamp replay error=%v", err)
	}
	assertTimestampConflictAnomaly(t, db, orderID, fact.ExternalID, fact.OccurredAt)
}

func TestProviderOccurrenceColumnsAreImmutable(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "immutable-provider-times.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, _ := seedLedgerOrder(t, db, 100)
	ctx := context.Background()
	t1 := time.Unix(1_700_000_000, 0).UTC()
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, PaymentFact{
		Provider: PaymentMethodStars, ExternalID: "immutable-time", PayerID: 42,
		AmountMinor: 100, Currency: "XTR", Scale: 0, OccurredAt: t1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := NewSQLPaymentLedgerStore(db).RecordRefund(ctx, Refund{OrderID: orderID, Provider: PaymentMethodStars,
		ExternalID: "immutable-refund", PaymentExternalID: "immutable-time", PayerID: 42,
		AmountMinor: 100, Currency: "XTR", Scale: 0, OccurredAt: t1.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`UPDATE payment_attempts SET occurred_at=? WHERE external_id='immutable-time'`, t1.Add(time.Hour)); err == nil {
		t.Fatal("payment_attempts.occurred_at update unexpectedly succeeded")
	}
	if _, err := db.Conn().Exec(`UPDATE refunds SET completed_at=? WHERE external_id='immutable-refund'`, t1.Add(time.Hour)); err == nil {
		t.Fatal("refunds.completed_at update unexpectedly succeeded")
	}
}

func assertTimestampConflictAnomaly(t *testing.T, db *DB, orderID int64, externalID string, occurredAt time.Time) {
	t.Helper()
	var anomalies int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies
		WHERE proposed_order_id=? AND provider='stars' AND external_id=? AND occurred_at=?`,
		orderID, externalID, occurredAt.UTC()).Scan(&anomalies); err != nil {
		t.Fatal(err)
	}
	if anomalies != 1 {
		t.Fatalf("timestamp conflict anomalies=%d", anomalies)
	}
}
