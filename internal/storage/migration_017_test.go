package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func preCommerceLedgerDB(t *testing.T) *DB {
	t.Helper()
	conn, err := sql.Open("sqlite", dsn(filepath.Join(t.TempDir(), "v16.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Exec(`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	db := &DB{conn: conn}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "017_commerce_ledger.sql" {
			break
		}
		statements, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if err := db.applyMigration(entry.Name(), string(statements)); err != nil {
			t.Fatalf("apply %s: %v", entry.Name(), err)
		}
	}
	return db
}

func TestMigration017QuarantinesPendingOrderAgainstUnexpiredEntitlement(t *testing.T) {
	db := preCommerceLedgerDB(t)
	if _, err := db.Conn().Exec(`INSERT INTO categories (name) VALUES ('plans')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`INSERT INTO products
		(category_id,name,price_usd,price_stars,stock,is_active,sub_period_days)
		VALUES (1,'Plan',5,100,10,1,30)`); err != nil {
		t.Fatal(err)
	}
	oldRes, err := db.Conn().Exec(`INSERT INTO orders
		(user_id,total_usd,total_stars,payment_method,payment_id,status)
		VALUES (42,5,100,'stars','old','paid')`)
	if err != nil {
		t.Fatal(err)
	}
	oldOrder, _ := oldRes.LastInsertId()
	if _, err := db.Conn().Exec(`INSERT INTO order_items (order_id,product_id,product_name,quantity,price_usd) VALUES (?,1,'Plan',1,5)`, oldOrder); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`INSERT INTO subscriptions
		(user_id,product_id,order_id,telegram_charge_id,status,expires_at)
		VALUES (42,1,?,'old','canceled',?)`, oldOrder, time.Now().UTC().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	newRes, err := db.Conn().Exec(`INSERT INTO orders (user_id,total_usd,total_stars,status) VALUES (42,5,100,'pending')`)
	if err != nil {
		t.Fatal(err)
	}
	newOrder, _ := newRes.LastInsertId()
	if _, err := db.Conn().Exec(`INSERT INTO order_items (order_id,product_id,product_name,quantity,price_usd) VALUES (?,1,'Plan',1,5)`, newOrder); err != nil {
		t.Fatal(err)
	}
	applyCommerceLedgerMigration(t, db)
	var state string
	if err := db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, newOrder).Scan(&state); err != nil {
		t.Fatal(err)
	}
	var events int
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM order_events WHERE order_id=? AND event_type='subscription.entitlement_conflict_quarantined'`, newOrder).Scan(&events)
	if state != PaymentStateNeedsReview || events != 1 {
		t.Fatalf("state=%s events=%d", state, events)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	cases, err := ledger.ListPaymentReviews(context.Background(), PaymentMethodStars)
	if err != nil || len(cases) != 1 || cases[0].OrderID != newOrder ||
		len(cases[0].Targets) != 1 || cases[0].Targets[0].Kind != PaymentReviewTargetOrder {
		t.Fatalf("review cases=%+v err=%v", cases, err)
	}
	report, err := ledger.Reconcile(context.Background(), PaymentMethodStars, nil, false)
	if err != nil || report.NeedsReview == 0 {
		t.Fatalf("subscription order-only review was not visible to reconciliation: report=%+v err=%v", report, err)
	}
	resolution := PaymentReviewResolution{
		OrderID: newOrder, Provider: PaymentMethodStars, OrderTargetID: newOrder,
		Actor: "operator:test", Reason: "unpaid legacy reservation cancelled",
		ResultingPaymentState: PaymentStateCancelled,
	}
	if _, err := ledger.PreviewPaymentReviewResolution(context.Background(), resolution); err != nil {
		t.Fatalf("preview resolution: %v", err)
	}
	if err := ledger.ResolvePaymentReview(context.Background(), resolution); err != nil {
		t.Fatalf("resolve reservation: %v", err)
	}
	var legacyStatus string
	_ = db.Conn().QueryRow(`SELECT status, payment_state FROM orders WHERE id=?`, newOrder).Scan(&legacyStatus, &state)
	if legacyStatus != OrderStatusCancelled || state != PaymentStateCancelled {
		t.Fatalf("resolved status=%s state=%s", legacyStatus, state)
	}
	if _, err := db.Conn().Exec(`UPDATE subscriptions SET expires_at=datetime('now','-1 day') WHERE order_id=?`, oldOrder); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSQLOrderStore(db).CreateOrder(context.Background(), &Order{
		UserID: 42, TotalUSD: 5, TotalStars: 100, Status: OrderStatusPending,
		SubscriptionProductID: 1, SubscriptionPeriodDays: 30,
	}, []OrderItem{{ProductID: 1, ProductName: "Plan", Quantity: 1, PriceUSD: 5}}); err != nil {
		t.Fatalf("replacement after expiry: %v", err)
	}
}

func applyCommerceLedgerMigration(t *testing.T, db *DB) {
	t.Helper()
	statements, err := migrationsFS.ReadFile("migrations/017_commerce_ledger.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.applyMigration("017_commerce_ledger.sql", string(statements)); err != nil {
		t.Fatal(err)
	}
}

func TestMigration017BackfillsUniqueLegacyCaptureAsSettled(t *testing.T) {
	db := preCommerceLedgerDB(t)
	const telegramPayerID int64 = 987654321
	if _, err := db.Conn().Exec(`INSERT INTO orders
		(user_id,total_usd,total_stars,payment_method,payment_id,status)
		VALUES (?,1.25,25,'stars','legacy-unique','paid')`, telegramPayerID); err != nil {
		t.Fatal(err)
	}
	applyCommerceLedgerMigration(t, db)
	var paymentState, attemptStatus, disposition string
	var payerID int64
	if err := db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=1`).Scan(&paymentState); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT status, payer_id FROM payment_attempts WHERE order_id=1`).Scan(&attemptStatus, &payerID); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT disposition FROM payment_events WHERE order_id=1 AND event_kind='captured'`).Scan(&disposition); err != nil {
		t.Fatal(err)
	}
	if paymentState != PaymentStateSettled || attemptStatus != "succeeded" || disposition != PaymentDispositionSettled || payerID != telegramPayerID {
		t.Fatalf("state=%s attempt=%s disposition=%s payer_id=%d", paymentState, attemptStatus, disposition, payerID)
	}
}

func TestMigration017BackfillsLegacyCryptoAsProviderUSDT(t *testing.T) {
	db := preCommerceLedgerDB(t)
	if _, err := db.Conn().Exec(`INSERT INTO orders
		(user_id,total_usd,total_stars,payment_method,payment_id,status)
		VALUES (42,12.34,0,'crypto','legacy-crypto','paid')`); err != nil {
		t.Fatal(err)
	}
	applyCommerceLedgerMigration(t, db)
	var currency string
	var amount int64
	if err := db.Conn().QueryRow(`SELECT currency, amount_minor FROM payment_attempts WHERE external_id='legacy-crypto'`).Scan(&currency, &amount); err != nil {
		t.Fatal(err)
	}
	if currency != "USDT" || amount != 1234 {
		t.Fatalf("currency=%s amount=%d", currency, amount)
	}
	err := NewSQLOrderStore(db).UpdateOrderStatusWithPaymentFact(context.Background(), 1,
		OrderStatusPending, OrderStatusPaid, PaymentFact{
			Provider: "crypto", ExternalID: "legacy-crypto", AmountMinor: 1234, Currency: "USDT", Scale: 2,
		})
	if !errors.Is(err, ErrOrderStatusConflict) {
		t.Fatalf("exact legacy replay error=%v", err)
	}
	var state string
	var anomalies int
	_ = db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=1`).Scan(&state)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies`).Scan(&anomalies)
	if state != PaymentStateSettled || anomalies != 0 {
		t.Fatalf("state=%s anomalies=%d", state, anomalies)
	}
}

func TestMigration017QuarantinesDuplicateAndMalformedLegacyCaptures(t *testing.T) {
	db := preCommerceLedgerDB(t)
	for _, values := range []string{
		`(41,1,10,'stars','duplicate','paid')`,
		`(42,1,10,'stars','duplicate','paid')`,
		`(43,1,-10,'stars','negative','paid')`,
	} {
		if _, err := db.Conn().Exec(`INSERT INTO orders
			(user_id,total_usd,total_stars,payment_method,payment_id,status) VALUES ` + values); err != nil {
			t.Fatal(err)
		}
	}
	applyCommerceLedgerMigration(t, db)
	var attempts, reviews int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM orders WHERE payment_state='needs_review'`).Scan(&reviews); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 || reviews != 3 {
		t.Fatalf("attempts=%d reviews=%d", attempts, reviews)
	}
}

