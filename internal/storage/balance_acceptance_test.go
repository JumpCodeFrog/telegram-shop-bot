package storage

// Acceptance legs for the internal balance rail. The ledger CHECKs admitted
// the 'balance' provider since migration 021 as a forward-pin; these tests
// pin the app-level acceptance now that the feature lands: balance facts are
// USD/scale-2 money with a REQUIRED positive payer equal to the order user
// (the rail always knows its Telegram payer — it is never in the payerless
// set), and every operator allowlist (anomalies, ingress audit, review
// listing/resolution, refunds) accepts the provider identity.

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"
)

// seedBalanceLedgerOrder mirrors seedNowpaymentsLedgerOrder for the internal
// balance rail: the order snapshots TotalUSD 19.99 (1999 cents, scale 2).
func seedBalanceLedgerOrder(t *testing.T, db *DB) (*SQLOrderStore, int64, int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Conn().ExecContext(ctx, `INSERT INTO categories (name) VALUES ('balance-ops')`); err != nil {
		t.Fatal(err)
	}
	res, err := db.Conn().ExecContext(ctx,
		`INSERT INTO products (category_id, name, price_usd, price_stars, stock, is_active)
		 VALUES (1, 'Gadget', 19.99, 0, 100, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	productID, _ := res.LastInsertId()
	store := NewSQLOrderStore(db)
	orderID, err := store.CreateOrder(ctx, &Order{
		UserID: 42, TotalUSD: 19.99, Status: OrderStatusPending,
	}, []OrderItem{{ProductID: productID, Quantity: 1, PriceUSD: 19.99}})
	if err != nil {
		t.Fatal(err)
	}
	return store, orderID, productID
}

func TestOrderMoneyBalance(t *testing.T) {
	order := Order{TotalUSD: 19.99}
	amount, currency, scale, err := orderMoney(order, PaymentMethodBalance)
	if err != nil || amount != 1999 || currency != "USD" || scale != 2 {
		t.Fatalf("orderMoney(balance) = %d %s %d err=%v, want 1999 USD 2", amount, currency, scale, err)
	}
	for _, total := range []float64{0, -5, math.Inf(1), math.Inf(-1), math.NaN()} {
		if _, _, _, err := orderMoney(Order{TotalUSD: total}, PaymentMethodBalance); !errors.Is(err, ErrInvalidMoney) {
			t.Fatalf("orderMoney(balance, total=%v): err=%v, want ErrInvalidMoney", total, err)
		}
	}
	if _, _, _, err := orderMoney(Order{TotalUSD: 19.99}, PaymentMethodBalance); err != nil {
		t.Fatal(err)
	}
}

func TestValidatePaymentFactBalance(t *testing.T) {
	order := Order{ID: 1, UserID: 42, TotalUSD: 19.99}
	valid := PaymentFact{
		Provider: PaymentMethodBalance, ExternalID: "balance:1",
		PayerID: 42, AmountMinor: 1999, Currency: "USD", Scale: 2,
		OccurredAt: providerIngressTime,
	}
	if _, err := validatePaymentFact(order, valid); err != nil {
		t.Fatalf("valid balance fact rejected: %v", err)
	}

	cases := map[string]PaymentFact{
		// The balance rail always knows its Telegram payer: zero and foreign
		// payers are both mismatches (mirrors the stars equality rule plus a
		// required-payer floor).
		"payer zero":     {Provider: PaymentMethodBalance, ExternalID: "balance:1", PayerID: 0, AmountMinor: 1999, Currency: "USD", Scale: 2, OccurredAt: providerIngressTime},
		"payer foreign":  {Provider: PaymentMethodBalance, ExternalID: "balance:1", PayerID: 43, AmountMinor: 1999, Currency: "USD", Scale: 2, OccurredAt: providerIngressTime},
		"payer negative": {Provider: PaymentMethodBalance, ExternalID: "balance:1", PayerID: -1, AmountMinor: 1999, Currency: "USD", Scale: 2, OccurredAt: providerIngressTime},
		"currency":       {Provider: PaymentMethodBalance, ExternalID: "balance:1", PayerID: 42, AmountMinor: 1999, Currency: "USDT", Scale: 2, OccurredAt: providerIngressTime},
		"scale":          {Provider: PaymentMethodBalance, ExternalID: "balance:1", PayerID: 42, AmountMinor: 1999, Currency: "USD", Scale: 0, OccurredAt: providerIngressTime},
		"amount":         {Provider: PaymentMethodBalance, ExternalID: "balance:1", PayerID: 42, AmountMinor: 1998, Currency: "USD", Scale: 2, OccurredAt: providerIngressTime},
		"no external id": {Provider: PaymentMethodBalance, ExternalID: "", PayerID: 42, AmountMinor: 1999, Currency: "USD", Scale: 2, OccurredAt: providerIngressTime},
	}
	for name, fact := range cases {
		if _, err := validatePaymentFact(order, fact); !errors.Is(err, ErrPaymentReceiptMismatch) {
			t.Fatalf("%s: err=%v, want ErrPaymentReceiptMismatch", name, err)
		}
	}
}

// TestBalanceCaptureIngressRequiresPositivePayer pins the payerless-set
// exclusion: providerHasNoTelegramPayer must NOT list balance, so a
// PayerID-0 balance fact is rejected at both capture gates, while the same
// fact with the order's user as payer is durably quarantined (with its
// operator audit row) exactly like a stars capture.
func TestBalanceCaptureIngressRequiresPositivePayer(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "balance-capture-ingress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, orderID, _ := seedBalanceLedgerOrder(t, db)
	ctx := context.Background()
	audit := PaymentIngressAudit{Actor: "operator:test", Reason: "balance capture review"}

	payerless := PaymentFact{
		Provider: PaymentMethodBalance, ExternalID: "balance:payerless",
		PayerID: 0, AmountMinor: 1999, Currency: "USD", Scale: 2, OccurredAt: providerIngressTime,
	}
	if _, err := store.PreviewProviderCaptureIngress(ctx, orderID, payerless); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("payerless preview err=%v, want ErrPaymentReceiptMismatch", err)
	}
	if err := store.IngestProviderCapture(ctx, orderID, payerless, audit); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("payerless ingest err=%v, want ErrPaymentReceiptMismatch", err)
	}
	var attempts int
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE external_id='balance:payerless'`).Scan(&attempts)
	if attempts != 0 {
		t.Fatalf("payerless balance attempts=%d, want 0", attempts)
	}

	fact := PaymentFact{
		Provider: PaymentMethodBalance, ExternalID: "balance:op-1",
		PayerID: 42, AmountMinor: 1999, Currency: "USD", Scale: 2, OccurredAt: providerIngressTime,
	}
	preview, err := store.PreviewProviderCaptureIngress(ctx, orderID, fact)
	if err != nil || preview != PaymentIngressQuarantine {
		t.Fatalf("initial preview=%q err=%v, want quarantine", preview, err)
	}
	if err := store.IngestProviderCapture(ctx, orderID, fact, audit); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("ingest err=%v, want ErrPaymentNeedsReview", err)
	}
	var quarantined, events, audits int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts
		WHERE order_id=? AND provider='balance' AND external_id='balance:op-1'
		  AND payer_id=42 AND amount_minor=1999 AND currency='USD' AND scale=2 AND status='needs_review'`, orderID).Scan(&quarantined); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events
		WHERE order_id=? AND provider='balance' AND external_id='balance:op-1' AND disposition='needs_review'`, orderID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_ingress_audits a
		JOIN payment_events e ON a.target_kind='payment_event' AND a.target_id=e.id
		WHERE a.order_id=? AND a.provider='balance' AND a.actor='operator:test' AND e.external_id='balance:op-1'`, orderID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if quarantined != 1 || events != 1 || audits != 1 {
		t.Fatalf("quarantined=%d events=%d audits=%d, want 1/1/1", quarantined, events, audits)
	}

	// Exact replay is a durable no-op.
	if preview, err := store.PreviewProviderCaptureIngress(ctx, orderID, fact); err != nil || preview != PaymentIngressReplay {
		t.Fatalf("replay preview=%q err=%v, want replay", preview, err)
	}
	if err := store.IngestProviderCapture(ctx, orderID, fact, audit); err != nil {
		t.Fatalf("replay ingest err=%v", err)
	}
}

// TestBalanceSettlementAndRefundLifecycle settles an order through the same
// fact path the shop layer uses, then records a full balance refund through
// the operator refund path: both allowlists (recordRefundOnce and the refund
// ingress preview) accept the balance identity, and the capture's positive
// payer corroborates the refund payer.
func TestBalanceSettlementAndRefundLifecycle(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "balance-refund.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, orderID, _ := seedBalanceLedgerOrder(t, db)
	ctx := context.Background()
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, PaymentFact{
		Provider: PaymentMethodBalance, ExternalID: "balance:7",
		PayerID: 42, AmountMinor: 1999, Currency: "USD", Scale: 2, OccurredAt: providerIngressTime,
	}); err != nil {
		t.Fatalf("settle via balance fact: %v", err)
	}
	var method, paymentID, state string
	if err := db.Conn().QueryRow(`SELECT payment_method, payment_id, payment_state FROM orders WHERE id=?`, orderID).
		Scan(&method, &paymentID, &state); err != nil {
		t.Fatal(err)
	}
	if method != PaymentMethodBalance || paymentID != "balance:7" || state != PaymentStateSettled {
		t.Fatalf("order after balance settle: method=%q payment_id=%q state=%q", method, paymentID, state)
	}

	ledger := NewSQLPaymentLedgerStore(db)
	refund := Refund{
		OrderID: orderID, Provider: PaymentMethodBalance, ExternalID: "bal-refund-1",
		PaymentExternalID: "balance:7", PayerID: 42,
		AmountMinor: 1999, Currency: "USD", Scale: 2, OccurredAt: providerIngressTime.Add(time.Minute),
	}
	preview, err := ledger.PreviewProviderRefundIngress(ctx, refund)
	if err != nil || preview != PaymentIngressApply {
		t.Fatalf("refund preview=%q err=%v, want apply", preview, err)
	}
	if err := ledger.RecordRefund(ctx, refund); err != nil {
		t.Fatalf("record balance refund: %v", err)
	}
	var refunds int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM refunds
		WHERE provider='balance' AND external_id='bal-refund-1' AND payer_id=42 AND status='succeeded'`).Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if refunds != 1 || state != PaymentStateRefunded {
		t.Fatalf("refunds=%d state=%s, want 1/refunded", refunds, state)
	}
}

