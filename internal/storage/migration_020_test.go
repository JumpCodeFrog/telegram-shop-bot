package storage

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// seedLegacyLedgerRows inserts representative stars/crypto rows into all six
// ledger tables on a schema-019 database. The payment_events row with the
// non-NULL payment_attempt_id must span the rebuild: dropping the referenced
// payment_attempts table mid-migration is exactly what defer_foreign_keys
// has to survive.
func seedLegacyLedgerRows(t *testing.T, db *DB) {
	t.Helper()
	statements := []string{
		`INSERT INTO orders (user_id, total_usd, total_stars, payment_method, payment_id, status)
		 VALUES (42, 1, 100, 'stars', 'seed-stars', 'paid'),
		        (43, 12.34, 0, 'crypto', 'seed-crypto', 'paid')`,
		`INSERT INTO payment_attempts
		 (id, order_id, provider, external_id, payer_id, amount_minor, currency, scale, status,
		  entitlement_expires_at, occurred_at, created_at)
		 VALUES (1, 1, 'stars', 'seed-att-stars', 42, 100, 'XTR', 0, 'succeeded', NULL,
		         '2026-01-02 03:04:05', '2026-01-02 03:04:06'),
		        (2, 2, 'crypto', 'seed-att-crypto', 43, 1234, 'USDT', 2, 'needs_review', NULL,
		         '2026-02-03 04:05:06', '2026-02-03 04:05:07')`,
		`INSERT INTO payment_events
		 (id, order_id, payment_attempt_id, provider, event_kind, external_id, amount_minor,
		  currency, scale, disposition, occurred_at, created_at)
		 VALUES (1, 1, 1, 'stars', 'captured', 'seed-att-stars', 100, 'XTR', 0, 'settled',
		         '2026-01-02 03:04:05', '2026-01-02 03:04:06'),
		        (2, 2, NULL, 'crypto', 'refunded', 'seed-evt-crypto', 1234, 'USDT', 2, 'observed',
		         '2026-02-03 04:05:06', '2026-02-03 04:05:07')`,
		`INSERT INTO payment_anomalies
		 (id, fingerprint, proposed_order_id, provider, event_kind, external_id,
		  related_external_id, payer_id, amount_minor, currency, scale, raw_amount, raw_payload,
		  reason, occurred_at)
		 VALUES (1, 'seed-fingerprint', 2, 'crypto', 'captured', 'seed-anom', 'seed-att-crypto',
		         43, 1234, 'USDT', 2, '12.34', '{"raw":true}', 'legacy_capture_unverifiable',
		         '2026-02-03 04:05:06')`,
		`INSERT INTO refunds
		 (id, order_id, provider, external_id, payment_external_id, payer_id, amount_minor,
		  currency, scale, status, requested_at, completed_at, created_at)
		 VALUES (1, 1, 'stars', 'seed-refund', 'seed-att-stars', 42, 100, 'XTR', 0, 'succeeded',
		         '2026-01-05 06:07:08', '2026-01-05 06:08:09', '2026-01-05 06:07:08')`,
		`INSERT INTO payment_resolutions
		 (id, order_id, provider, target_kind, target_id, decision, actor, reason,
		  resulting_payment_state, resolved_at)
		 VALUES (1, 1, 'stars', 'payment_event', 1, 'accepted_refund', 'operator:seed',
		         'seed refund accepted', 'refunded', '2026-01-05 06:08:09'),
		        (2, 0, 'unknown', 'order', 2, 'dismissed', 'operator:seed',
		         'seed neutral dismissal', 'needs_review', '2026-02-04 05:06:07')`,
		`INSERT INTO payment_ingress_audits
		 (id, order_id, provider, event_kind, target_kind, target_id, actor, reason, applied_at)
		 VALUES (1, 1, 'stars', 'refunded', 'refund', 1, 'operator:seed', 'seed refund ingress',
		         '2026-01-05 06:08:10')`,
	}
	for _, statement := range statements {
		if _, err := db.Conn().Exec(statement); err != nil {
			t.Fatalf("seed legacy ledger rows: %v\n%s", err, statement)
		}
	}
}