func TestMigration017LegacyKnownProviderReviewRequiresExplicitCompensation(t *testing.T) {
	db := preCommerceLedgerDB(t)
	for _, values := range []string{
		`(41,1,100,'stars','','paid')`,
		`(42,1,-10,'stars','negative','paid')`,
	} {
		if _, err := db.Conn().Exec(`INSERT INTO orders
			(user_id,total_usd,total_stars,payment_method,payment_id,status) VALUES ` + values); err != nil {
			t.Fatal(err)
		}
	}
	applyCommerceLedgerMigration(t, db)

	ledger := NewSQLPaymentLedgerStore(db)
	cases, err := ledger.ListPaymentReviews(context.Background(), PaymentMethodStars)
	if err != nil || len(cases) != 2 {
		t.Fatalf("legacy cases=%+v err=%v", cases, err)
	}
	caseByOrder := make(map[int64]PaymentReviewCase, len(cases))
	for _, item := range cases {
		caseByOrder[item.OrderID] = item
	}
	lostIdentity := caseByOrder[1]
	if len(lostIdentity.Targets) != 1 || lostIdentity.Targets[0].Kind != PaymentReviewTargetAnomaly ||
		lostIdentity.Targets[0].ReasonCode != "legacy_capture_unverifiable" {
		t.Fatalf("lost identity case=%+v", lostIdentity)
	}
	resolution := PaymentReviewResolution{
		OrderID: 1, Provider: PaymentMethodStars,
		AnomalyIDs: []int64{lostIdentity.Targets[0].ID},
		Decision:   "compensated", Actor: "operator:test", Reason: "provider verified compensation",
		ResultingPaymentState: PaymentStateSettled,
	}
	if _, err := ledger.PreviewPaymentReviewResolution(context.Background(), resolution); !errors.Is(err, ErrPaymentReviewConflict) {
		t.Fatalf("legacy capture became settled: %v", err)
	}
	resolution.ResultingPaymentState = PaymentStateRefunded
	resolution.Decision = ""
	if _, err := ledger.PreviewPaymentReviewResolution(context.Background(), resolution); !errors.Is(err, ErrPaymentReviewConflict) {
		t.Fatalf("legacy capture accepted without decision: %v", err)
	}
	resolution.Decision = "compensated"
	if _, err := ledger.PreviewPaymentReviewResolution(context.Background(), resolution); err != nil {
		t.Fatalf("legacy compensation preview: %v", err)
	}
	if err := ledger.ResolvePaymentReview(context.Background(), resolution); err != nil {
		t.Fatalf("legacy compensation resolve: %v", err)
	}

	var state, status, decision string
	var attempts int
	if err := db.Conn().QueryRow(`SELECT payment_state,status FROM orders WHERE id=1`).Scan(&state, &status); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT decision FROM payment_resolutions
		WHERE target_kind='payment_anomaly' AND target_id=?`, lostIdentity.Targets[0].ID).Scan(&decision); err != nil {
		t.Fatal(err)
	}
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=1`).Scan(&attempts)
	if state != PaymentStateRefunded || status != OrderStatusPaid || decision != "compensated" || attempts != 0 {
		t.Fatalf("state=%s status=%s decision=%s attempts=%d", state, status, decision, attempts)
	}

	malformed := caseByOrder[2]
	if len(malformed.Targets) != 1 {
		t.Fatalf("malformed case=%+v", malformed)
	}
	malformedResolution := PaymentReviewResolution{
		OrderID: 2, Provider: PaymentMethodStars,
		AnomalyIDs: []int64{malformed.Targets[0].ID},
		Decision:   "compensated", Actor: "operator:test", Reason: "invalid amount cannot be verified",
		ResultingPaymentState: PaymentStateRefunded,
	}
	if _, err := ledger.PreviewPaymentReviewResolution(context.Background(), malformedResolution); !errors.Is(err, ErrPaymentReviewConflict) {
		t.Fatalf("malformed legacy amount accepted: %v", err)
	}
}

