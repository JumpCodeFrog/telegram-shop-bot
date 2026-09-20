package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedStripeLedgerOrder mirrors seedYooKassaLedgerOrder for the USD card
// rail: the order snapshots TotalUSD 1999.00 (199900 cents, scale 2) with
// payment method stripe, so orderMoney derives the exact provider money
// tuple.
func seedStripeLedgerOrder(t *testing.T, db *DB) (*SQLOrderStore, int64, int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Conn().ExecContext(ctx, `INSERT INTO categories (name) VALUES ('stripe-ops')`); err != nil {
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
		PaymentMethod: PaymentMethodStripe, Status: OrderStatusPending,
	}, []OrderItem{{ProductID: productID, Quantity: 1, PriceUSD: 1999.00}})
	if err != nil {
		t.Fatal(err)
	}
	return store, orderID, productID
}

func TestStripeRefundOfNeedsReviewCaptureIsDurable(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "stripe-refund.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, orderID, _ := seedStripeLedgerOrder(t, db)
	ctx := context.Background()
	if err := store.UpdateOrderStatus(ctx, orderID, OrderStatusPending, OrderStatusPaid, PaymentMethodStripe, "str-capture"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUnexpectedPayment(ctx, orderID, PaymentMethodStripe, "str-second-capture", "second_charge"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("unexpected capture error=%v", err)
	}

	ledger := NewSQLPaymentLedgerStore(db)
	refund := Refund{
		OrderID: orderID, Provider: PaymentMethodStripe, ExternalID: "str-second-refund",
		PaymentExternalID: "str-second-capture", AmountMinor: 199900, Currency: "USD", Scale: 2,
	}
	if err := ledger.RecordRefund(ctx, refund); err != nil {
		t.Fatalf("stripe refund error=%v", err)
	}
	if err := ledger.RecordRefund(ctx, refund); err != nil {
		t.Fatalf("exact replay error=%v", err)
	}

	var refunds, events int
	var status, disposition, state string
	if err := db.Conn().QueryRow(`SELECT COUNT(*), MIN(status) FROM refunds
		WHERE provider='stripe' AND external_id='str-second-refund'`).Scan(&refunds, &status); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*), MIN(disposition) FROM payment_events
		WHERE provider='stripe' AND event_kind='refunded' AND external_id='str-second-refund'`).Scan(&events, &disposition); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if refunds != 1 || events != 1 || status != "succeeded" || disposition != PaymentDispositionNeedsReview || state != PaymentStateNeedsReview {
		t.Fatalf("refunds=%d events=%d status=%s disposition=%s state=%s", refunds, events, status, disposition, state)
	}

	conflict := refund
	conflict.AmountMinor = 199899
	if err := ledger.RecordRefund(ctx, conflict); !errors.Is(err, ErrPaymentIdentityConflict) {
		t.Fatalf("conflicting replay error=%v", err)
	}
	var anomalies int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies
		WHERE proposed_order_id=? AND event_kind='refunded' AND external_id='str-second-refund'
		  AND related_external_id='str-second-capture' AND reason='refund_identity_conflict'`, orderID).Scan(&anomalies); err != nil {
		t.Fatal(err)
	}
	if anomalies != 1 {
		t.Fatalf("conflicting replay anomalies=%d", anomalies)
	}

	// An invalid stripe refund fact is quarantined under its own provider
	// identity instead of being relabeled through the order's payment method.
	invalid := refund
	invalid.ExternalID = "str-invalid-refund"
	invalid.AmountMinor = 0
	if err := ledger.RecordRefund(ctx, invalid); !errors.Is(err, ErrInvalidMoney) {
		t.Fatalf("invalid refund error=%v", err)
	}
	var anomalyProvider, rawPayload string
	if err := db.Conn().QueryRow(`SELECT provider, raw_payload FROM payment_anomalies
		WHERE external_id='str-invalid-refund'`).Scan(&anomalyProvider, &rawPayload); err != nil {
		t.Fatal(err)
	}
	if anomalyProvider != PaymentMethodStripe || strings.Contains(rawPayload, "invalid_provider") {
		t.Fatalf("invalid refund anomaly provider=%s raw_payload=%q", anomalyProvider, rawPayload)
	}
}

