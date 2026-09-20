package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedTONLedgerOrder mirrors seedStripeLedgerOrder for the on-chain TON
// rail: the order snapshots TotalTonNano 1500000000 (1.5 TON, scale 9) with
// payment method ton, so orderMoney derives the exact provider money tuple.
func seedTONLedgerOrder(t *testing.T, db *DB) (*SQLOrderStore, int64, int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Conn().ExecContext(ctx, `INSERT INTO categories (name) VALUES ('ton-ops')`); err != nil {
		t.Fatal(err)
	}
	res, err := db.Conn().ExecContext(ctx,
		`INSERT INTO products (category_id, name, price_usd, price_stars, stock, is_active)
		 VALUES (1, 'Gadget', 0, 0, 100, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	productID, _ := res.LastInsertId()
	store := NewSQLOrderStore(db)
	orderID, err := store.CreateOrder(ctx, &Order{
		UserID: 42, TotalTonNano: 1_500_000_000,
		PaymentMethod: PaymentMethodTON, Status: OrderStatusPending,
	}, []OrderItem{{ProductID: productID, Quantity: 1, PriceUSD: 0}})
	if err != nil {
		t.Fatal(err)
	}
	return store, orderID, productID
}

// seedNowpaymentsLedgerOrder mirrors seedStripeLedgerOrder for the
// NOWPayments rail: the order snapshots TotalUSD 1999.00 (199900 cents,
// scale 2) with payment method nowpayments.
func seedNowpaymentsLedgerOrder(t *testing.T, db *DB) (*SQLOrderStore, int64, int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Conn().ExecContext(ctx, `INSERT INTO categories (name) VALUES ('nowpayments-ops')`); err != nil {
		t.Fatal(err)
	}
	res, err := db.Conn().ExecContext(ctx,
		`INSERT INTO products (category_id, name, price_usd, price_stars, stock, is_active)
		 VALUES (1, 'Gadget', 1999.00, 0, 100, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	productID, _ := res.LastInsertId()
	store := NewSQLOrderStore(db)
	orderID, err := store.CreateOrder(ctx, &Order{
		UserID: 42, TotalUSD: 1999.00,
		PaymentMethod: PaymentMethodNowpayments, Status: OrderStatusPending,
	}, []OrderItem{{ProductID: productID, Quantity: 1, PriceUSD: 1999.00}})
	if err != nil {
		t.Fatal(err)
	}
	return store, orderID, productID
}

func TestTONRefundOfNeedsReviewCaptureIsDurable(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "ton-refund.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, orderID, _ := seedTONLedgerOrder(t, db)
	ctx := context.Background()
	if err := store.UpdateOrderStatus(ctx, orderID, OrderStatusPending, OrderStatusPaid, PaymentMethodTON, "ton-capture"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUnexpectedPayment(ctx, orderID, PaymentMethodTON, "ton-second-capture", "second_transfer"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("unexpected capture error=%v", err)
	}

	ledger := NewSQLPaymentLedgerStore(db)
	refund := Refund{
		OrderID: orderID, Provider: PaymentMethodTON, ExternalID: "ton-second-refund",
		PaymentExternalID: "ton-second-capture", AmountMinor: 1_500_000_000, Currency: "TON", Scale: 9,
	}
	if err := ledger.RecordRefund(ctx, refund); err != nil {
		t.Fatalf("ton refund error=%v", err)
	}
	if err := ledger.RecordRefund(ctx, refund); err != nil {
		t.Fatalf("exact replay error=%v", err)
	}

	var refunds, events int
	var status, disposition, state string
	if err := db.Conn().QueryRow(`SELECT COUNT(*), MIN(status) FROM refunds
		WHERE provider='ton' AND external_id='ton-second-refund'`).Scan(&refunds, &status); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*), MIN(disposition) FROM payment_events
		WHERE provider='ton' AND event_kind='refunded' AND external_id='ton-second-refund'`).Scan(&events, &disposition); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if refunds != 1 || events != 1 || status != "succeeded" || disposition != PaymentDispositionNeedsReview || state != PaymentStateNeedsReview {
		t.Fatalf("refunds=%d events=%d status=%s disposition=%s state=%s", refunds, events, status, disposition, state)
	}

	conflict := refund
	conflict.AmountMinor = 1_499_999_999
	if err := ledger.RecordRefund(ctx, conflict); !errors.Is(err, ErrPaymentIdentityConflict) {
		t.Fatalf("conflicting replay error=%v", err)
	}
	var anomalies int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies
		WHERE proposed_order_id=? AND event_kind='refunded' AND external_id='ton-second-refund'
		  AND related_external_id='ton-second-capture' AND reason='refund_identity_conflict'`, orderID).Scan(&anomalies); err != nil {
		t.Fatal(err)
	}
	if anomalies != 1 {
		t.Fatalf("conflicting replay anomalies=%d", anomalies)
	}

	// An invalid ton refund fact is quarantined under its own provider
	// identity instead of being relabeled through the order's payment method.
	invalid := refund
	invalid.ExternalID = "ton-invalid-refund"
	invalid.AmountMinor = 0
	if err := ledger.RecordRefund(ctx, invalid); !errors.Is(err, ErrInvalidMoney) {
		t.Fatalf("invalid refund error=%v", err)
	}
	var anomalyProvider, rawPayload string
	if err := db.Conn().QueryRow(`SELECT provider, raw_payload FROM payment_anomalies
		WHERE external_id='ton-invalid-refund'`).Scan(&anomalyProvider, &rawPayload); err != nil {
		t.Fatal(err)
	}
	if anomalyProvider != PaymentMethodTON || strings.Contains(rawPayload, "invalid_provider") {
		t.Fatalf("invalid refund anomaly provider=%s raw_payload=%q", anomalyProvider, rawPayload)
	}
}

func TestNowpaymentsRefundOfNeedsReviewCaptureIsDurable(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "nowpayments-refund.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, orderID, _ := seedNowpaymentsLedgerOrder(t, db)
	ctx := context.Background()
	if err := store.UpdateOrderStatus(ctx, orderID, OrderStatusPending, OrderStatusPaid, PaymentMethodNowpayments, "np-capture"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUnexpectedPayment(ctx, orderID, PaymentMethodNowpayments, "np-second-capture", "second_charge"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("unexpected capture error=%v", err)
	}

	ledger := NewSQLPaymentLedgerStore(db)
	refund := Refund{
		OrderID: orderID, Provider: PaymentMethodNowpayments, ExternalID: "np-second-refund",
		PaymentExternalID: "np-second-capture", AmountMinor: 199900, Currency: "USD", Scale: 2,
	}
	if err := ledger.RecordRefund(ctx, refund); err != nil {
		t.Fatalf("nowpayments refund error=%v", err)
	}
	if err := ledger.RecordRefund(ctx, refund); err != nil {
		t.Fatalf("exact replay error=%v", err)
	}

	var refunds, events int
	var status, disposition, state string
	if err := db.Conn().QueryRow(`SELECT COUNT(*), MIN(status) FROM refunds
		WHERE provider='nowpayments' AND external_id='np-second-refund'`).Scan(&refunds, &status); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*), MIN(disposition) FROM payment_events
		WHERE provider='nowpayments' AND event_kind='refunded' AND external_id='np-second-refund'`).Scan(&events, &disposition); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if refunds != 1 || events != 1 || status != "succeeded" || disposition != PaymentDispositionNeedsReview || state != PaymentStateNeedsReview {
		t.Fatalf("refunds=%d events=%d status=%s disposition=%s state=%s", refunds, events, status, disposition, state)
	}

	// An invalid nowpayments refund fact is quarantined under its own
	// provider identity instead of being relabeled through the order's
	// payment method.
	invalid := refund
	invalid.ExternalID = "np-invalid-refund"
	invalid.AmountMinor = 0
	if err := ledger.RecordRefund(ctx, invalid); !errors.Is(err, ErrInvalidMoney) {
		t.Fatalf("invalid refund error=%v", err)
	}
	var anomalyProvider, rawPayload string
	if err := db.Conn().QueryRow(`SELECT provider, raw_payload FROM payment_anomalies
		WHERE external_id='np-invalid-refund'`).Scan(&anomalyProvider, &rawPayload); err != nil {
		t.Fatal(err)
	}
	if anomalyProvider != PaymentMethodNowpayments || strings.Contains(rawPayload, "invalid_provider") {
		t.Fatalf("invalid refund anomaly provider=%s raw_payload=%q", anomalyProvider, rawPayload)
	}
}

func TestProviderRefundIngressPreviewsAndIngestsTON(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "ton-refund-ingress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, orderID, _ := seedTONLedgerOrder(t, db)
	ctx := context.Background()
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, PaymentFact{
		Provider: PaymentMethodTON, ExternalID: "ton-pay-1",
		AmountMinor: 1_500_000_000, Currency: "TON", Scale: 9, OccurredAt: providerIngressTime,
	}); err != nil {
		t.Fatal(err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	// The operator supplies the order's Telegram user as the refund payer
	// because the capture carries no payer id (on-chain transfers have no
	// Telegram payer identity). Within the capture cap and the exact parent
	// money tuple, the preview must agree with what ingest would do: apply.
	refund := Refund{
		OrderID: orderID, Provider: PaymentMethodTON, ExternalID: "ton-refund-1",
		PaymentExternalID: "ton-pay-1", PayerID: 42,
		AmountMinor: 1_500_000_000, Currency: "TON", Scale: 9, OccurredAt: providerIngressTime.Add(time.Minute),
	}
	preview, err := ledger.PreviewProviderRefundIngress(ctx, refund)
	if err != nil || preview != PaymentIngressApply {
		t.Fatalf("initial preview=%q err=%v", preview, err)
	}
	var prerecorded int
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM refunds`).Scan(&prerecorded)
	if prerecorded != 0 {
		t.Fatalf("preview wrote refunds=%d", prerecorded)
	}
	audit := PaymentIngressAudit{Actor: "operator:test", Reason: "provider-only refund"}
	if err := ledger.IngestProviderRefund(ctx, refund, audit); err != nil {
		t.Fatalf("ingest error=%v", err)
	}

	var refunds, refundEvents, audits int
	var state string
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM refunds
		WHERE order_id=? AND provider='ton' AND external_id='ton-refund-1'
		  AND payment_external_id='ton-pay-1' AND payer_id=42 AND status='succeeded'`, orderID).Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events
		WHERE order_id=? AND provider='ton' AND event_kind='refunded'
		  AND external_id='ton-refund-1' AND disposition='settled'`, orderID).Scan(&refundEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_ingress_audits a
		JOIN refunds r ON a.target_kind='refund' AND a.target_id=r.id
		WHERE a.order_id=? AND a.provider='ton' AND a.event_kind='refunded'
		  AND a.actor='operator:test' AND a.reason='provider-only refund'
		  AND r.external_id='ton-refund-1'`, orderID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if refunds != 1 || refundEvents != 1 || audits != 1 || state != PaymentStateRefunded {
		t.Fatalf("refunds=%d refund_events=%d audits=%d state=%s", refunds, refundEvents, audits, state)
	}

	// The exactly recorded refund previews as a replay, and replaying the
	// ingest stays idempotent.
	preview, err = ledger.PreviewProviderRefundIngress(ctx, refund)
	if err != nil || preview != PaymentIngressReplay {
		t.Fatalf("replay preview=%q err=%v", preview, err)
	}
	if err := ledger.IngestProviderRefund(ctx, refund, audit); err != nil {
		t.Fatalf("exact replay error=%v", err)
	}

	// An over-cap refund previews as review evidence only, and the ingest
	// rejects durably with a quarantine anomaly.
	over := Refund{
		OrderID: orderID, Provider: PaymentMethodTON, ExternalID: "ton-refund-2",
		PaymentExternalID: "ton-pay-1", PayerID: 42,
		AmountMinor: 1, Currency: "TON", Scale: 9, OccurredAt: providerIngressTime.Add(2 * time.Minute),
	}
	if preview, err := ledger.PreviewProviderRefundIngress(ctx, over); err != nil || preview != PaymentIngressQuarantine {
		t.Fatalf("over-cap preview=%q err=%v", preview, err)
	}
	if err := ledger.IngestProviderRefund(ctx, over, audit); !errors.Is(err, ErrRefundExceedsPayment) {
		t.Fatalf("over-refund error=%v", err)
	}
	var overAnomalies int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies
		WHERE proposed_order_id=? AND provider='ton' AND event_kind='refunded'
		  AND external_id='ton-refund-2' AND reason='refund_exceeds_payment'`, orderID).Scan(&overAnomalies); err != nil {
		t.Fatal(err)
	}
	if overAnomalies != 1 {
		t.Fatalf("over-refund anomalies=%d", overAnomalies)
	}

	// Defense in depth: a capture row whose positive payer disagrees with the
	// refund payer still quarantines the preview, exactly like the stripe
	// rail. Seeded directly to pin the corroboration rule.
	if _, err := db.Conn().Exec(`INSERT INTO payment_attempts
		(order_id, provider, external_id, payer_id, amount_minor, currency, scale, status, occurred_at)
		VALUES (?, 'ton', 'ton-hostile-pay', 43, 1500000000, 'TON', 9, 'succeeded', ?)`,
		orderID, providerIngressTime); err != nil {
		t.Fatal(err)
	}
	hostile := Refund{
		OrderID: orderID, Provider: PaymentMethodTON, ExternalID: "ton-hostile-refund",
		PaymentExternalID: "ton-hostile-pay", PayerID: 42,
		AmountMinor: 1_500_000_000, Currency: "TON", Scale: 9, OccurredAt: providerIngressTime.Add(3 * time.Minute),
	}
	if preview, err := ledger.PreviewProviderRefundIngress(ctx, hostile); err != nil || preview != PaymentIngressQuarantine {
		t.Fatalf("mismatched positive payer preview=%q err=%v", preview, err)
	}
}

