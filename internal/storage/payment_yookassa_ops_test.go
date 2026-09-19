package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedYooKassaLedgerOrder mirrors seedLedgerOrder for the RUB rail: the order
// snapshots TotalRUB 1849.08 (184908 kopecks, scale 2) with payment method
// yookassa, so orderMoney derives the exact provider money tuple.
func seedYooKassaLedgerOrder(t *testing.T, db *DB) (*SQLOrderStore, int64, int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Conn().ExecContext(ctx, `INSERT INTO categories (name) VALUES ('yookassa-ops')`); err != nil {
		t.Fatal(err)
	}
	res, err := db.Conn().ExecContext(ctx,
		`INSERT INTO products (category_id, name, price_usd, price_stars, stock, is_active)
		 VALUES (1, 'Gadget', 23.00, 0, 100, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	productID, _ := res.LastInsertId()
	store := NewSQLOrderStore(db)
	orderID, err := store.CreateOrder(ctx, &Order{
		UserID: 42, TotalUSD: 23.00, TotalRUB: 1849.08,
		PaymentMethod: PaymentMethodYooKassa, Status: OrderStatusPending,
	}, []OrderItem{{ProductID: productID, Quantity: 1, PriceUSD: 23.00}})
	if err != nil {
		t.Fatal(err)
	}
	return store, orderID, productID
}

func TestYooKassaRefundOfNeedsReviewCaptureIsDurable(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "yookassa-refund.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, orderID, _ := seedYooKassaLedgerOrder(t, db)
	ctx := context.Background()
	if err := store.UpdateOrderStatus(ctx, orderID, OrderStatusPending, OrderStatusPaid, PaymentMethodYooKassa, "yoo-capture"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUnexpectedPayment(ctx, orderID, PaymentMethodYooKassa, "yoo-second-capture", "second_charge"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("unexpected capture error=%v", err)
	}

	ledger := NewSQLPaymentLedgerStore(db)
	refund := Refund{
		OrderID: orderID, Provider: PaymentMethodYooKassa, ExternalID: "yoo-second-refund",
		PaymentExternalID: "yoo-second-capture", AmountMinor: 184908, Currency: "RUB", Scale: 2,
	}
	if err := ledger.RecordRefund(ctx, refund); err != nil {
		t.Fatalf("yookassa refund error=%v", err)
	}
	if err := ledger.RecordRefund(ctx, refund); err != nil {
		t.Fatalf("exact replay error=%v", err)
	}

	var refunds, events int
	var status, disposition, state string
	if err := db.Conn().QueryRow(`SELECT COUNT(*), MIN(status) FROM refunds
		WHERE provider='yookassa' AND external_id='yoo-second-refund'`).Scan(&refunds, &status); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*), MIN(disposition) FROM payment_events
		WHERE provider='yookassa' AND event_kind='refunded' AND external_id='yoo-second-refund'`).Scan(&events, &disposition); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if refunds != 1 || events != 1 || status != "succeeded" || disposition != PaymentDispositionNeedsReview || state != PaymentStateNeedsReview {
		t.Fatalf("refunds=%d events=%d status=%s disposition=%s state=%s", refunds, events, status, disposition, state)
	}

	conflict := refund
	conflict.AmountMinor = 184907
	if err := ledger.RecordRefund(ctx, conflict); !errors.Is(err, ErrPaymentIdentityConflict) {
		t.Fatalf("conflicting replay error=%v", err)
	}
	var anomalies int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies
		WHERE proposed_order_id=? AND event_kind='refunded' AND external_id='yoo-second-refund'
		  AND related_external_id='yoo-second-capture' AND reason='refund_identity_conflict'`, orderID).Scan(&anomalies); err != nil {
		t.Fatal(err)
	}
	if anomalies != 1 {
		t.Fatalf("conflicting replay anomalies=%d", anomalies)
	}

	// An invalid yookassa refund fact is quarantined under its own provider
	// identity instead of being relabeled through the order's payment method.
	invalid := refund
	invalid.ExternalID = "yoo-invalid-refund"
	invalid.AmountMinor = 0
	if err := ledger.RecordRefund(ctx, invalid); !errors.Is(err, ErrInvalidMoney) {
		t.Fatalf("invalid refund error=%v", err)
	}
	var anomalyProvider, rawPayload string
	if err := db.Conn().QueryRow(`SELECT provider, raw_payload FROM payment_anomalies
		WHERE external_id='yoo-invalid-refund'`).Scan(&anomalyProvider, &rawPayload); err != nil {
		t.Fatal(err)
	}
	if anomalyProvider != PaymentMethodYooKassa || strings.Contains(rawPayload, "invalid_provider") {
		t.Fatalf("invalid refund anomaly provider=%s raw_payload=%q", anomalyProvider, rawPayload)
	}
}

func TestProviderRefundIngressPreviewsAndIngestsYooKassa(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "yookassa-refund-ingress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, orderID, _ := seedYooKassaLedgerOrder(t, db)
	ctx := context.Background()
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, PaymentFact{
		Provider: PaymentMethodYooKassa, ExternalID: "yoo-pay-1",
		AmountMinor: 184908, Currency: "RUB", Scale: 2, OccurredAt: providerIngressTime,
	}); err != nil {
		t.Fatal(err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	// The operator supplies the order's Telegram user as the refund payer
	// because the capture carries no payer id (YooKassa has no Telegram payer
	// identity). Within the capture cap and the exact parent money tuple, the
	// preview must agree with what ingest would do: apply.
	refund := Refund{
		OrderID: orderID, Provider: PaymentMethodYooKassa, ExternalID: "yoo-refund-1",
		PaymentExternalID: "yoo-pay-1", PayerID: 42,
		AmountMinor: 184908, Currency: "RUB", Scale: 2, OccurredAt: providerIngressTime.Add(time.Minute),
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
		WHERE order_id=? AND provider='yookassa' AND external_id='yoo-refund-1'
		  AND payment_external_id='yoo-pay-1' AND payer_id=42 AND status='succeeded'`, orderID).Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events
		WHERE order_id=? AND provider='yookassa' AND event_kind='refunded'
		  AND external_id='yoo-refund-1' AND disposition='settled'`, orderID).Scan(&refundEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_ingress_audits a
		JOIN refunds r ON a.target_kind='refund' AND a.target_id=r.id
		WHERE a.order_id=? AND a.provider='yookassa' AND a.event_kind='refunded'
		  AND a.actor='operator:test' AND a.reason='provider-only refund'
		  AND r.external_id='yoo-refund-1'`, orderID).Scan(&audits); err != nil {
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
		OrderID: orderID, Provider: PaymentMethodYooKassa, ExternalID: "yoo-refund-2",
		PaymentExternalID: "yoo-pay-1", PayerID: 42,
		AmountMinor: 1, Currency: "RUB", Scale: 2, OccurredAt: providerIngressTime.Add(2 * time.Minute),
	}
	if preview, err := ledger.PreviewProviderRefundIngress(ctx, over); err != nil || preview != PaymentIngressQuarantine {
		t.Fatalf("over-cap preview=%q err=%v", preview, err)
	}
	if err := ledger.IngestProviderRefund(ctx, over, audit); !errors.Is(err, ErrRefundExceedsPayment) {
		t.Fatalf("over-refund error=%v", err)
	}
	var overAnomalies int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies
		WHERE proposed_order_id=? AND provider='yookassa' AND event_kind='refunded'
		  AND external_id='yoo-refund-2' AND reason='refund_exceeds_payment'`, orderID).Scan(&overAnomalies); err != nil {
		t.Fatal(err)
	}
	if overAnomalies != 1 {
		t.Fatalf("over-refund anomalies=%d", overAnomalies)
	}

	// Defense in depth: a capture row whose positive payer disagrees with the
	// refund payer still quarantines the preview. No legitimate settlement
	// path writes such a row (settlement facts validate the payer against the
	// order user), so it is seeded directly to pin the corroboration rule.
	if _, err := db.Conn().Exec(`INSERT INTO payment_attempts
		(order_id, provider, external_id, payer_id, amount_minor, currency, scale, status, occurred_at)
		VALUES (?, 'yookassa', 'yoo-hostile-pay', 43, 184908, 'RUB', 2, 'succeeded', ?)`,
		orderID, providerIngressTime); err != nil {
		t.Fatal(err)
	}
	hostile := Refund{
		OrderID: orderID, Provider: PaymentMethodYooKassa, ExternalID: "yoo-hostile-refund",
		PaymentExternalID: "yoo-hostile-pay", PayerID: 42,
		AmountMinor: 184908, Currency: "RUB", Scale: 2, OccurredAt: providerIngressTime.Add(3 * time.Minute),
	}
	if preview, err := ledger.PreviewProviderRefundIngress(ctx, hostile); err != nil || preview != PaymentIngressQuarantine {
		t.Fatalf("mismatched positive payer preview=%q err=%v", preview, err)
	}
}

func TestProviderCaptureIngressAcceptsPayerlessYooKassaFact(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "yookassa-capture-ingress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, orderID, productID := seedYooKassaLedgerOrder(t, db)
	ctx := context.Background()
	fact := PaymentFact{
		Provider: PaymentMethodYooKassa, ExternalID: "yoo-provider-only-capture",
		PayerID: 0, AmountMinor: 184908, Currency: "RUB", Scale: 2, OccurredAt: providerIngressTime,
	}
	audit := PaymentIngressAudit{Actor: "operator:test", Reason: "provider-only capture"}

	preview, err := store.PreviewProviderCaptureIngress(ctx, orderID, fact)
	if err != nil || preview != PaymentIngressQuarantine {
		t.Fatalf("initial preview=%q err=%v", preview, err)
	}
	if err := store.IngestProviderCapture(ctx, orderID, fact, audit); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("ingest error=%v", err)
	}
	assertYooKassaCaptureQuarantine(t, db, orderID, productID)

	preview, err = store.PreviewProviderCaptureIngress(ctx, orderID, fact)
	if err != nil || preview != PaymentIngressReplay {
		t.Fatalf("replay preview=%q err=%v", preview, err)
	}
	if err := store.IngestProviderCapture(ctx, orderID, fact, audit); err != nil {
		t.Fatalf("exact replay error=%v", err)
	}
	assertYooKassaCaptureQuarantine(t, db, orderID, productID)

	// Defense in depth: a positive payer id that disagrees with the order user
	// still rejects, and nothing is written for the rejected identity.
	wrongPayer := fact
	wrongPayer.ExternalID = "yoo-wrong-payer-capture"
	wrongPayer.PayerID = 43
	if _, err := store.PreviewProviderCaptureIngress(ctx, orderID, wrongPayer); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("wrong payer preview error=%v", err)
	}
	if err := store.IngestProviderCapture(ctx, orderID, wrongPayer, audit); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("wrong payer ingest error=%v", err)
	}
	var wrongAttempts int
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts
		WHERE external_id='yoo-wrong-payer-capture'`).Scan(&wrongAttempts)
	if wrongAttempts != 0 {
		t.Fatalf("wrong payer attempts=%d", wrongAttempts)
	}

	// A missing provider timestamp is never acceptable, payer or not.
	noTime := fact
	noTime.ExternalID = "yoo-no-time-capture"
	noTime.OccurredAt = time.Time{}
	if _, err := store.PreviewProviderCaptureIngress(ctx, orderID, noTime); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("zero timestamp preview error=%v", err)
	}
	if err := store.IngestProviderCapture(ctx, orderID, noTime, audit); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("zero timestamp ingest error=%v", err)
	}
}

