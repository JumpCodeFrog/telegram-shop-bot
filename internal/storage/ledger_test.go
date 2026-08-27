package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func seedLedgerOrder(t *testing.T, db *DB, totalStars int) (*SQLOrderStore, int64, int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Conn().ExecContext(ctx, `INSERT INTO categories (name) VALUES ('ledger')`); err != nil {
		t.Fatal(err)
	}
	res, err := db.Conn().ExecContext(ctx,
		`INSERT INTO products (category_id, name, price_usd, price_stars, stock, is_active)
		 VALUES (1, 'Widget', 12.50, ?, 100, 1)`, totalStars)
	if err != nil {
		t.Fatal(err)
	}
	productID, _ := res.LastInsertId()
	store := NewSQLOrderStore(db)
	orderID, err := store.CreateOrder(ctx, &Order{
		UserID: 42, TotalUSD: 12.50, TotalStars: totalStars, Status: OrderStatusPending,
	}, []OrderItem{{ProductID: productID, ProductName: "Widget", Quantity: 2, PriceUSD: 12.50}})
	if err != nil {
		t.Fatal(err)
	}
	return store, orderID, productID
}

func paymentEventOccurredAt(t *testing.T, db *DB, kind, externalID string) time.Time {
	t.Helper()
	var occurredAt time.Time
	if err := db.Conn().QueryRow(`SELECT occurred_at FROM payment_events
		WHERE event_kind=? AND external_id=? ORDER BY id DESC LIMIT 1`, kind, externalID).Scan(&occurredAt); err != nil {
		t.Fatal(err)
	}
	return occurredAt
}

func seedSubscriptionLedgerOrder(t *testing.T, db *DB, userID int64, totalStars int) (*SQLOrderStore, int64, int64, Subscription) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Conn().ExecContext(ctx, `INSERT INTO categories (name) VALUES ('subscriptions')`); err != nil {
		t.Fatal(err)
	}
	var categoryID int64
	if err := db.Conn().QueryRowContext(ctx, `SELECT id FROM categories ORDER BY id DESC LIMIT 1`).Scan(&categoryID); err != nil {
		t.Fatal(err)
	}
	res, err := db.Conn().ExecContext(ctx, `
		INSERT INTO products
			(category_id, name, price_usd, price_stars, stock, is_active, sub_period_days)
		VALUES (?, 'Recurring', 12.50, ?, 100, 1, 30)`, categoryID, totalStars)
	if err != nil {
		t.Fatal(err)
	}
	productID, _ := res.LastInsertId()
	store := NewSQLOrderStore(db)
	orderID, err := store.CreateOrder(ctx, &Order{
		UserID: userID, TotalUSD: 12.50, TotalStars: totalStars, Status: OrderStatusPending,
		SubscriptionProductID: productID, SubscriptionPeriodDays: 30,
	}, []OrderItem{{ProductID: productID, ProductName: "Recurring", Quantity: 1, PriceUSD: 12.50}})
	if err != nil {
		t.Fatal(err)
	}
	initial := Subscription{
		UserID: userID, ProductID: productID, OrderID: orderID,
		ChargeID: "initial-charge", Status: SubStatusActive,
		ExpiresAt: time.Now().UTC().Add(30 * 24 * time.Hour),
	}
	if err := store.UpdateOrderStatusWithSubscription(ctx, orderID, OrderStatusPending, OrderStatusPaid, "stars", initial.ChargeID, initial); err != nil {
		t.Fatal(err)
	}
	return store, orderID, productID, initial
}