func TestMigration017LegacyReviewClosesAfterAuthenticatedCaptureAndRefund(t *testing.T) {
	db := preCommerceLedgerDB(t)
	if _, err := db.Conn().Exec(`INSERT INTO orders
		(user_id,total_usd,total_stars,payment_method,payment_id,status)
		VALUES (42,1,100,'stars','','paid')`); err != nil {
		t.Fatal(err)
	}
	applyCommerceLedgerMigration(t, db)

	ctx := context.Background()
	providerTime := time.Unix(1_700_000_000, 0).UTC()
	fact := PaymentFact{
		Provider: PaymentMethodStars, ExternalID: "authenticated-legacy-capture", PayerID: 42,
		AmountMinor: 100, Currency: "XTR", Scale: 0, OccurredAt: providerTime,
	}
	store := NewSQLOrderStore(db)
	if err := store.IngestProviderCapture(ctx, 1, fact, PaymentIngressAudit{
		Actor: "operator:test", Reason: "authenticated legacy capture",
	}); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("capture ingress=%v", err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	if err := ledger.IngestProviderRefund(ctx, Refund{
		OrderID: 1, Provider: PaymentMethodStars,
		ExternalID: fact.ExternalID, PaymentExternalID: fact.ExternalID, PayerID: 42,
		AmountMinor: 100, Currency: "XTR", Scale: 0, OccurredAt: providerTime.Add(time.Minute),
	}, PaymentIngressAudit{Actor: "operator:test", Reason: "authenticated legacy refund"}); err != nil {
		t.Fatalf("refund ingress=%v", err)
	}

	cases, err := ledger.ListPaymentReviews(ctx, PaymentMethodStars)
	if err != nil || len(cases) != 1 || len(cases[0].Targets) != 3 {
		t.Fatalf("review cases=%+v err=%v", cases, err)
	}
	resolution := PaymentReviewResolution{
		OrderID: 1, Provider: PaymentMethodStars, Decision: "compensated",
		Actor: "operator:test", Reason: "authenticated capture fully refunded",
		ResultingPaymentState: PaymentStateRefunded,
	}
	for _, target := range cases[0].Targets {
		switch target.Kind {
		case PaymentReviewTargetEvent:
			resolution.EventIDs = append(resolution.EventIDs, target.ID)
		case PaymentReviewTargetAnomaly:
			resolution.AnomalyIDs = append(resolution.AnomalyIDs, target.ID)
		}
	}
	if len(resolution.EventIDs) != 2 || len(resolution.AnomalyIDs) != 1 {
		t.Fatalf("exact targets=%+v", resolution)
	}
	if _, err := ledger.PreviewPaymentReviewResolution(ctx, resolution); err != nil {
		t.Fatalf("resolution preview=%v", err)
	}
	if err := ledger.ResolvePaymentReview(ctx, resolution); err != nil {
		t.Fatalf("resolution apply=%v", err)
	}
	if err := ledger.ResolvePaymentReview(ctx, resolution); err != nil {
		t.Fatalf("resolution exact replay=%v", err)
	}

	var state string
	var captureDecisions, refundDecisions, anomalyDecisions int
	if err := db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=1`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_resolutions r
		JOIN payment_events e ON r.target_kind='payment_event' AND r.target_id=e.id
		WHERE r.order_id=1 AND e.event_kind='captured' AND r.decision='compensated'`).Scan(&captureDecisions); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_resolutions r
		JOIN payment_events e ON r.target_kind='payment_event' AND r.target_id=e.id
		WHERE r.order_id=1 AND e.event_kind='refunded' AND r.decision='accepted_refund'`).Scan(&refundDecisions); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_resolutions
		WHERE order_id=1 AND target_kind='payment_anomaly' AND decision='compensated'`).Scan(&anomalyDecisions); err != nil {
		t.Fatal(err)
	}
	remaining, err := ledger.ListPaymentReviews(ctx, PaymentMethodStars)
	if err != nil || state != PaymentStateRefunded || captureDecisions != 1 || refundDecisions != 1 ||
		anomalyDecisions != 1 || len(remaining) != 0 {
		t.Fatalf("state=%s capture=%d refund=%d anomaly=%d remaining=%+v err=%v",
			state, captureDecisions, refundDecisions, anomalyDecisions, remaining, err)
	}
}