func TestProviderRefundIngressPreviewsAndIngestsStripe(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "stripe-refund-ingress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, orderID, _ := seedStripeLedgerOrder(t, db)
	ctx := context.Background()
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, PaymentFact{
		Provider: PaymentMethodStripe, ExternalID: "str-pay-1",
		AmountMinor: 199900, Currency: "USD", Scale: 2, OccurredAt: providerIngressTime,
	}); err != nil {
		t.Fatal(err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	// The operator supplies the order's Telegram user as the refund payer
	// because the capture carries no payer id (Stripe has no Telegram payer
	// identity). Within the capture cap and the exact parent money tuple, the
	// preview must agree with what ingest would do: apply.
	refund := Refund{
		OrderID: orderID, Provider: PaymentMethodStripe, ExternalID: "str-refund-1",
		PaymentExternalID: "str-pay-1", PayerID: 42,
		AmountMinor: 199900, Currency: "USD", Scale: 2, OccurredAt: providerIngressTime.Add(time.Minute),
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
		WHERE order_id=? AND provider='stripe' AND external_id='str-refund-1'
		  AND payment_external_id='str-pay-1' AND payer_id=42 AND status='succeeded'`, orderID).Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events
		WHERE order_id=? AND provider='stripe' AND event_kind='refunded'
		  AND external_id='str-refund-1' AND disposition='settled'`, orderID).Scan(&refundEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_ingress_audits a
		JOIN refunds r ON a.target_kind='refund' AND a.target_id=r.id
		WHERE a.order_id=? AND a.provider='stripe' AND a.event_kind='refunded'
		  AND a.actor='operator:test' AND a.reason='provider-only refund'
		  AND r.external_id='str-refund-1'`, orderID).Scan(&audits); err != nil {
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
		OrderID: orderID, Provider: PaymentMethodStripe, ExternalID: "str-refund-2",
		PaymentExternalID: "str-pay-1", PayerID: 42,
		AmountMinor: 1, Currency: "USD", Scale: 2, OccurredAt: providerIngressTime.Add(2 * time.Minute),
	}
	if preview, err := ledger.PreviewProviderRefundIngress(ctx, over); err != nil || preview != PaymentIngressQuarantine {
		t.Fatalf("over-cap preview=%q err=%v", preview, err)
	}
	if err := ledger.IngestProviderRefund(ctx, over, audit); !errors.Is(err, ErrRefundExceedsPayment) {
		t.Fatalf("over-refund error=%v", err)
	}
	var overAnomalies int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies
		WHERE proposed_order_id=? AND provider='stripe' AND event_kind='refunded'
		  AND external_id='str-refund-2' AND reason='refund_exceeds_payment'`, orderID).Scan(&overAnomalies); err != nil {
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
		VALUES (?, 'stripe', 'str-hostile-pay', 43, 199900, 'USD', 2, 'succeeded', ?)`,
		orderID, providerIngressTime); err != nil {
		t.Fatal(err)
	}
	hostile := Refund{
		OrderID: orderID, Provider: PaymentMethodStripe, ExternalID: "str-hostile-refund",
		PaymentExternalID: "str-hostile-pay", PayerID: 42,
		AmountMinor: 199900, Currency: "USD", Scale: 2, OccurredAt: providerIngressTime.Add(3 * time.Minute),
	}
	if preview, err := ledger.PreviewProviderRefundIngress(ctx, hostile); err != nil || preview != PaymentIngressQuarantine {
		t.Fatalf("mismatched positive payer preview=%q err=%v", preview, err)
	}
}

func TestProviderCaptureIngressAcceptsPayerlessStripeFact(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "stripe-capture-ingress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, orderID, productID := seedStripeLedgerOrder(t, db)
	ctx := context.Background()
	fact := PaymentFact{
		Provider: PaymentMethodStripe, ExternalID: "str-provider-only-capture",
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
	assertStripeCaptureQuarantine(t, db, orderID, productID)

	preview, err = store.PreviewProviderCaptureIngress(ctx, orderID, fact)
	if err != nil || preview != PaymentIngressReplay {
		t.Fatalf("replay preview=%q err=%v", preview, err)
	}
	if err := store.IngestProviderCapture(ctx, orderID, fact, audit); err != nil {
		t.Fatalf("exact replay error=%v", err)
	}
	assertStripeCaptureQuarantine(t, db, orderID, productID)

	// Defense in depth: a positive payer id that disagrees with the order user
	// still rejects, and nothing is written for the rejected identity.
	wrongPayer := fact
	wrongPayer.ExternalID = "str-wrong-payer-capture"
	wrongPayer.PayerID = 43
	if _, err := store.PreviewProviderCaptureIngress(ctx, orderID, wrongPayer); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("wrong payer preview error=%v", err)
	}
	if err := store.IngestProviderCapture(ctx, orderID, wrongPayer, audit); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("wrong payer ingest error=%v", err)
	}
	var wrongAttempts int
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts
		WHERE external_id='str-wrong-payer-capture'`).Scan(&wrongAttempts)
	if wrongAttempts != 0 {
		t.Fatalf("wrong payer attempts=%d", wrongAttempts)
	}

	// A negative payer id rejects for stripe too: zero is the only payerless
	// identity the card rails may carry.
	negativePayer := fact
	negativePayer.ExternalID = "str-negative-payer-capture"
	negativePayer.PayerID = -1
	if _, err := store.PreviewProviderCaptureIngress(ctx, orderID, negativePayer); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("negative payer preview error=%v", err)
	}
	if err := store.IngestProviderCapture(ctx, orderID, negativePayer, audit); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("negative payer ingest error=%v", err)
	}

	// A missing provider timestamp is never acceptable, payer or not.
	noTime := fact
	noTime.ExternalID = "str-no-time-capture"
	noTime.OccurredAt = time.Time{}
	if _, err := store.PreviewProviderCaptureIngress(ctx, orderID, noTime); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("zero timestamp preview error=%v", err)
	}
	if err := store.IngestProviderCapture(ctx, orderID, noTime, audit); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("zero timestamp ingest error=%v", err)
	}
}