func TestPaymentLedgerSettlementAndTimeline(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, productID := seedLedgerOrder(t, db, 250)
	if err := store.UpdateOrderStatus(context.Background(), orderID,
		OrderStatusPending, OrderStatusPaid, "stars", "charge-1"); err != nil {
		t.Fatal(err)
	}

	order, err := store.GetOrder(context.Background(), orderID)
	if err != nil {
		t.Fatal(err)
	}
	if order.PaymentState != PaymentStateSettled || order.OrderState != OrderStatePlaced ||
		order.FulfillmentState != FulfillmentStateUnfulfilled {
		t.Fatalf("order states = %+v", order)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	attempts, _ := ledger.ListPaymentAttempts(context.Background(), orderID)
	events, _ := ledger.ListOrderEvents(context.Background(), orderID)
	if len(attempts) != 1 || attempts[0].Status != "succeeded" || attempts[0].AmountMinor != 250 {
		t.Fatalf("attempts = %+v", attempts)
	}
	if len(events) != 2 || events[1].EventType != "payment.settled" {
		t.Fatalf("events = %+v", events)
	}
	var stock int
	_ = db.Conn().QueryRow(`SELECT stock FROM products WHERE id = ?`, productID).Scan(&stock)
	if stock != 98 {
		t.Fatalf("stock = %d", stock)
	}
}

func TestPaymentLedgerCrossOrderIdentityConflictRollsBack(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "identity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, first, _ := seedLedgerOrder(t, db, 100)
	second, err := store.CreateOrder(context.Background(), &Order{
		UserID: 43, TotalUSD: 12.50, TotalStars: 100, Status: OrderStatusPending,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateOrderStatus(context.Background(), first, OrderStatusPending, OrderStatusPaid, "stars", "same-charge"); err != nil {
		t.Fatal(err)
	}
	err = store.UpdateOrderStatus(context.Background(), second, OrderStatusPending, OrderStatusPaid, "stars", "same-charge")
	if !errors.Is(err, ErrPaymentIdentityConflict) {
		t.Fatalf("error = %v", err)
	}
	order, _ := store.GetOrder(context.Background(), second)
	if order.Status != OrderStatusPending || order.PaymentState != PaymentStateNeedsReview {
		t.Fatalf("second order = %+v", order)
	}
}

func TestPaymentLedgerDistinctSecondChargeIsDurableNeedsReview(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "second-charge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, productID := seedLedgerOrder(t, db, 100)
	if err := store.UpdateOrderStatus(context.Background(), orderID, OrderStatusPending, OrderStatusPaid, "stars", "charge-a"); err != nil {
		t.Fatal(err)
	}
	err = store.RecordUnexpectedPayment(context.Background(), orderID, "stars", "charge-b", "second_charge")
	if !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("second charge error = %v", err)
	}
	order, _ := store.GetOrder(context.Background(), orderID)
	if order.Status != OrderStatusPaid || order.PaymentState != PaymentStateNeedsReview || order.PaymentID != "charge-a" {
		t.Fatalf("order = %+v", order)
	}
	issues, err := NewSQLPaymentLedgerStore(db).ListPaymentEvents(context.Background(), "needs_review")
	if err != nil || len(issues) != 1 || issues[0].EventKind != PaymentEventCaptured || issues[0].ExternalID != "charge-b" {
		t.Fatalf("issues=%+v err=%v", issues, err)
	}
	var stock int
	_ = db.Conn().QueryRow(`SELECT stock FROM products WHERE id = ?`, productID).Scan(&stock)
	if stock != 98 {
		t.Fatalf("stock after second charge = %d", stock)
	}
}

func TestResolvedNeedsReviewCaptureReplayIsNoOpButMismatchReopensReview(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "resolved-capture-replay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	store, orderID, _ := seedLedgerOrder(t, db, 100)
	expiry := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Second)
	fact := PaymentFact{
		Provider: PaymentMethodStars, ExternalID: "resolved-capture",
		AmountMinor: 100, Currency: "XTR", Scale: 0,
		EntitlementExpiresAt: expiry,
	}
	if err := store.RecordUnexpectedPaymentFact(ctx, orderID, fact, "provider_confirmed"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatal(err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	if err := ledger.RecordRefund(ctx, Refund{
		OrderID: orderID, Provider: PaymentMethodStars, ExternalID: "resolved-refund",
		PaymentExternalID: fact.ExternalID, AmountMinor: 100, Currency: "XTR", Scale: 0,
	}); err != nil {
		t.Fatal(err)
	}
	cases, err := ledger.ListPaymentReviews(ctx, PaymentMethodStars)
	if err != nil || len(cases) != 1 || cases[0].OrderID != orderID {
		t.Fatalf("cases=%+v err=%v", cases, err)
	}
	resolution := PaymentReviewResolution{
		OrderID: orderID, Provider: PaymentMethodStars,
		Actor: "operator:test", Reason: "capture compensated",
		ResultingPaymentState: PaymentStateCancelled,
	}
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
	var beforeEvents, beforeAnomalies, beforeResolutions int
	var beforeState string
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events WHERE order_id=?`, orderID).Scan(&beforeEvents)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies WHERE proposed_order_id=?`, orderID).Scan(&beforeAnomalies)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_resolutions WHERE order_id=?`, orderID).Scan(&beforeResolutions)
	_ = db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&beforeState)
	// The append-only capture, rather than a later mutable order-price edit, is
	// authoritative when classifying an already-resolved provider redelivery.
	if _, err := db.Conn().Exec(`UPDATE orders SET total_stars=101 WHERE id=?`, orderID); err != nil {
		t.Fatal(err)
	}

	if err := store.RecordUnexpectedPaymentFact(ctx, orderID, fact, "provider_redelivery"); err != nil {
		t.Fatalf("exact resolved replay: %v", err)
	}
	var afterEvents, afterAnomalies, afterResolutions, attempts int
	var afterState string
	var persistedExpiry time.Time
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events WHERE order_id=?`, orderID).Scan(&afterEvents)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies WHERE proposed_order_id=?`, orderID).Scan(&afterAnomalies)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_resolutions WHERE order_id=?`, orderID).Scan(&afterResolutions)
	_ = db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&afterState)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts
		WHERE order_id=? AND provider='stars' AND external_id='resolved-capture'`, orderID).Scan(&attempts)
	_ = db.Conn().QueryRow(`SELECT entitlement_expires_at FROM payment_attempts
		WHERE order_id=? AND provider='stars' AND external_id='resolved-capture'`, orderID).Scan(&persistedExpiry)
	if beforeState != PaymentStateCancelled || afterState != beforeState ||
		afterEvents != beforeEvents || afterAnomalies != beforeAnomalies || afterResolutions != beforeResolutions ||
		attempts != 1 || !persistedExpiry.Equal(expiry) {
		t.Fatalf("before=(state=%s events=%d anomalies=%d resolutions=%d) after=(state=%s events=%d anomalies=%d resolutions=%d attempts=%d expiry=%v)",
			beforeState, beforeEvents, beforeAnomalies, beforeResolutions,
			afterState, afterEvents, afterAnomalies, afterResolutions, attempts, persistedExpiry)
	}

	for _, mismatch := range []PaymentFact{
		{Provider: PaymentMethodStars, ExternalID: fact.ExternalID, AmountMinor: 99, Currency: "XTR", Scale: 0, EntitlementExpiresAt: expiry},
		{Provider: PaymentMethodStars, ExternalID: fact.ExternalID, AmountMinor: 100, Currency: "USD", Scale: 0, EntitlementExpiresAt: expiry},
		{Provider: PaymentMethodStars, ExternalID: fact.ExternalID, AmountMinor: 100, Currency: "XTR", Scale: 1, EntitlementExpiresAt: expiry},
	} {
		if err := store.RecordUnexpectedPaymentFact(ctx, orderID, mismatch, "provider_redelivery"); !errors.Is(err, ErrPaymentReceiptMismatch) || !errors.Is(err, ErrPaymentNeedsReview) {
			t.Fatalf("mismatch=%+v error=%v", mismatch, err)
		}
	}
	var mismatchAnomalies int
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies
		WHERE proposed_order_id=? AND reason='unexpected_payment_fact_mismatch'`, orderID).Scan(&mismatchAnomalies)
	_ = db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&afterState)
	if mismatchAnomalies != 3 || afterState != PaymentStateNeedsReview {
		t.Fatalf("mismatch anomalies=%d state=%s", mismatchAnomalies, afterState)
	}
}