func TestMigration017BackfillsAndSerializesPendingSubscriptionOrders(t *testing.T) {
	db := preCommerceLedgerDB(t)
	if _, err := db.Conn().Exec(`INSERT INTO categories (name) VALUES ('plans')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`INSERT INTO products
		(category_id,name,price_usd,price_stars,stock,is_active,sub_period_days)
		VALUES (1,'Plan',5,100,10,1,30)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		res, err := db.Conn().Exec(`INSERT INTO orders (user_id,total_usd,total_stars,status) VALUES (42,5,100,'pending')`)
		if err != nil {
			t.Fatal(err)
		}
		orderID, _ := res.LastInsertId()
		if _, err := db.Conn().Exec(`INSERT INTO order_items
			(order_id,product_id,product_name,quantity,price_usd)
			VALUES (?,1,'Plan',1,5)`, orderID); err != nil {
			t.Fatal(err)
		}
	}
	applyCommerceLedgerMigration(t, db)
	rows, err := db.Conn().Query(`SELECT subscription_product_id, subscription_period_days, payment_state FROM orders ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var states []string
	for rows.Next() {
		var productID, days int
		var state string
		if err := rows.Scan(&productID, &days, &state); err != nil {
			t.Fatal(err)
		}
		if productID != 1 || days != 30 {
			t.Fatalf("snapshot product=%d days=%d", productID, days)
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 || states[0] != PaymentStatePending || states[1] != PaymentStateNeedsReview {
		t.Fatalf("states=%v", states)
	}
	var quarantineEvents int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM order_events WHERE event_type='subscription.duplicate_quarantined'`).Scan(&quarantineEvents); err != nil {
		t.Fatal(err)
	}
	if quarantineEvents != 1 {
		t.Fatalf("quarantine events=%d", quarantineEvents)
	}
}