func TestProviderRefundIngressPreviewsAndIngestsNowpayments(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "nowpayments-refund-ingress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, orderID, _ := seedNowpaymentsLedgerOrder(t, db)
	ctx := context.Background()
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, PaymentFact{
		Provider: PaymentMethodNowpayments, ExternalID: "np-pay-1",
		AmountMinor: 199900, Currency: "USD", Scale: 2, OccurredAt: providerIngressTime,
	}); err != nil {
		t.Fatal(err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	// The operator supplies the order's Telegram user as the refund payer
	// because the capture carries no payer id (NOWPayments has no Telegram
	// payer identity), mirroring the stripe rail.
	refund := Refund{
		OrderID: orderID, Provider: PaymentMethodNowpayments, ExternalID: "np-refund-1",
		PaymentExternalID: "np-pay-1", PayerID: 42,
		AmountMinor: 199900, Currency: "USD", Scale: 2, OccurredAt: providerIngressTime.Add(time.Minute),
	}
	preview, err := ledger.PreviewProviderRefundIngress(ctx, refund)
	if err != nil || preview != PaymentIngressApply {
		t.Fatalf("initial preview=%q err=%v", preview, err)
	}
	audit := PaymentIngressAudit{Actor: "operator:test", Reason: "provider-only refund"}
	if err := ledger.IngestProviderRefund(ctx, refund, audit); err != nil {
		t.Fatalf("ingest error=%v", err)
	}

	var refunds, refundEvents, audits int
	var state string
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM refunds
		WHERE order_id=? AND provider='nowpayments' AND external_id='np-refund-1'
		  AND payment_external_id='np-pay-1' AND payer_id=42 AND status='succeeded'`, orderID).Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events
		WHERE order_id=? AND provider='nowpayments' AND event_kind='refunded'
		  AND external_id='np-refund-1' AND disposition='settled'`, orderID).Scan(&refundEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_ingress_audits a
		JOIN refunds r ON a.target_kind='refund' AND a.target_id=r.id
		WHERE a.order_id=? AND a.provider='nowpayments' AND a.event_kind='refunded'
		  AND a.actor='operator:test' AND a.reason='provider-only refund'
		  AND r.external_id='np-refund-1'`, orderID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if refunds != 1 || refundEvents != 1 || audits != 1 || state != PaymentStateRefunded {
		t.Fatalf("refunds=%d refund_events=%d audits=%d state=%s", refunds, refundEvents, audits, state)
	}

	preview, err = ledger.PreviewProviderRefundIngress(ctx, refund)
	if err != nil || preview != PaymentIngressReplay {
		t.Fatalf("replay preview=%q err=%v", preview, err)
	}
	if err := ledger.IngestProviderRefund(ctx, refund, audit); err != nil {
		t.Fatalf("exact replay error=%v", err)
	}
}

func TestProviderCaptureIngressAcceptsPayerlessTONFact(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "ton-capture-ingress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, orderID, productID := seedTONLedgerOrder(t, db)
	ctx := context.Background()
	fact := PaymentFact{
		Provider: PaymentMethodTON, ExternalID: "ton-provider-only-capture",
		PayerID: 0, AmountMinor: 1_500_000_000, Currency: "TON", Scale: 9, OccurredAt: providerIngressTime,
	}
	audit := PaymentIngressAudit{Actor: "operator:test", Reason: "provider-only capture"}

	preview, err := store.PreviewProviderCaptureIngress(ctx, orderID, fact)
	if err != nil || preview != PaymentIngressQuarantine {
		t.Fatalf("initial preview=%q err=%v", preview, err)
	}
	if err := store.IngestProviderCapture(ctx, orderID, fact, audit); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("ingest error=%v", err)
	}
	assertTONCaptureQuarantine(t, db, orderID, productID)

	preview, err = store.PreviewProviderCaptureIngress(ctx, orderID, fact)
	if err != nil || preview != PaymentIngressReplay {
		t.Fatalf("replay preview=%q err=%v", preview, err)
	}
	if err := store.IngestProviderCapture(ctx, orderID, fact, audit); err != nil {
		t.Fatalf("exact replay error=%v", err)
	}
	assertTONCaptureQuarantine(t, db, orderID, productID)

	// Defense in depth: a positive payer id that disagrees with the order
	// user still rejects, and nothing is written for the rejected identity.
	wrongPayer := fact
	wrongPayer.ExternalID = "ton-wrong-payer-capture"
	wrongPayer.PayerID = 43
	if _, err := store.PreviewProviderCaptureIngress(ctx, orderID, wrongPayer); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("wrong payer preview error=%v", err)
	}
	if err := store.IngestProviderCapture(ctx, orderID, wrongPayer, audit); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("wrong payer ingest error=%v", err)
	}
	var wrongAttempts int
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts
		WHERE external_id='ton-wrong-payer-capture'`).Scan(&wrongAttempts)
	if wrongAttempts != 0 {
		t.Fatalf("wrong payer attempts=%d", wrongAttempts)
	}

	// A negative payer id rejects for ton too: zero is the only payerless
	// identity the payerless rails may carry.
	negativePayer := fact
	negativePayer.ExternalID = "ton-negative-payer-capture"
	negativePayer.PayerID = -1
	if _, err := store.PreviewProviderCaptureIngress(ctx, orderID, negativePayer); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("negative payer preview error=%v", err)
	}
	if err := store.IngestProviderCapture(ctx, orderID, negativePayer, audit); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("negative payer ingest error=%v", err)
	}
}