// TestMigration020PreservesLegacyLedgerRows rebuilds the six ledger tables on
// a database holding stars/crypto rows and proves the rebuild is lossless:
// identical columns, identical values, all 7 provider indexes, all 13
// six-table triggers, and no leftover *_new tables.
func TestMigration020PreservesLegacyLedgerRows(t *testing.T) {
	db := migrationDBBefore(t, "020_ledger_provider_yookassa.sql")
	seedLegacyLedgerRows(t, db)

	tables := ledgerRebuildTables()
	before := make(map[string][][]string, len(tables))
	for _, table := range tables {
		cols, rows := ledgerTableDump(t, db, table.name)
		if !reflect.DeepEqual(cols, table.cols) {
			t.Fatalf("%s schema-019 columns %v, want %v", table.name, cols, table.cols)
		}
		before[table.name] = rows
	}

	applyMigrationFile(t, db, "020_ledger_provider_yookassa.sql")

	for _, table := range tables {
		cols, rows := ledgerTableDump(t, db, table.name)
		if !reflect.DeepEqual(cols, table.cols) {
			t.Fatalf("%s columns changed by rebuild: got %v, want %v", table.name, cols, table.cols)
		}
		if !reflect.DeepEqual(rows, before[table.name]) {
			t.Fatalf("%s rows changed by rebuild:\n got  %v\n want %v", table.name, rows, before[table.name])
		}
	}

	assertLedgerRebuildInventory(t, db)
}