func TestMigration017QuarantinesUnknownLegacyPaymentAsNeutralOrderTarget(t *testing.T) {
	db := preCommerceLedgerDB(t)
	if _, err := db.Conn().Exec(`INSERT INTO orders
		(user_id,total_usd,total_stars,payment_method,payment_id,status)
		VALUES (42,5,100,NULL,NULL,'paid')`); err != nil {
		t.Fatal(err)
	}
	applyCommerceLedgerMigration(t, db)

	var state string
	if err := db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=1`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	var attempts, anomalies, neutralEvents int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=1`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies WHERE proposed_order_id=1`).Scan(&anomalies); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM order_events
		WHERE order_id=1 AND event_type='payment.legacy_provider_unknown'`).Scan(&neutralEvents); err != nil {
		t.Fatal(err)
	}
	if state != PaymentStateNeedsReview || attempts != 0 || anomalies != 0 || neutralEvents != 1 {
		t.Fatalf("state=%s attempts=%d anomalies=%d neutral_events=%d", state, attempts, anomalies, neutralEvents)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	cases, err := ledger.ListPaymentReviews(context.Background(), PaymentReviewProviderUnknown)
	if err != nil || len(cases) != 1 || cases[0].OrderID != 1 || cases[0].Provider != PaymentReviewProviderUnknown ||
		len(cases[0].Targets) != 1 || cases[0].Targets[0].Kind != PaymentReviewTargetOrder ||
		cases[0].Targets[0].ReasonCode != "payment.legacy_provider_unknown" {
		t.Fatalf("neutral cases=%+v err=%v", cases, err)
	}
	for _, provider := range []string{PaymentMethodStars, PaymentMethodCrypto} {
		providerCases, err := ledger.ListPaymentReviews(context.Background(), provider)
		if err != nil || len(providerCases) != 0 {
			t.Fatalf("provider=%s cases=%+v err=%v", provider, providerCases, err)
		}
	}
}

func TestMigration017CoalescesNullableLegacyTimestamps(t *testing.T) {
	db := preCommerceLedgerDB(t)
	for _, values := range []string{
		`(41,1,10,'stars','legacy-without-time','paid',NULL,NULL)`,
		`(42,1,-10,'stars','malformed-without-time','paid',NULL,NULL)`,
		`(43,1,10,NULL,NULL,'pending',NULL,NULL)`,
	} {
		if _, err := db.Conn().Exec(`INSERT INTO orders
			(user_id,total_usd,total_stars,payment_method,payment_id,status,created_at,updated_at)
			VALUES ` + values); err != nil {
			t.Fatal(err)
		}
	}
	applyCommerceLedgerMigration(t, db)

	for _, query := range []string{
		`SELECT COUNT(*) FROM order_events WHERE occurred_at IS NULL`,
		`SELECT COUNT(*) FROM payment_attempts WHERE occurred_at IS NULL OR created_at IS NULL`,
		`SELECT COUNT(*) FROM payment_events WHERE occurred_at IS NULL OR created_at IS NULL`,
		`SELECT COUNT(*) FROM payment_anomalies WHERE occurred_at IS NULL`,
	} {
		var nulls int
		if err := db.Conn().QueryRow(query).Scan(&nulls); err != nil {
			t.Fatal(err)
		}
		if nulls != 0 {
			t.Fatalf("nullable timestamp rows=%d query=%s", nulls, query)
		}
	}
}
