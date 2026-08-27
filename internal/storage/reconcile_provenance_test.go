package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestStarsReconcileRequiresExactPayerAndProviderTimestamp(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "reconcile-provenance.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, _ := seedLedgerOrder(t, db, 25)
	providerTime := time.Unix(1_700_000_000, 0).UTC()
	if err := store.UpdateOrderStatusWithPaymentFact(context.Background(), orderID,
		OrderStatusPending, OrderStatusPaid, PaymentFact{
			Provider: PaymentMethodStars, ExternalID: "provenance-capture", PayerID: 42,
			AmountMinor: 25, Currency: "XTR", Scale: 0, OccurredAt: providerTime,
		}); err != nil {
		t.Fatal(err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	valid := ProviderTransaction{
		Kind: PaymentEventCaptured, ExternalID: "provenance-capture",
		OrderID: orderID, PayloadValid: true, PayerID: 42,
		AmountMinor: 25, Currency: "XTR", Scale: 0,
		OccurredAt: providerTime,
	}

	for _, tc := range []struct {
		name string
		row  ProviderTransaction
	}{
		{name: "wrong payer", row: func() ProviderTransaction { row := valid; row.PayerID = 43; return row }()},
		{name: "missing timestamp", row: func() ProviderTransaction { row := valid; row.OccurredAt = time.Time{}; return row }()},
		{name: "wrong timestamp", row: func() ProviderTransaction {
			row := valid
			row.OccurredAt = row.OccurredAt.Add(24 * time.Hour)
			return row
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := ledger.Reconcile(context.Background(), PaymentMethodStars, []ProviderTransaction{tc.row}, false)
			if err != nil {
				t.Fatal(err)
			}
			if report.Matched != 0 || report.AmountMismatch != 1 || report.NeedsReview != 1 {
				t.Fatalf("report=%+v", report)
			}
		})
	}

	report, err := ledger.Reconcile(context.Background(), PaymentMethodStars, []ProviderTransaction{valid}, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Matched != 1 || report.AmountMismatch != 0 || report.NeedsReview != 0 {
		t.Fatalf("valid report=%+v", report)
	}
}