func TestSubscriptionRenewalIsSettledIdempotentAndDoesNotReplayStock(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "renewal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, productID, initial := seedSubscriptionLedgerOrder(t, db, 42, 100)
	ctx := context.Background()
	renewal := initial
	renewal.ChargeID = "renewal-charge"
	renewal.ExpiresAt = initial.ExpiresAt.Add(30 * 24 * time.Hour)
	if err := store.RecordSubscriptionRenewal(ctx, orderID, "stars", "renewal-charge", renewal); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSubscriptionRenewal(ctx, orderID, "stars", "renewal-charge", renewal); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	order, err := store.GetOrder(ctx, orderID)
	if err != nil {
		t.Fatal(err)
	}
	if order.PaymentState != PaymentStateSettled || order.PaymentID != "initial-charge" {
		t.Fatalf("order = %+v", order)
	}
	var stock, renewals, captures int
	if err := db.Conn().QueryRow(`SELECT stock FROM products WHERE id = ?`, productID).Scan(&stock); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM order_events WHERE order_id = ? AND event_type = 'payment.subscription_renewed'`, orderID).Scan(&renewals); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events WHERE order_id = ? AND event_kind = 'captured' AND disposition = 'settled'`, orderID).Scan(&captures); err != nil {
		t.Fatal(err)
	}
	if stock != 99 || renewals != 1 || captures != 2 {
		t.Fatalf("stock=%d renewals=%d captures=%d", stock, renewals, captures)
	}
}