func assertStripeCaptureQuarantine(t *testing.T, db *DB, orderID, productID int64) {
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
		WHERE order_id = ? AND provider = 'stripe' AND external_id = 'str-provider-only-capture'
		  AND payer_id = 0 AND status = 'needs_review'`, orderID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events
		WHERE order_id = ? AND provider = 'stripe' AND event_kind = 'captured'
		  AND external_id = 'str-provider-only-capture' AND disposition = 'needs_review'`, orderID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_ingress_audits a
		JOIN payment_events e ON a.target_kind='payment_event' AND a.target_id=e.id
		WHERE a.order_id=? AND a.provider='stripe' AND a.event_kind='captured'
		  AND a.actor='operator:test' AND a.reason='provider-only capture'
		  AND e.external_id='str-provider-only-capture'`, orderID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if status != OrderStatusPending || orderState != OrderStatePlaced || paymentState != PaymentStateNeedsReview ||
		fulfillmentState != FulfillmentStateUnfulfilled || paymentID != "" || stock != 100 ||
		attempts != 1 || events != 1 || audits != 1 {
		t.Fatalf("status=%s order=%s payment=%s fulfillment=%s payment_id=%q stock=%d attempts=%d events=%d audits=%d",
			status, orderState, paymentState, fulfillmentState, paymentID, stock, attempts, events, audits)
	}
}

// TestInvalidProviderCapturePayerMatrix pins the capture payer rule for every
// provider: a positive payer id must equal the order user; exactly zero is
// accepted only on the rails that have no Telegram payer identity (the card
// rails yookassa/stripe and the on-chain/IPN rails ton/nowpayments); a
// negative payer id is rejected for every provider. The DB-only balance
// forward-pin is not in the payerless set, so a zero payer rejects for it.
// The negative-payer rejection is a deliberate tightening: yookassa accepted
// negatives before the stripe storage acceptance generalized the predicate.
func TestInvalidProviderCapturePayerMatrix(t *testing.T) {
	tests := []struct {
		name        string
		provider    string
		payerID     int64
		orderUserID int64
		wantInvalid bool
	}{
		{name: "stars payerless rejects", provider: PaymentMethodStars, payerID: 0, orderUserID: 42, wantInvalid: true},
		{name: "stars matching payer accepts", provider: PaymentMethodStars, payerID: 42, orderUserID: 42, wantInvalid: false},
		{name: "stars mismatched payer rejects", provider: PaymentMethodStars, payerID: 43, orderUserID: 42, wantInvalid: true},
		{name: "stars negative payer rejects", provider: PaymentMethodStars, payerID: -1, orderUserID: 42, wantInvalid: true},
		{name: "crypto payerless rejects", provider: PaymentMethodCrypto, payerID: 0, orderUserID: 42, wantInvalid: true},
		{name: "crypto matching payer accepts", provider: PaymentMethodCrypto, payerID: 42, orderUserID: 42, wantInvalid: false},
		{name: "crypto mismatched payer rejects", provider: PaymentMethodCrypto, payerID: 43, orderUserID: 42, wantInvalid: true},
		{name: "crypto negative payer rejects", provider: PaymentMethodCrypto, payerID: -1, orderUserID: 42, wantInvalid: true},
		{name: "yookassa payerless accepts", provider: PaymentMethodYooKassa, payerID: 0, orderUserID: 42, wantInvalid: false},
		{name: "yookassa matching payer accepts", provider: PaymentMethodYooKassa, payerID: 42, orderUserID: 42, wantInvalid: false},
		{name: "yookassa mismatched payer rejects", provider: PaymentMethodYooKassa, payerID: 43, orderUserID: 42, wantInvalid: true},
		{name: "yookassa negative payer rejects", provider: PaymentMethodYooKassa, payerID: -1, orderUserID: 42, wantInvalid: true},
		{name: "stripe payerless accepts", provider: PaymentMethodStripe, payerID: 0, orderUserID: 42, wantInvalid: false},
		{name: "stripe matching payer accepts", provider: PaymentMethodStripe, payerID: 42, orderUserID: 42, wantInvalid: false},
		{name: "stripe mismatched payer rejects", provider: PaymentMethodStripe, payerID: 43, orderUserID: 42, wantInvalid: true},
		{name: "stripe negative payer rejects", provider: PaymentMethodStripe, payerID: -1, orderUserID: 42, wantInvalid: true},
		{name: "ton payerless accepts", provider: PaymentMethodTON, payerID: 0, orderUserID: 42, wantInvalid: false},
		{name: "ton matching payer accepts", provider: PaymentMethodTON, payerID: 42, orderUserID: 42, wantInvalid: false},
		{name: "ton mismatched payer rejects", provider: PaymentMethodTON, payerID: 43, orderUserID: 42, wantInvalid: true},
		{name: "ton negative payer rejects", provider: PaymentMethodTON, payerID: -1, orderUserID: 42, wantInvalid: true},
		{name: "nowpayments payerless accepts", provider: PaymentMethodNowpayments, payerID: 0, orderUserID: 42, wantInvalid: false},
		{name: "nowpayments matching payer accepts", provider: PaymentMethodNowpayments, payerID: 42, orderUserID: 42, wantInvalid: false},
		{name: "nowpayments mismatched payer rejects", provider: PaymentMethodNowpayments, payerID: 43, orderUserID: 42, wantInvalid: true},
		{name: "nowpayments negative payer rejects", provider: PaymentMethodNowpayments, payerID: -1, orderUserID: 42, wantInvalid: true},
		{name: "balance payerless rejects", provider: PaymentMethodBalance, payerID: 0, orderUserID: 42, wantInvalid: true},
		{name: "balance mismatched payer rejects", provider: PaymentMethodBalance, payerID: 43, orderUserID: 42, wantInvalid: true},
		{name: "balance negative payer rejects", provider: PaymentMethodBalance, payerID: -1, orderUserID: 42, wantInvalid: true},
	}
	for _, tc := range tests {
		fact := PaymentFact{Provider: tc.provider, PayerID: tc.payerID}
		if got := invalidProviderCapturePayer(fact, tc.orderUserID); got != tc.wantInvalid {
			t.Fatalf("%s: invalidProviderCapturePayer(%s/%d, %d)=%v, want %v",
				tc.name, tc.provider, tc.payerID, tc.orderUserID, got, tc.wantInvalid)
		}
	}
}

// TestProviderCaptureIngressRejectsNegativeYooKassaPayer proves the
// deliberate tightening end to end at both capture gates: a negative payer id
// on a yookassa fact rejected neither before nor after is now rejected, and
// nothing is written for the rejected identity.
func TestProviderCaptureIngressRejectsNegativeYooKassaPayer(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "yookassa-negative-payer.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, orderID, _ := seedYooKassaLedgerOrder(t, db)
	ctx := context.Background()
	fact := PaymentFact{
		Provider: PaymentMethodYooKassa, ExternalID: "yoo-negative-payer-capture",
		PayerID: -1, AmountMinor: 184908, Currency: "RUB", Scale: 2, OccurredAt: providerIngressTime,
	}
	audit := PaymentIngressAudit{Actor: "operator:test", Reason: "provider-only capture"}
	if _, err := store.PreviewProviderCaptureIngress(ctx, orderID, fact); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("negative payer preview error=%v", err)
	}
	if err := store.IngestProviderCapture(ctx, orderID, fact, audit); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("negative payer ingest error=%v", err)
	}
	var attempts int
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts
		WHERE external_id='yoo-negative-payer-capture'`).Scan(&attempts)
	if attempts != 0 {
		t.Fatalf("negative payer attempts=%d", attempts)
	}
}