func TestProviderCaptureIngressAcceptsPayerlessNowpaymentsFact(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "nowpayments-capture-ingress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, orderID, productID := seedNowpaymentsLedgerOrder(t, db)
	ctx := context.Background()
	fact := PaymentFact{
		Provider: PaymentMethodNowpayments, ExternalID: "np-provider-only-capture",
		PayerID: 0, AmountMinor: 199900, Currency: "USD", Scale: 2, OccurredAt: providerIngressTime,
	}
	audit := PaymentIngressAudit{Actor: "operator:test", Reason: "provider-only capture"}

	preview, err := store.PreviewProviderCaptureIngress(ctx, orderID, fact)
	if err != nil || preview != PaymentIngressQuarantine {
		t.Fatalf("initial preview=%q err=%v", preview, err)
	}
	if err := store.IngestProviderCapture(ctx, orderID, fact, audit); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("ingest error=%v", err)
	}
	assertNowpaymentsCaptureQuarantine(t, db, orderID, productID)

	preview, err = store.PreviewProviderCaptureIngress(ctx, orderID, fact)
	if err != nil || preview != PaymentIngressReplay {
		t.Fatalf("replay preview=%q err=%v", preview, err)
	}
	if err := store.IngestProviderCapture(ctx, orderID, fact, audit); err != nil {
		t.Fatalf("exact replay error=%v", err)
	}
	assertNowpaymentsCaptureQuarantine(t, db, orderID, productID)

	// A negative payer id rejects for nowpayments too, mirroring the stripe
	// and ton rails.
	negativePayer := fact
	negativePayer.ExternalID = "np-negative-payer-capture"
	negativePayer.PayerID = -1
	if _, err := store.PreviewProviderCaptureIngress(ctx, orderID, negativePayer); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("negative payer preview error=%v", err)
	}
	if err := store.IngestProviderCapture(ctx, orderID, negativePayer, audit); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("negative payer ingest error=%v", err)
	}
}