func TestSubscriptionRenewalCrossOrderIdentityFailsClosed(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "renewal-conflict.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, first, productID, firstInitial := seedSubscriptionLedgerOrder(t, db, 42, 100)
	ctx := context.Background()
	second, err := store.CreateOrder(ctx, &Order{
		UserID: 43, TotalStars: 100, Status: OrderStatusPending,
		SubscriptionProductID: productID, SubscriptionPeriodDays: 30,
	}, []OrderItem{{ProductID: productID, Quantity: 1}})
	if err != nil {
		t.Fatal(err)
	}
	secondInitial := Subscription{UserID: 43, ProductID: productID, OrderID: second, ChargeID: "second-initial", ExpiresAt: time.Now().UTC().Add(30 * 24 * time.Hour)}
	if err := store.UpdateOrderStatusWithSubscription(ctx, second, OrderStatusPending, OrderStatusPaid, "stars", "second-initial", secondInitial); err != nil {
		t.Fatal(err)
	}
	firstRenewal := firstInitial
	firstRenewal.ChargeID = "shared-renewal"
	firstRenewal.ExpiresAt = firstInitial.ExpiresAt.Add(30 * 24 * time.Hour)
	if err := store.RecordSubscriptionRenewal(ctx, first, "stars", "shared-renewal", firstRenewal); err != nil {
		t.Fatal(err)
	}
	secondRenewal := secondInitial
	secondRenewal.ChargeID = "shared-renewal"
	secondRenewal.ExpiresAt = secondInitial.ExpiresAt.Add(30 * 24 * time.Hour)
	if err := store.RecordSubscriptionRenewal(ctx, second, "stars", "shared-renewal", secondRenewal); !errors.Is(err, ErrPaymentIdentityConflict) {
		t.Fatalf("error = %v", err)
	}
	order, _ := store.GetOrder(ctx, second)
	if order.PaymentState != PaymentStateNeedsReview {
		t.Fatalf("second order = %+v", order)
	}
}