func assertYooKassaCaptureQuarantine(t *testing.T, db *DB, orderID, productID int64) {
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
		WHERE order_id = ? AND provider = 'yookassa' AND external_id = 'yoo-provider-only-capture'
		  AND payer_id = 0 AND status = 'needs_review'`, orderID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events
		WHERE order_id = ? AND provider = 'yookassa' AND event_kind = 'captured'
		  AND external_id = 'yoo-provider-only-capture' AND disposition = 'needs_review'`, orderID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_ingress_audits a
		JOIN payment_events e ON a.target_kind='payment_event' AND a.target_id=e.id
		WHERE a.order_id=? AND a.provider='yookassa' AND a.event_kind='captured'
		  AND a.actor='operator:test' AND a.reason='provider-only capture'
		  AND e.external_id='yoo-provider-only-capture'`, orderID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if status != OrderStatusPending || orderState != OrderStatePlaced || paymentState != PaymentStateNeedsReview ||
		fulfillmentState != FulfillmentStateUnfulfilled || paymentID != "" || stock != 100 ||
		attempts != 1 || events != 1 || audits != 1 {
		t.Fatalf("status=%s order=%s payment=%s fulfillment=%s payment_id=%q stock=%d attempts=%d events=%d audits=%d",
			status, orderState, paymentState, fulfillmentState, paymentID, stock, attempts, events, audits)
	}
}

func TestProviderCaptureIngressKeepsStarsAndCryptoPayerRequirements(t *testing.T) {
	ctx := context.Background()
	// Stars: a payerless capture fact still rejects.
	starsDB, err := New(filepath.Join(t.TempDir(), "stars-payer-gate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer starsDB.Close()
	starsStore, starsOrderID, _ := seedLedgerOrder(t, starsDB, 100)
	starsFact := PaymentFact{
		Provider: PaymentMethodStars, ExternalID: "stars-payerless",
		PayerID: 0, AmountMinor: 100, Currency: "XTR", Scale: 0, OccurredAt: providerIngressTime,
	}
	if _, err := starsStore.PreviewProviderCaptureIngress(ctx, starsOrderID, starsFact); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("stars payerless preview error=%v", err)
	}
	if err := starsStore.IngestProviderCapture(ctx, starsOrderID, starsFact, PaymentIngressAudit{Actor: "operator:test", Reason: "provider-only capture"}); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("stars payerless ingest error=%v", err)
	}
	// Stars: a correct payer still quarantines as review evidence.
	starsFact.PayerID = 42
	if preview, err := starsStore.PreviewProviderCaptureIngress(ctx, starsOrderID, starsFact); err != nil || preview != PaymentIngressQuarantine {
		t.Fatalf("stars payered preview=%q err=%v", preview, err)
	}
	if err := starsStore.IngestProviderCapture(ctx, starsOrderID, starsFact, PaymentIngressAudit{Actor: "operator:test", Reason: "provider-only capture"}); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("stars payered ingest error=%v", err)
	}

	// Crypto: a payerless capture fact still rejects.
	cryptoDB, err := New(filepath.Join(t.TempDir(), "crypto-payer-gate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cryptoDB.Close()
	cryptoStore, cryptoOrderID, _ := seedLedgerOrder(t, cryptoDB, 100)
	cryptoFact := PaymentFact{
		Provider: PaymentMethodCrypto, ExternalID: "crypto-payerless",
		PayerID: 0, AmountMinor: 1250, Currency: "USD", Scale: 2, OccurredAt: providerIngressTime,
	}
	if _, err := cryptoStore.PreviewProviderCaptureIngress(ctx, cryptoOrderID, cryptoFact); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("crypto payerless preview error=%v", err)
	}
	if err := cryptoStore.IngestProviderCapture(ctx, cryptoOrderID, cryptoFact, PaymentIngressAudit{Actor: "operator:test", Reason: "provider-only capture"}); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("crypto payerless ingest error=%v", err)
	}
}

func TestPaymentReviewListsPreviewsAndResolvesYooKassaTargets(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "yookassa-resolve.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	store, orderID, _ := seedYooKassaLedgerOrder(t, db)
	if err := store.UpdateOrderStatus(ctx, orderID, OrderStatusPending, OrderStatusPaid, PaymentMethodYooKassa, "yoo-capture-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUnexpectedPayment(ctx, orderID, PaymentMethodYooKassa, "yoo-capture-b", "second_charge"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("unexpected capture error=%v", err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	if err := ledger.RecordRefund(ctx, Refund{
		OrderID: orderID, Provider: PaymentMethodYooKassa, ExternalID: "yoo-refund-b",
		PaymentExternalID: "yoo-capture-b", AmountMinor: 184908, Currency: "RUB", Scale: 2,
	}); err != nil {
		t.Fatalf("yookassa refund error=%v", err)
	}

	cases, err := ledger.ListPaymentReviews(ctx, PaymentMethodYooKassa)
	if err != nil || len(cases) != 1 {
		t.Fatalf("cases=%+v err=%v", cases, err)
	}
	resolution := PaymentReviewResolution{
		OrderID: orderID, Provider: PaymentMethodYooKassa, Actor: "operator:test",
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
	cases, err = ledger.ListPaymentReviews(ctx, PaymentMethodYooKassa)
	if err != nil || len(cases) != 0 {
		t.Fatalf("final cases=%+v err=%v", cases, err)
	}
}