func TestPaymentReviewListsPreviewsAndResolvesStripeTargets(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "stripe-resolve.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	store, orderID, _ := seedStripeLedgerOrder(t, db)
	if err := store.UpdateOrderStatus(ctx, orderID, OrderStatusPending, OrderStatusPaid, PaymentMethodStripe, "str-capture-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUnexpectedPayment(ctx, orderID, PaymentMethodStripe, "str-capture-b", "second_charge"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("unexpected capture error=%v", err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	if err := ledger.RecordRefund(ctx, Refund{
		OrderID: orderID, Provider: PaymentMethodStripe, ExternalID: "str-refund-b",
		PaymentExternalID: "str-capture-b", AmountMinor: 199900, Currency: "USD", Scale: 2,
	}); err != nil {
		t.Fatalf("stripe refund error=%v", err)
	}

	cases, err := ledger.ListPaymentReviews(ctx, PaymentMethodStripe)
	if err != nil || len(cases) != 1 {
		t.Fatalf("cases=%+v err=%v", cases, err)
	}
	resolution := PaymentReviewResolution{
		OrderID: orderID, Provider: PaymentMethodStripe, Actor: "operator:test",
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
	cases, err = ledger.ListPaymentReviews(ctx, PaymentMethodStripe)
	if err != nil || len(cases) != 0 {
		t.Fatalf("final cases=%+v err=%v", cases, err)
	}
}