func TestRenewalAfterPartialRefundIsDurable(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "renewal-after-refund.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, _, initial := seedSubscriptionLedgerOrder(t, db, 42, 100)
	ctx := context.Background()
	ledger := NewSQLPaymentLedgerStore(db)
	if err := ledger.RecordRefund(ctx, Refund{OrderID: orderID, Provider: "stars", ExternalID: "partial", PaymentExternalID: initial.ChargeID, AmountMinor: 40, Currency: "XTR", Scale: 0}); err != nil {
		t.Fatal(err)
	}
	renewal := initial
	renewal.ChargeID = "renewal"
	renewal.ExpiresAt = initial.ExpiresAt.Add(30 * 24 * time.Hour)
	if err := store.RecordSubscriptionRenewal(ctx, orderID, "stars", "renewal", renewal); err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id = ? AND status='succeeded'`, orderID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	order, _ := store.GetOrder(ctx, orderID)
	if attempts != 2 || order.PaymentState != PaymentStatePartiallyRefunded {
		t.Fatalf("attempts=%d order=%+v", attempts, order)
	}
}

func TestRenewalAfterFullRefundIsQuarantinedWithoutEntitlement(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "renewal-after-full-refund.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, _, initial := seedSubscriptionLedgerOrder(t, db, 42, 100)
	ctx := context.Background()
	ledger := NewSQLPaymentLedgerStore(db)
	if err := ledger.RecordRefund(ctx, Refund{OrderID: orderID, Provider: "stars", ExternalID: "full", PaymentExternalID: initial.ChargeID, AmountMinor: 100, Currency: "XTR", Scale: 0}); err != nil {
		t.Fatal(err)
	}
	renewal := initial
	renewal.ChargeID = "renewal-after-full"
	renewal.ExpiresAt = initial.ExpiresAt.Add(30 * 24 * time.Hour)
	if err := store.RecordSubscriptionRenewal(ctx, orderID, "stars", renewal.ChargeID, renewal); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("renewal error=%v", err)
	}
	order, _ := store.GetOrder(ctx, orderID)
	var succeeded, review int
	var subscriptionStatus string
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=? AND status='succeeded'`, orderID).Scan(&succeeded)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=? AND status='needs_review'`, orderID).Scan(&review)
	_ = db.Conn().QueryRow(`SELECT status FROM subscriptions WHERE order_id=?`, orderID).Scan(&subscriptionStatus)
	if succeeded != 1 || review != 1 || order.PaymentState != PaymentStateNeedsReview || subscriptionStatus != SubStatusExpired {
		t.Fatalf("succeeded=%d review=%d subscription=%s order=%+v", succeeded, review, subscriptionStatus, order)
	}
}

func TestSubscriptionRenewalReplayRepairsLegacySplitCommitOnce(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "renewal-repair.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, _, initial := seedSubscriptionLedgerOrder(t, db, 42, 100)
	ctx := context.Background()
	target := initial.ExpiresAt.Add(30 * 24 * time.Hour).UTC()
	res, err := db.Conn().Exec(`INSERT INTO payment_attempts
		(order_id,provider,external_id,amount_minor,currency,scale,status)
		VALUES (?,'stars','legacy-renewal',100,'XTR',0,'succeeded')`, orderID)
	if err != nil {
		t.Fatal(err)
	}
	attemptID, _ := res.LastInsertId()
	if _, err := db.Conn().Exec(`INSERT INTO payment_events
		(order_id,payment_attempt_id,provider,event_kind,external_id,amount_minor,currency,scale,disposition)
		VALUES (?,?,'stars','captured','legacy-renewal',100,'XTR',0,'settled')`, orderID, attemptID); err != nil {
		t.Fatal(err)
	}
	renewal := initial
	renewal.ChargeID = "legacy-renewal"
	renewal.ExpiresAt = target
	if err := store.RecordSubscriptionRenewal(ctx, orderID, "stars", renewal.ChargeID, renewal); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSubscriptionRenewal(ctx, orderID, "stars", renewal.ChargeID, renewal); err != nil {
		t.Fatal(err)
	}
	var gotExpiry, persistedTarget time.Time
	var attempts int
	if err := db.Conn().QueryRow(`SELECT expires_at FROM subscriptions WHERE order_id=?`, orderID).Scan(&gotExpiry); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT entitlement_expires_at FROM payment_attempts WHERE id=?`, attemptID).Scan(&persistedTarget); err != nil {
		t.Fatal(err)
	}
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE provider='stars' AND external_id='legacy-renewal'`).Scan(&attempts)
	if gotExpiry.Unix() != target.Unix() || persistedTarget.Unix() != target.Unix() || attempts != 1 {
		t.Fatalf("expiry=%v target=%v persisted=%v attempts=%d", gotExpiry, target, persistedTarget, attempts)
	}
}

func TestFreshRenewalMustAdvanceExpiryOrQuarantine(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "renewal-non-advancing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, _, initial := seedSubscriptionLedgerOrder(t, db, 42, 100)
	renewal := initial
	renewal.ChargeID = "non-advancing"
	err = store.RecordSubscriptionRenewal(context.Background(), orderID, "stars", renewal.ChargeID, renewal)
	if !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("error=%v", err)
	}
	if err := store.RecordSubscriptionRenewal(context.Background(), orderID, "stars", renewal.ChargeID, renewal); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("exact quarantine replay error=%v", err)
	}
	var status string
	var expiry, quarantinedExpiry time.Time
	var attempts int
	_ = db.Conn().QueryRow(`SELECT status, entitlement_expires_at FROM payment_attempts
		WHERE external_id='non-advancing'`).Scan(&status, &quarantinedExpiry)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE external_id='non-advancing'`).Scan(&attempts)
	_ = db.Conn().QueryRow(`SELECT expires_at FROM subscriptions WHERE order_id=?`, orderID).Scan(&expiry)
	if status != "needs_review" || attempts != 1 || expiry.Unix() != initial.ExpiresAt.Unix() ||
		!quarantinedExpiry.Equal(renewal.ExpiresAt) {
		t.Fatalf("status=%s attempts=%d expiry=%v initial=%v quarantined=%v",
			status, attempts, expiry, initial.ExpiresAt, quarantinedExpiry)
	}
}

