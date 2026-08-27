package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

var providerIngressTime = time.Unix(1_700_000_000, 0).UTC()

func TestProviderCaptureIngressQuarantinesWithoutFulfillmentAndReplaysExactly(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "capture-ingress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, orderID, productID := seedLedgerOrder(t, db, 100)
	ctx := context.Background()
	fact := PaymentFact{
		Provider: PaymentMethodStars, ExternalID: "provider-only-capture",
		PayerID: 42, AmountMinor: 100, Currency: "XTR", Scale: 0, OccurredAt: providerIngressTime,
	}
	audit := PaymentIngressAudit{Actor: "operator:test", Reason: "provider-only capture"}

	preview, err := store.PreviewProviderCaptureIngress(ctx, orderID, fact)
	if err != nil || preview != PaymentIngressQuarantine {
		t.Fatalf("initial preview=%q err=%v", preview, err)
	}
	if err := store.IngestProviderCapture(ctx, orderID, fact, audit); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("ingest error=%v", err)
	}

	assertProviderCaptureQuarantine(t, db, orderID, productID, 1, 1, 1)
	preview, err = store.PreviewProviderCaptureIngress(ctx, orderID, fact)
	if err != nil || preview != PaymentIngressReplay {
		t.Fatalf("replay preview=%q err=%v", preview, err)
	}
	if err := store.IngestProviderCapture(ctx, orderID, fact, audit); err != nil {
		t.Fatalf("exact replay error=%v", err)
	}
	assertProviderCaptureQuarantine(t, db, orderID, productID, 1, 1, 1)
}

func assertProviderCaptureQuarantine(t *testing.T, db *DB, orderID, productID int64, wantAttempts, wantEvents, wantAudits int) {
	t.Helper()
	var (
		status, orderState, paymentState, fulfillmentState, paymentID string
		stock, attempts, events, audits, loyalty, referrals           int
	)
	if err := db.Conn().QueryRow(`
		SELECT status, order_state, payment_state, fulfillment_state, COALESCE(payment_id, '')
		FROM orders WHERE id = ?`, orderID).Scan(
		&status, &orderState, &paymentState, &fulfillmentState, &paymentID); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT stock FROM products WHERE id = ?`, productID).Scan(&stock); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts
		WHERE order_id = ? AND provider = 'stars' AND external_id = 'provider-only-capture'
		  AND status = 'needs_review'`, orderID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events
		WHERE order_id = ? AND provider = 'stars' AND event_kind = 'captured'
		  AND external_id = 'provider-only-capture' AND disposition = 'needs_review'`, orderID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_ingress_audits a
		JOIN payment_events e ON a.target_kind='payment_event' AND a.target_id=e.id
		WHERE a.order_id=? AND a.provider='stars' AND a.event_kind='captured'
		  AND a.actor='operator:test' AND a.reason='provider-only capture'
		  AND e.external_id='provider-only-capture'`, orderID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM loyalty_txs WHERE ref_id = ?`, orderID).Scan(&loyalty); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM referral_awards`).Scan(&referrals); err != nil {
		t.Fatal(err)
	}
	if status != OrderStatusPending || orderState != OrderStatePlaced || paymentState != PaymentStateNeedsReview ||
		fulfillmentState != FulfillmentStateUnfulfilled || paymentID != "" || stock != 100 ||
		attempts != wantAttempts || events != wantEvents || audits != wantAudits || loyalty != 0 || referrals != 0 {
		t.Fatalf("status=%s order=%s payment=%s fulfillment=%s payment_id=%q stock=%d attempts=%d events=%d audits=%d loyalty=%d referrals=%d",
			status, orderState, paymentState, fulfillmentState, paymentID, stock, attempts, events, audits, loyalty, referrals)
	}
}