func assertTONCaptureQuarantine(t *testing.T, db *DB, orderID, productID int64) {
	t.Helper()
	var (
		status, orderState, paymentState, fulfillmentState, paymentID string
		stock, attempts, events, audits                               int
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
		WHERE order_id = ? AND provider = 'ton' AND external_id = 'ton-provider-only-capture'
		  AND payer_id = 0 AND status = 'needs_review'`, orderID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events
		WHERE order_id = ? AND provider = 'ton' AND event_kind = 'captured'
		  AND external_id = 'ton-provider-only-capture' AND disposition = 'needs_review'`, orderID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_ingress_audits a
		JOIN payment_events e ON a.target_kind='payment_event' AND a.target_id=e.id
		WHERE a.order_id=? AND a.provider='ton' AND a.event_kind='captured'
		  AND a.actor='operator:test' AND a.reason='provider-only capture'
		  AND e.external_id='ton-provider-only-capture'`, orderID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if status != OrderStatusPending || orderState != OrderStatePlaced || paymentState != PaymentStateNeedsReview ||
		fulfillmentState != FulfillmentStateUnfulfilled || paymentID != "" || stock != 100 ||
		attempts != 1 || events != 1 || audits != 1 {
		t.Fatalf("status=%s order=%s payment=%s fulfillment=%s payment_id=%q stock=%d attempts=%d events=%d audits=%d",
			status, orderState, paymentState, fulfillmentState, paymentID, stock, attempts, events, audits)
	}
}

func assertNowpaymentsCaptureQuarantine(t *testing.T, db *DB, orderID, productID int64) {
	t.Helper()
	var (
		status, orderState, paymentState, fulfillmentState, paymentID string
		stock, attempts, events, audits                               int
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
		WHERE order_id = ? AND provider = 'nowpayments' AND external_id = 'np-provider-only-capture'
		  AND payer_id = 0 AND status = 'needs_review'`, orderID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events
		WHERE order_id = ? AND provider = 'nowpayments' AND event_kind = 'captured'
		  AND external_id = 'np-provider-only-capture' AND disposition = 'needs_review'`, orderID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_ingress_audits a
		JOIN payment_events e ON a.target_kind='payment_event' AND a.target_id=e.id
		WHERE a.order_id=? AND a.provider='nowpayments' AND a.event_kind='captured'
		  AND a.actor='operator:test' AND a.reason='provider-only capture'
		  AND e.external_id='np-provider-only-capture'`, orderID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if status != OrderStatusPending || orderState != OrderStatePlaced || paymentState != PaymentStateNeedsReview ||
		fulfillmentState != FulfillmentStateUnfulfilled || paymentID != "" || stock != 100 ||
		attempts != 1 || events != 1 || audits != 1 {
		t.Fatalf("status=%s order=%s payment=%s fulfillment=%s payment_id=%q stock=%d attempts=%d events=%d audits=%d",
			status, orderState, paymentState, fulfillmentState, paymentID, stock, attempts, events, audits)
	}
}

func TestPaymentReviewListsPreviewsAndResolvesTONTargets(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "ton-resolve.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	store, orderID, _ := seedTONLedgerOrder(t, db)
	if err := store.UpdateOrderStatus(ctx, orderID, OrderStatusPending, OrderStatusPaid, PaymentMethodTON, "ton-capture-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUnexpectedPayment(ctx, orderID, PaymentMethodTON, "ton-capture-b", "second_transfer"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("unexpected capture error=%v", err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	if err := ledger.RecordRefund(ctx, Refund{
		OrderID: orderID, Provider: PaymentMethodTON, ExternalID: "ton-refund-b",
		PaymentExternalID: "ton-capture-b", AmountMinor: 1_500_000_000, Currency: "TON", Scale: 9,
	}); err != nil {
		t.Fatalf("ton refund error=%v", err)
	}

	cases, err := ledger.ListPaymentReviews(ctx, PaymentMethodTON)
	if err != nil || len(cases) != 1 {
		t.Fatalf("cases=%+v err=%v", cases, err)
	}
	resolution := PaymentReviewResolution{
		OrderID: orderID, Provider: PaymentMethodTON, Actor: "operator:test",
		Reason: "duplicate transfer fully refunded", ResultingPaymentState: PaymentStateSettled,
	}
	for _, target := range cases[0].Targets {
		switch target.Kind {
		case PaymentReviewTargetEvent:
			resolution.EventIDs = append(resolution.EventIDs, target.ID)
		case PaymentReviewTargetAnomaly:
			resolution.AnomalyIDs = append(resolution.AnomalyIDs, target.ID)
		}
	}
	if len(resolution.EventIDs) != 2 || len(resolution.AnomalyIDs) != 0 {
		t.Fatalf("event targets=%v anomaly targets=%v", resolution.EventIDs, resolution.AnomalyIDs)
	}
	if _, err := ledger.PreviewPaymentReviewResolution(ctx, resolution); err != nil {
		t.Fatalf("preview error=%v", err)
	}
	if err := ledger.ResolvePaymentReview(ctx, resolution); err != nil {
		t.Fatalf("resolve error=%v", err)
	}
	if err := ledger.ResolvePaymentReview(ctx, resolution); err != nil {
		t.Fatalf("resolution replay error=%v", err)
	}

	var state string
	var resolutions int
	_ = db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_resolutions WHERE order_id=?`, orderID).Scan(&resolutions)
	if state != PaymentStateSettled || resolutions != 2 {
		t.Fatalf("state=%s resolutions=%d", state, resolutions)
	}
	cases, err = ledger.ListPaymentReviews(ctx, PaymentMethodTON)
	if err != nil || len(cases) != 0 {
		t.Fatalf("final cases=%+v err=%v", cases, err)
	}
}

func TestPaymentReviewListsPreviewsAndResolvesNowpaymentsTargets(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "nowpayments-resolve.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	store, orderID, _ := seedNowpaymentsLedgerOrder(t, db)
	if err := store.UpdateOrderStatus(ctx, orderID, OrderStatusPending, OrderStatusPaid, PaymentMethodNowpayments, "np-capture-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUnexpectedPayment(ctx, orderID, PaymentMethodNowpayments, "np-capture-b", "second_charge"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("unexpected capture error=%v", err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	if err := ledger.RecordRefund(ctx, Refund{
		OrderID: orderID, Provider: PaymentMethodNowpayments, ExternalID: "np-refund-b",
		PaymentExternalID: "np-capture-b", AmountMinor: 199900, Currency: "USD", Scale: 2,
	}); err != nil {
		t.Fatalf("nowpayments refund error=%v", err)
	}

	cases, err := ledger.ListPaymentReviews(ctx, PaymentMethodNowpayments)
	if err != nil || len(cases) != 1 {
		t.Fatalf("cases=%+v err=%v", cases, err)
	}
	resolution := PaymentReviewResolution{
		OrderID: orderID, Provider: PaymentMethodNowpayments, Actor: "operator:test",
		Reason: "duplicate charge fully refunded", ResultingPaymentState: PaymentStateSettled,
	}
	for _, target := range cases[0].Targets {
		switch target.Kind {
		case PaymentReviewTargetEvent:
			resolution.EventIDs = append(resolution.EventIDs, target.ID)
		case PaymentReviewTargetAnomaly:
			resolution.AnomalyIDs = append(resolution.AnomalyIDs, target.ID)
		}
	}
	if len(resolution.EventIDs) != 2 || len(resolution.AnomalyIDs) != 0 {
		t.Fatalf("event targets=%v anomaly targets=%v", resolution.EventIDs, resolution.AnomalyIDs)
	}
	if _, err := ledger.PreviewPaymentReviewResolution(ctx, resolution); err != nil {
		t.Fatalf("preview error=%v", err)
	}
	if err := ledger.ResolvePaymentReview(ctx, resolution); err != nil {
		t.Fatalf("resolve error=%v", err)
	}

	var state string
	_ = db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state)
	if state != PaymentStateSettled {
		t.Fatalf("state=%s", state)
	}
	cases, err = ledger.ListPaymentReviews(ctx, PaymentMethodNowpayments)
	if err != nil || len(cases) != 0 {
		t.Fatalf("final cases=%+v err=%v", cases, err)
	}
}

// NOTE: the former TestBalanceFailsClosedAtAppLevel pinned the dormant
// DB-only balance forward-pin. The balance feature now lands, so the
// app-level ACCEPTANCE discipline for the balance rail lives in
// balance_acceptance_test.go instead.