func TestSubscriptionRenewalExpiryConflictPreservesImmutableFacts(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "renewal-expiry-conflict.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, _, initial := seedSubscriptionLedgerOrder(t, db, 42, 100)
	ctx := context.Background()
	original := initial
	original.ChargeID = "expiry-conflict"
	original.ExpiresAt = initial.ExpiresAt.Add(30 * 24 * time.Hour).UTC().Truncate(time.Second)
	originalFact := PaymentFact{
		Provider: PaymentMethodStars, ExternalID: original.ChargeID,
		AmountMinor: 100, Currency: "XTR", Scale: 0,
		EntitlementExpiresAt: original.ExpiresAt,
	}
	if err := store.RecordSubscriptionRenewalFact(ctx, orderID, originalFact, original); err != nil {
		t.Fatal(err)
	}
	// Exact replay keeps one immutable capture and expiry.
	if err := store.RecordSubscriptionRenewalFact(ctx, orderID, originalFact, original); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	conflict := original
	conflict.ExpiresAt = original.ExpiresAt.Add(time.Hour)
	conflictFact := originalFact
	conflictFact.EntitlementExpiresAt = conflict.ExpiresAt
	if err := store.RecordSubscriptionRenewalFact(ctx, orderID, conflictFact, conflict); !errors.Is(err, ErrPaymentIdentityConflict) {
		t.Fatalf("conflicting expiry error=%v", err)
	}

	var persistedExpiry time.Time
	var attempts, anomalies int
	if err := db.Conn().QueryRow(`SELECT entitlement_expires_at FROM payment_attempts
		WHERE provider='stars' AND external_id='expiry-conflict'`).Scan(&persistedExpiry); err != nil {
		t.Fatal(err)
	}
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts
		WHERE provider='stars' AND external_id='expiry-conflict'`).Scan(&attempts)
	rawConflict := "entitlement_expires_at:" + conflict.ExpiresAt.UTC().Format(time.RFC3339Nano)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies
		WHERE proposed_order_id=? AND provider='stars' AND external_id='expiry-conflict'
		  AND raw_payload=? AND reason='subscription_identity_conflict'`, orderID, rawConflict).Scan(&anomalies)
	if attempts != 1 || !persistedExpiry.Equal(original.ExpiresAt) || anomalies != 1 {
		t.Fatalf("attempts=%d persisted=%v original=%v anomalies=%d",
			attempts, persistedExpiry, original.ExpiresAt, anomalies)
	}
}

func TestRecordUnexpectedPaymentAfterOutOfStock(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "paid-out-of-stock.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, first, productID := seedLedgerOrder(t, db, 25)
	second, err := store.CreateOrder(context.Background(), &Order{
		UserID: 43, TotalUSD: 12.50, TotalStars: 25, Status: OrderStatusPending,
	}, []OrderItem{{ProductID: productID, ProductName: "Widget", Quantity: 99, PriceUSD: 12.50}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateOrderStatus(context.Background(), first, OrderStatusPending, OrderStatusPaid, "stars", "first"); err != nil {
		t.Fatal(err)
	}
	err = store.UpdateOrderStatus(context.Background(), second, OrderStatusPending, OrderStatusPaid, "stars", "charged")
	if !errors.Is(err, ErrProductOutOfStock) {
		t.Fatalf("settlement error = %v", err)
	}
	if err := store.RecordUnexpectedPayment(context.Background(), second, "stars", "charged", "out_of_stock_after_charge"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("record unexpected = %v", err)
	}
	order, _ := store.GetOrder(context.Background(), second)
	if order.Status != OrderStatusPending || order.PaymentState != PaymentStateNeedsReview {
		t.Fatalf("order = %+v", order)
	}
	issues, _ := NewSQLPaymentLedgerStore(db).ListPaymentEvents(context.Background(), "needs_review")
	if len(issues) != 1 {
		t.Fatalf("issues = %+v", issues)
	}
}

func TestPaymentLedgerConcurrentReplayFiftyExactlyOnce(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "concurrent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, productID := seedLedgerOrder(t, db, 300)
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.UpdateOrderStatus(context.Background(), orderID,
				OrderStatusPending, OrderStatusPaid, "stars", "replay-charge")
		}()
	}
	wg.Wait()
	close(errs)
	winners := 0
	for err := range errs {
		if err == nil {
			winners++
			continue
		}
		if !errors.Is(err, ErrOrderStatusConflict) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d", winners)
	}
	for query, want := range map[string]int{
		`SELECT COUNT(*) FROM payment_attempts WHERE order_id = ?`:                                1,
		`SELECT COUNT(*) FROM payment_events WHERE order_id = ? AND event_kind = 'captured'`:      1,
		`SELECT COUNT(*) FROM order_events WHERE order_id = ? AND event_type = 'payment.settled'`: 1,
	} {
		var got int
		if err := db.Conn().QueryRow(query, orderID).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: got=%d err=%v want=%d", query, got, err, want)
		}
	}
	var stock int
	_ = db.Conn().QueryRow(`SELECT stock FROM products WHERE id = ?`, productID).Scan(&stock)
	if stock != 98 {
		t.Fatalf("stock = %d", stock)
	}
}