// TestMigration020AcceptsYooKassaAndStripeProviders proves the widened CHECKs
// accept the yookassa and stripe provider identities on all six tables
// (payment_resolutions also keeps 'unknown') while still rejecting an
// unapproved provider with a CHECK constraint failure.
func TestMigration020AcceptsYooKassaAndStripeProviders(t *testing.T) {
	db := migrationDBBefore(t, "020_ledger_provider_yookassa.sql")
	applyMigrationFile(t, db, "020_ledger_provider_yookassa.sql")
	if _, err := db.Conn().Exec(`INSERT INTO orders (user_id, total_usd, total_stars, status)
		VALUES (42, 1, 100, 'pending')`); err != nil {
		t.Fatal(err)
	}

	accepted := []string{
		`INSERT INTO payment_attempts
		 (order_id, provider, external_id, payer_id, amount_minor, currency, scale, status)
		 VALUES (1, 'yookassa', 'yoo-att-1', 42, 184908, 'RUB', 2, 'observed')`,
		`INSERT INTO payment_events
		 (order_id, payment_attempt_id, provider, event_kind, external_id, amount_minor,
		  currency, scale, disposition)
		 VALUES (1, NULL, 'yookassa', 'captured', 'yoo-evt-1', 184908, 'RUB', 2, 'observed')`,
		`INSERT INTO payment_anomalies
		 (fingerprint, proposed_order_id, provider, event_kind, external_id, payer_id,
		  amount_minor, currency, scale, reason)
		 VALUES ('yoo-fp-1', 1, 'yookassa', 'captured', 'yoo-anom-1', 42, 184908, 'RUB', 2,
		         'test yookassa anomaly')`,
		`INSERT INTO refunds
		 (order_id, provider, external_id, payment_external_id, payer_id, amount_minor,
		  currency, scale, status)
		 VALUES (1, 'yookassa', 'yoo-ref-1', 'yoo-att-1', 42, 100, 'RUB', 2, 'requested')`,
		`INSERT INTO payment_resolutions
		 (order_id, provider, target_kind, target_id, decision, actor, reason,
		  resulting_payment_state)
		 VALUES (1, 'yookassa', 'payment_anomaly', 101, 'dismissed', 'operator:test',
		         'yookassa anomaly dismissed', 'needs_review')`,
		`INSERT INTO payment_resolutions
		 (order_id, provider, target_kind, target_id, decision, actor, reason,
		  resulting_payment_state)
		 VALUES (1, 'unknown', 'order', 102, 'dismissed', 'operator:test',
		         'unknown provider dismissed', 'needs_review')`,
		`INSERT INTO payment_ingress_audits
		 (order_id, provider, event_kind, target_kind, target_id, actor, reason)
		 VALUES (1, 'yookassa', 'captured', 'refund', 101, 'operator:test',
		         'yookassa capture ingressed')`,
		`INSERT INTO payment_attempts
		 (order_id, provider, external_id, payer_id, amount_minor, currency, scale, status)
		 VALUES (1, 'stripe', 'str-att-1', 42, 1999, 'USD', 2, 'observed')`,
		`INSERT INTO payment_events
		 (order_id, payment_attempt_id, provider, event_kind, external_id, amount_minor,
		  currency, scale, disposition)
		 VALUES (1, NULL, 'stripe', 'captured', 'str-evt-1', 1999, 'USD', 2, 'observed')`,
		`INSERT INTO payment_anomalies
		 (fingerprint, proposed_order_id, provider, event_kind, external_id, payer_id,
		  amount_minor, currency, scale, reason)
		 VALUES ('str-fp-1', 1, 'stripe', 'captured', 'str-anom-1', 42, 1999, 'USD', 2,
		         'test stripe anomaly')`,
		`INSERT INTO refunds
		 (order_id, provider, external_id, payment_external_id, payer_id, amount_minor,
		  currency, scale, status)
		 VALUES (1, 'stripe', 'str-ref-1', 'str-att-1', 42, 100, 'USD', 2, 'requested')`,
		`INSERT INTO payment_resolutions
		 (order_id, provider, target_kind, target_id, decision, actor, reason,
		  resulting_payment_state)
		 VALUES (1, 'stripe', 'payment_anomaly', 201, 'dismissed', 'operator:test',
		         'stripe anomaly dismissed', 'needs_review')`,
		`INSERT INTO payment_ingress_audits
		 (order_id, provider, event_kind, target_kind, target_id, actor, reason)
		 VALUES (1, 'stripe', 'refunded', 'refund', 201, 'operator:test',
		         'stripe refund ingressed')`,
	}
	for _, statement := range accepted {
		if _, err := db.Conn().Exec(statement); err != nil {
			t.Fatalf("provider insert rejected: %v\n%s", err, statement)
		}
	}

	rejected := []string{
		`INSERT INTO payment_attempts
		 (order_id, provider, external_id, payer_id, amount_minor, currency, scale, status)
		 VALUES (1, 'paypal', 'pp-att-1', 42, 100, 'USD', 2, 'observed')`,
		`INSERT INTO payment_events
		 (order_id, payment_attempt_id, provider, event_kind, external_id, amount_minor,
		  currency, scale, disposition)
		 VALUES (1, NULL, 'paypal', 'captured', 'pp-evt-1', 100, 'USD', 2, 'observed')`,
		`INSERT INTO payment_anomalies
		 (fingerprint, proposed_order_id, provider, event_kind, external_id, payer_id,
		  amount_minor, currency, scale, reason)
		 VALUES ('pp-fp-1', 1, 'paypal', 'captured', 'pp-anom-1', 42, 100, 'USD', 2,
		         'test paypal anomaly')`,
		`INSERT INTO refunds
		 (order_id, provider, external_id, payment_external_id, payer_id, amount_minor,
		  currency, scale, status)
		 VALUES (1, 'paypal', 'pp-ref-1', 'pp-att-1', 42, 100, 'USD', 2, 'requested')`,
		`INSERT INTO payment_resolutions
		 (order_id, provider, target_kind, target_id, decision, actor, reason,
		  resulting_payment_state)
		 VALUES (1, 'paypal', 'payment_anomaly', 301, 'dismissed', 'operator:test',
		         'paypal anomaly dismissed', 'needs_review')`,
		`INSERT INTO payment_ingress_audits
		 (order_id, provider, event_kind, target_kind, target_id, actor, reason)
		 VALUES (1, 'paypal', 'captured', 'refund', 301, 'operator:test',
		         'paypal capture ingressed')`,
	}
	for _, statement := range rejected {
		_, err := db.Conn().Exec(statement)
		if err == nil {
			t.Fatalf("paypal insert unexpectedly accepted:\n%s", statement)
		}
		if !strings.Contains(strings.ToLower(err.Error()), "check constraint") {
			t.Fatalf("paypal insert failed for the wrong reason: %v\n%s", err, statement)
		}
	}
}