// TestPaymentReviewListsAndResolvesBalanceTargets replaces the dormant-pin
// discipline test: the review queue and the resolution gates now accept the
// balance provider, so a quarantined second balance capture is listed,
// previewed and resolved exactly like any other rail.
func TestPaymentReviewListsAndResolvesBalanceTargets(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "balance-resolve.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	store, orderID, _ := seedBalanceLedgerOrder(t, db)
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, PaymentFact{
		Provider: PaymentMethodBalance, ExternalID: "balance:11",
		PayerID: 42, AmountMinor: 1999, Currency: "USD", Scale: 2, OccurredAt: providerIngressTime,
	}); err != nil {
		t.Fatal(err)
	}
	// A distinct second balance fact for the same order quarantines, and its
	// operator refund mirrors the proven ton/nowpayments review scenario.
	if err := store.RecordUnexpectedPaymentFact(ctx, orderID, PaymentFact{
		Provider: PaymentMethodBalance, ExternalID: "balance:11-dup",
		PayerID: 42, AmountMinor: 1999, Currency: "USD", Scale: 2, OccurredAt: providerIngressTime.Add(time.Minute),
	}, "second_charge"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("unexpected balance fact err=%v, want ErrPaymentNeedsReview", err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	if err := ledger.RecordRefund(ctx, Refund{
		OrderID: orderID, Provider: PaymentMethodBalance, ExternalID: "bal-refund-dup",
		PaymentExternalID: "balance:11-dup", PayerID: 42, AmountMinor: 1999, Currency: "USD", Scale: 2,
	}); err != nil {
		t.Fatalf("balance refund of quarantined capture: %v", err)
	}

	cases, err := ledger.ListPaymentReviews(ctx, PaymentMethodBalance)
	if err != nil || len(cases) != 1 {
		t.Fatalf("list balance reviews: cases=%+v err=%v, want 1 case", cases, err)
	}
	resolution := PaymentReviewResolution{
		OrderID: orderID, Provider: PaymentMethodBalance, Actor: "operator:test",
		Reason: "duplicate balance debit refunded", ResultingPaymentState: PaymentStateSettled,
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
		t.Fatalf("event targets=%v anomaly targets=%v, want 2 events", resolution.EventIDs, resolution.AnomalyIDs)
	}
	if _, err := ledger.PreviewPaymentReviewResolution(ctx, resolution); err != nil {
		t.Fatalf("preview balance resolution: %v", err)
	}
	if err := ledger.ResolvePaymentReview(ctx, resolution); err != nil {
		t.Fatalf("resolve balance review: %v", err)
	}
	cases, err = ledger.ListPaymentReviews(ctx, PaymentMethodBalance)
	if err != nil || len(cases) != 0 {
		t.Fatalf("final balance cases=%+v err=%v, want empty", cases, err)
	}
}

// TestBalanceAnomalyAccepted pins the anomaly allowlist: a balance fact that
// cannot be attached to an order is preserved under its own provider
// identity (no invalid_provider relabeling).
func TestBalanceAnomalyAccepted(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "balance-anomaly.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := NewSQLOrderStore(db)
	// A fresh anomaly insert reports the durable quarantine signal.
	err = store.RecordPaymentAnomaly(context.Background(), PaymentAnomaly{
		ProposedOrderID: 0, Provider: PaymentMethodBalance, EventKind: PaymentEventCaptured,
		ExternalID: "balance:orphan", PayerID: 42, AmountMinor: 1999, Currency: "USD", Scale: 2,
		Reason: "balance_orphan", OccurredAt: providerIngressTime,
	})
	if !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("record balance anomaly: err=%v, want ErrPaymentNeedsReview", err)
	}
	var provider, rawPayload string
	if err := db.Conn().QueryRow(`SELECT provider, raw_payload FROM payment_anomalies
		WHERE external_id='balance:orphan'`).Scan(&provider, &rawPayload); err != nil {
		t.Fatal(err)
	}
	if provider != PaymentMethodBalance || rawPayload != "" {
		t.Fatalf("balance anomaly provider=%q raw_payload=%q", provider, rawPayload)
	}
}