func TestRefundBoundariesIdempotencyAndAppendOnly(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "refund.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, _ := seedLedgerOrder(t, db, 100)
	if err := store.UpdateOrderStatus(context.Background(), orderID, OrderStatusPending, OrderStatusPaid, "stars", "capture"); err != nil {
		t.Fatal(err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	partial := Refund{OrderID: orderID, Provider: "stars", ExternalID: "refund-1", PaymentExternalID: "capture", AmountMinor: 40, Currency: "XTR", Scale: 0}
	if err := ledger.RecordRefund(context.Background(), partial); err != nil {
		t.Fatal(err)
	}
	if err := ledger.RecordRefund(context.Background(), partial); err != nil {
		t.Fatal(err)
	}
	full := partial
	full.ExternalID = "refund-2"
	full.AmountMinor = 60
	if err := ledger.RecordRefund(context.Background(), full); err != nil {
		t.Fatal(err)
	}
	order, _ := store.GetOrder(context.Background(), orderID)
	if order.PaymentState != PaymentStateRefunded || order.Status != OrderStatusPaid {
		t.Fatalf("order = %+v", order)
	}
	events, err := ledger.ListOrderEvents(context.Background(), orderID)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.FromState != PaymentStatePartiallyRefunded || last.ToState != PaymentStateRefunded {
		t.Fatalf("last refund event = %+v", last)
	}
	if _, err := db.Conn().Exec(`DELETE FROM payment_events`); err == nil {
		t.Fatal("payment_events delete unexpectedly succeeded")
	}
	if _, err := db.Conn().Exec(`UPDATE order_events SET event_type = 'tampered' WHERE order_id = ?`, orderID); err == nil {
		t.Fatal("order_events update unexpectedly succeeded")
	}
	if _, err := db.Conn().Exec(`UPDATE payment_attempts SET external_id = 'tampered' WHERE order_id = ?`, orderID); err == nil {
		t.Fatal("payment attempt identity update unexpectedly succeeded")
	}
	conflictingReplay := partial
	conflictingReplay.AmountMinor = 41
	if err := ledger.RecordRefund(context.Background(), conflictingReplay); !errors.Is(err, ErrPaymentIdentityConflict) {
		t.Fatalf("conflicting refund replay = %v", err)
	}
}

func TestRefundStateAggregatesAllSettledCaptures(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "renewal-refund.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, _, initial := seedSubscriptionLedgerOrder(t, db, 42, 100)
	ctx := context.Background()
	renewal := initial
	renewal.ChargeID = "renewal"
	renewal.ExpiresAt = initial.ExpiresAt.Add(30 * 24 * time.Hour)
	if err := store.RecordSubscriptionRenewal(ctx, orderID, "stars", "renewal", renewal); err != nil {
		t.Fatal(err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	if err := ledger.RecordRefund(ctx, Refund{OrderID: orderID, Provider: "stars", ExternalID: "refund-renewal", PaymentExternalID: "renewal", AmountMinor: 100, Currency: "XTR", Scale: 0}); err != nil {
		t.Fatal(err)
	}
	order, err := store.GetOrder(ctx, orderID)
	if err != nil {
		t.Fatal(err)
	}
	if order.PaymentState != PaymentStatePartiallyRefunded {
		t.Fatalf("payment_state=%s want=%s", order.PaymentState, PaymentStatePartiallyRefunded)
	}
}

func TestConcurrentTwoValidRefundsDoNotLoseProviderFacts(t *testing.T) {
	for round := 0; round < 20; round++ {
		db, err := New(filepath.Join(t.TempDir(), fmt.Sprintf("refund-race-%d.db", round)))
		if err != nil {
			t.Fatal(err)
		}
		store, orderID, _ := seedLedgerOrder(t, db, 100)
		ctx := context.Background()
		if err := store.UpdateOrderStatus(ctx, orderID, OrderStatusPending, OrderStatusPaid, "stars", "capture"); err != nil {
			t.Fatal(err)
		}
		ledger := NewSQLPaymentLedgerStore(db)
		refunds := []Refund{
			{OrderID: orderID, Provider: "stars", ExternalID: "r1", PaymentExternalID: "capture", AmountMinor: 50, Currency: "XTR", Scale: 0},
			{OrderID: orderID, Provider: "stars", ExternalID: "r2", PaymentExternalID: "capture", AmountMinor: 50, Currency: "XTR", Scale: 0},
		}
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i := range refunds {
			wg.Add(1)
			go func(i int) { defer wg.Done(); errs[i] = ledger.RecordRefund(ctx, refunds[i]) }(i)
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatalf("round=%d errors=%v", round, errs)
			}
		}
		var count int
		if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM refunds WHERE order_id = ?`, orderID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		order, err := store.GetOrder(ctx, orderID)
		if err != nil {
			t.Fatal(err)
		}
		if count != 2 || order.PaymentState != PaymentStateRefunded {
			t.Fatalf("round=%d count=%d state=%s", round, count, order.PaymentState)
		}
		_ = db.Close()
	}
}

func TestReconcileReport(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "reconcile.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, _ := seedLedgerOrder(t, db, 50)
	if err := store.UpdateOrderStatus(context.Background(), orderID, OrderStatusPending, OrderStatusPaid, "stars", "match"); err != nil {
		t.Fatal(err)
	}
	localAt := paymentEventOccurredAt(t, db, PaymentEventCaptured, "match")
	report, err := NewSQLPaymentLedgerStore(db).Reconcile(context.Background(), "stars", []ProviderTransaction{
		{Kind: PaymentEventCaptured, ExternalID: "match", OrderID: orderID, PayloadValid: true, AmountMinor: 50, Currency: "XTR", Scale: 0, PayerID: 42, OccurredAt: localAt},
		{Kind: PaymentEventCaptured, ExternalID: "provider-only", OrderID: 999, PayloadValid: true, AmountMinor: 10, Currency: "XTR", Scale: 0, PayerID: 999, OccurredAt: time.Unix(1_700_000_001, 0)},
		{Kind: PaymentEventCaptured, ExternalID: "match", OrderID: orderID, PayloadValid: true, AmountMinor: 51, Currency: "XTR", Scale: 0, PayerID: 42, OccurredAt: localAt},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(report.Matched, report.ProviderOnly, report.AmountMismatch, report.NeedsReview, report.WindowComplete) != "1 1 1 1 false" {
		t.Fatalf("report = %+v", report)
	}
}

func TestReconcileDeduplicatesProviderWindow(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "reconcile-duplicates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, _ := seedLedgerOrder(t, db, 7)
	if err := store.UpdateOrderStatus(context.Background(), orderID, OrderStatusPending, OrderStatusPaid, "stars", "same"); err != nil {
		t.Fatal(err)
	}
	row := ProviderTransaction{Kind: PaymentEventCaptured, ExternalID: "same", OrderID: orderID, PayloadValid: true, AmountMinor: 7, Currency: "XTR", Scale: 0, PayerID: 42, OccurredAt: paymentEventOccurredAt(t, db, PaymentEventCaptured, "same")}
	report, err := NewSQLPaymentLedgerStore(db).Reconcile(context.Background(), "stars", []ProviderTransaction{row, row}, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Matched != 1 || report.DuplicateRows != 1 || report.NeedsReview == 0 {
		t.Fatalf("report = %+v", report)
	}
}

func TestReconcileCompleteWindowReportsLocalOnly(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "reconcile-local-only.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, _ := seedLedgerOrder(t, db, 9)
	if err := store.UpdateOrderStatus(context.Background(), orderID, OrderStatusPending, OrderStatusPaid, "stars", "local-only"); err != nil {
		t.Fatal(err)
	}
	report, err := NewSQLPaymentLedgerStore(db).Reconcile(context.Background(), "stars", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.LocalOnly != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestReconcileDoesNotCountNeedsReviewCaptureAsMatched(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "reconcile-needs-review.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, orderID, _ := seedLedgerOrder(t, db, 11)
	if err := store.RecordUnexpectedPayment(context.Background(), orderID, "stars", "review", "provider_only"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatal(err)
	}
	report, err := NewSQLPaymentLedgerStore(db).Reconcile(context.Background(), "stars", []ProviderTransaction{
		{Kind: PaymentEventCaptured, ExternalID: "review", OrderID: orderID, PayloadValid: true, AmountMinor: 11, Currency: "XTR", Scale: 0, PayerID: 42, OccurredAt: time.Unix(1_700_000_000, 0)},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Matched != 0 || report.AmountMismatch != 1 || report.NeedsReview != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestReconcileCannotGreenWithIdentityConflict(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "reconcile-identity-conflict.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, first, _ := seedLedgerOrder(t, db, 5)
	ctx := context.Background()
	second, err := store.CreateOrder(ctx, &Order{UserID: 43, TotalStars: 5, Status: OrderStatusPending}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateOrderStatus(ctx, first, OrderStatusPending, OrderStatusPaid, "stars", "same"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateOrderStatus(ctx, second, OrderStatusPending, OrderStatusPaid, "stars", "same"); !errors.Is(err, ErrPaymentIdentityConflict) {
		t.Fatalf("conflict=%v", err)
	}
	report, err := NewSQLPaymentLedgerStore(db).Reconcile(ctx, "stars", []ProviderTransaction{{Kind: PaymentEventCaptured, ExternalID: "same", OrderID: first, PayloadValid: true, AmountMinor: 5, Currency: "XTR", Scale: 0, PayerID: 42, OccurredAt: paymentEventOccurredAt(t, db, PaymentEventCaptured, "same")}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Matched != 1 || report.NeedsReview == 0 {
		t.Fatalf("report = %+v", report)
	}
}