// TestMigration020ImmutabilityTriggersSurviveRebuild proves the rebuild
// recreated every ledger guard: identity immutability, append-only updates,
// and no-delete protection must all still abort after the tables are dropped
// and renamed.
func TestMigration020ImmutabilityTriggersSurviveRebuild(t *testing.T) {
	db := migrationDBBefore(t, "020_ledger_provider_yookassa.sql")
	seedLegacyLedgerRows(t, db)
	applyMigrationFile(t, db, "020_ledger_provider_yookassa.sql")

	for _, tc := range ledgerImmutabilityAborts() {
		_, err := db.Conn().Exec(tc.statement)
		if err == nil {
			t.Fatalf("%s: unexpectedly succeeded", tc.note)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error %q does not contain %q", tc.note, err.Error(), tc.want)
		}
	}
}

// TestYooKassaSettlementWritesLedgerRows is the end-to-end settlement the
// Task 3 round-trip test had to stop short of: a YooKassa order with a
// converted RUB total settles through the storage ledger (the money fact is
// derived from order.TotalRUB: 1849.08 -> 184908 kopecks, RUB, scale 2), the
// ledger rows land with provider 'yookassa', an identical replay stays
// idempotent, and a conflicting second charge quarantines the order
// (ErrPaymentNeedsReview) instead of dying on the provider CHECK.
// Feature: shop_bot, Property 2: Round-trip хранилища данных
// Validates: Requirements 12.5, 9.3
func TestYooKassaSettlementWritesLedgerRows(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New(:memory:): %v", err)
	}
	defer db.Close()

	store := NewSQLOrderStore(db)
	ctx := context.Background()

	if _, err := db.Conn().ExecContext(ctx, "INSERT INTO categories (name) VALUES ('Cat')"); err != nil {
		t.Fatalf("seed category: %v", err)
	}
	if _, err := db.Conn().ExecContext(ctx,
		`INSERT INTO products (category_id, name, price_usd, price_stars, stock, is_active)
		 VALUES (1, 'Gadget', 23.00, 0, 10, 1)`); err != nil {
		t.Fatalf("seed product: %v", err)
	}

	rubOrder := &Order{
		UserID:        42,
		TotalUSD:      23.00,
		TotalRUB:      1849.08,
		PaymentMethod: PaymentMethodYooKassa,
		Status:        OrderStatusPending,
	}
	items := []OrderItem{{ProductID: 1, Quantity: 1, PriceUSD: 23.00}}
	orderID, err := store.CreateOrder(ctx, rubOrder, items)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	if err := store.UpdateOrderStatus(ctx, orderID, OrderStatusPending, OrderStatusPaid,
		PaymentMethodYooKassa, "yoo-pay-1"); err != nil {
		t.Fatalf("yookassa settlement: %v", err)
	}

	var attemptOrderID int64
	var attemptPayerID, amountMinor int64
	var provider, currency, status string
	var scale int
	if err := db.Conn().QueryRowContext(ctx,
		`SELECT order_id, provider, external_id, payer_id, amount_minor, currency, scale, status
		 FROM payment_attempts WHERE external_id = 'yoo-pay-1'`).Scan(
		&attemptOrderID, &provider, new(string), &attemptPayerID, &amountMinor,
		&currency, &scale, &status); err != nil {
		t.Fatalf("load settled attempt: %v", err)
	}
	// The settlement path without a supplied fact derives the money fields
	// from the order but cannot know the payer, so payer_id stays 0; the
	// provider fact money (RUB, 184908 kopecks, scale 2) is what must match.
	if attemptOrderID != orderID || provider != PaymentMethodYooKassa || attemptPayerID != 0 ||
		amountMinor != 184908 || currency != "RUB" || scale != 2 || status != "succeeded" {
		t.Fatalf("settled attempt: order=%d provider=%s payer=%d amount=%d currency=%s scale=%d status=%s",
			attemptOrderID, provider, attemptPayerID, amountMinor, currency, scale, status)
	}

	var eventAttemptID sql.NullInt64
	var eventProvider, eventKind, disposition string
	if err := db.Conn().QueryRowContext(ctx,
		`SELECT payment_attempt_id, provider, event_kind, disposition
		 FROM payment_events WHERE external_id = 'yoo-pay-1'`).Scan(
		&eventAttemptID, &eventProvider, &eventKind, &disposition); err != nil {
		t.Fatalf("load settled event: %v", err)
	}
	if !eventAttemptID.Valid || eventProvider != PaymentMethodYooKassa ||
		eventKind != PaymentEventCaptured || disposition != PaymentDispositionSettled {
		t.Fatalf("settled event: attempt=%+v provider=%s kind=%s disposition=%s",
			eventAttemptID, eventProvider, eventKind, disposition)
	}

	var orderStatus, paymentState string
	if err := db.Conn().QueryRowContext(ctx,
		`SELECT status, payment_state FROM orders WHERE id = ?`, orderID).Scan(
		&orderStatus, &paymentState); err != nil {
		t.Fatalf("load settled order: %v", err)
	}
	if orderStatus != OrderStatusPaid || paymentState != PaymentStateSettled {
		t.Fatalf("settled order: status=%s payment_state=%s", orderStatus, paymentState)
	}

	// An identical replay is idempotent: the state CAS rejects it and no new
	// ledger rows appear.
	if err := store.UpdateOrderStatus(ctx, orderID, OrderStatusPending, OrderStatusPaid,
		PaymentMethodYooKassa, "yoo-pay-1"); !errors.Is(err, ErrOrderStatusConflict) {
		t.Fatalf("replayed settlement: err=%v, want ErrOrderStatusConflict", err)
	}
	for _, count := range []struct {
		query string
	}{
		{`SELECT COUNT(*) FROM payment_attempts WHERE provider = 'yookassa'`},
		{`SELECT COUNT(*) FROM payment_events WHERE provider = 'yookassa'`},
	} {
		var n int
		if err := db.Conn().QueryRowContext(ctx, count.query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("replay duplicated ledger rows: %s -> %d", count.query, n)
		}
	}

	// A conflicting fact — a distinct second charge on the settled order —
	// must quarantine (ErrPaymentNeedsReview) rather than fail on the CHECK.
	if err := store.RecordUnexpectedPayment(ctx, orderID, PaymentMethodYooKassa, "yoo-pay-2",
		"second_charge"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("conflicting fact quarantine: err=%v, want ErrPaymentNeedsReview", err)
	}
	if err := db.Conn().QueryRowContext(ctx,
		`SELECT status FROM payment_attempts WHERE external_id = 'yoo-pay-2'`).Scan(&status); err != nil {
		t.Fatalf("load quarantined attempt: %v", err)
	}
	if status != "needs_review" {
		t.Fatalf("quarantined attempt status=%s", status)
	}
	if err := db.Conn().QueryRowContext(ctx,
		`SELECT payment_state FROM orders WHERE id = ?`, orderID).Scan(&paymentState); err != nil {
		t.Fatal(err)
	}
	if paymentState != PaymentStateNeedsReview {
		t.Fatalf("quarantined order payment_state=%s", paymentState)
	}
}