func TestProviderRefundIngressPreviewApplyReplayAndGreenReconciliation(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "refund-ingress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, orderID, _ := seedLedgerOrder(t, db, 100)
	ctx := context.Background()
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, PaymentFact{
		Provider: PaymentMethodStars, ExternalID: "settled-capture", PayerID: 42,
		AmountMinor: 100, Currency: "XTR", Scale: 0, OccurredAt: providerIngressTime,
	}); err != nil {
		t.Fatal(err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	refund := Refund{
		OrderID: orderID, Provider: PaymentMethodStars, ExternalID: "settled-capture",
		PaymentExternalID: "settled-capture", PayerID: 42,
		AmountMinor: 100, Currency: "XTR", Scale: 0, OccurredAt: providerIngressTime.Add(time.Minute),
	}

	preview, err := ledger.PreviewProviderRefundIngress(ctx, refund)
	if err != nil || preview != PaymentIngressApply {
		t.Fatalf("initial preview=%q err=%v", preview, err)
	}
	audit := PaymentIngressAudit{Actor: "operator:test", Reason: "provider-only refund"}
	if err := ledger.IngestProviderRefund(ctx, refund, audit); err != nil {
		t.Fatal(err)
	}
	preview, err = ledger.PreviewProviderRefundIngress(ctx, refund)
	if err != nil || preview != PaymentIngressReplay {
		t.Fatalf("replay preview=%q err=%v", preview, err)
	}
	if err := ledger.IngestProviderRefund(ctx, refund, audit); err != nil {
		t.Fatalf("exact replay error=%v", err)
	}

	var refunds, refundEvents, audits int
	var state string
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM refunds
		WHERE order_id=? AND provider='stars' AND external_id='settled-capture'
		  AND payment_external_id='settled-capture' AND status='succeeded'`, orderID).Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events
		WHERE order_id=? AND provider='stars' AND event_kind='refunded'
		  AND external_id='settled-capture' AND disposition='settled'`, orderID).Scan(&refundEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_ingress_audits a
		JOIN refunds r ON a.target_kind='refund' AND a.target_id=r.id
		WHERE a.order_id=? AND a.provider='stars' AND a.event_kind='refunded'
		  AND a.actor='operator:test' AND a.reason='provider-only refund'
		  AND r.external_id='settled-capture'`, orderID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if refunds != 1 || refundEvents != 1 || audits != 1 || state != PaymentStateRefunded {
		t.Fatalf("refunds=%d refund_events=%d audits=%d state=%s", refunds, refundEvents, audits, state)
	}

	report, err := ledger.Reconcile(ctx, PaymentMethodStars, []ProviderTransaction{
		{Provider: PaymentMethodStars, Kind: PaymentEventCaptured, ExternalID: "settled-capture", OrderID: orderID, PayloadValid: true, AmountMinor: 100, Currency: "XTR", Scale: 0, PayerID: 42, OccurredAt: providerIngressTime},
		{Provider: PaymentMethodStars, Kind: PaymentEventRefunded, ExternalID: "settled-capture", OrderID: orderID, PayloadValid: true, AmountMinor: 100, Currency: "XTR", Scale: 0, PayerID: 42, OccurredAt: providerIngressTime.Add(time.Minute)},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !report.WindowComplete || report.ProviderRows != 2 || report.Matched != 2 || report.ProviderOnly != 0 ||
		report.LocalOnly != 0 || report.AmountMismatch != 0 || report.DuplicateRows != 0 || report.NeedsReview != 0 {
		t.Fatalf("reconciliation=%+v", report)
	}
}

func TestProviderRefundIngressAuditsDurableAnomalyAtomically(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "refund-ingress-anomaly.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, orderID, _ := seedLedgerOrder(t, db, 100)
	ctx := context.Background()
	refund := Refund{
		OrderID: orderID, Provider: PaymentMethodStars, ExternalID: "missing-capture",
		PaymentExternalID: "missing-capture", PayerID: 42,
		AmountMinor: 100, Currency: "XTR", Scale: 0, OccurredAt: providerIngressTime,
	}
	audit := PaymentIngressAudit{Actor: "operator:test", Reason: "provider-only orphan refund"}
	ledger := NewSQLPaymentLedgerStore(db)
	for attempt := 0; attempt < 2; attempt++ {
		if err := ledger.IngestProviderRefund(ctx, refund, audit); !errors.Is(err, ErrNotFound) {
			t.Fatalf("attempt=%d error=%v", attempt, err)
		}
	}
	var anomalies, audits int
	var state string
	if err := db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies
		WHERE proposed_order_id=? AND event_kind='refunded' AND reason='refund_parent_not_found'`, orderID).Scan(&anomalies); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_ingress_audits a
		JOIN payment_anomalies p ON a.target_kind='payment_anomaly' AND a.target_id=p.id
		WHERE a.order_id=? AND a.event_kind='refunded' AND a.actor='operator:test'
		  AND a.reason='provider-only orphan refund'`, orderID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if state != PaymentStateNeedsReview || anomalies != 1 || audits != 1 {
		t.Fatalf("state=%s anomalies=%d audits=%d", state, anomalies, audits)
	}
	if _, err := db.Conn().Exec(`UPDATE payment_ingress_audits SET reason='changed'`); err == nil {
		t.Fatal("payment ingress audit update unexpectedly succeeded")
	}
	if _, err := db.Conn().Exec(`DELETE FROM payment_ingress_audits`); err == nil {
		t.Fatal("payment ingress audit delete unexpectedly succeeded")
	}
}

func TestProviderIngressRollsBackFactWhenAuditAppendFails(t *testing.T) {
	t.Run("capture", func(t *testing.T) {
		db, err := New(filepath.Join(t.TempDir(), "capture-audit-failure.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		store, orderID, _ := seedLedgerOrder(t, db, 100)
		if _, err := db.Conn().Exec(`DROP TABLE payment_ingress_audits`); err != nil {
			t.Fatal(err)
		}
		err = store.IngestProviderCapture(context.Background(), orderID, PaymentFact{
			Provider: PaymentMethodStars, ExternalID: "capture-audit-failure",
			PayerID: 42, AmountMinor: 100, Currency: "XTR", Scale: 0, OccurredAt: providerIngressTime,
		}, PaymentIngressAudit{Actor: "operator:test", Reason: "prove atomic rollback"})
		if err == nil || errors.Is(err, ErrPaymentNeedsReview) {
			t.Fatalf("ingest error=%v", err)
		}
		var state string
		var attempts, events int
		_ = db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state)
		_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, orderID).Scan(&attempts)
		_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events WHERE order_id=?`, orderID).Scan(&events)
		if state != PaymentStatePending || attempts != 0 || events != 0 {
			t.Fatalf("state=%s attempts=%d events=%d", state, attempts, events)
		}
	})

	t.Run("refund", func(t *testing.T) {
		db, err := New(filepath.Join(t.TempDir(), "refund-audit-failure.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		store, orderID, _ := seedLedgerOrder(t, db, 100)
		ctx := context.Background()
		if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, PaymentFact{
			Provider: PaymentMethodStars, ExternalID: "capture", PayerID: 42,
			AmountMinor: 100, Currency: "XTR", Scale: 0, OccurredAt: providerIngressTime,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Conn().Exec(`DROP TABLE payment_ingress_audits`); err != nil {
			t.Fatal(err)
		}
		err = NewSQLPaymentLedgerStore(db).IngestProviderRefund(ctx, Refund{
			OrderID: orderID, Provider: PaymentMethodStars, ExternalID: "capture",
			PaymentExternalID: "capture", PayerID: 42,
			AmountMinor: 100, Currency: "XTR", Scale: 0, OccurredAt: providerIngressTime.Add(time.Minute),
		}, PaymentIngressAudit{Actor: "operator:test", Reason: "prove atomic rollback"})
		if err == nil {
			t.Fatal("refund unexpectedly succeeded without audit table")
		}
		var state string
		var refunds, events int
		_ = db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state)
		_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM refunds WHERE order_id=?`, orderID).Scan(&refunds)
		_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events WHERE order_id=? AND event_kind='refunded'`, orderID).Scan(&events)
		if state != PaymentStateSettled || refunds != 0 || events != 0 {
			t.Fatalf("state=%s refunds=%d events=%d", state, refunds, events)
		}
	})
}
