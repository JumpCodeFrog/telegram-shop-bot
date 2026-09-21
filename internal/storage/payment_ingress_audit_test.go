package storage

import (
	"context"
	"errors"
	"testing"
)

// TestAppendPaymentIngressAuditAcceptsYooKassaStarsStripe proves the operator
// ingress audit gate accepts the yookassa provider identity. No exported path can
// attribute a yookassa audit yet — IngestProviderCapture demands a Telegram
// payer id the YooKassa API never provides, and the refund ingress gate
// belongs to Task 7 — so the unexported helper is driven directly inside a
// transaction, exactly the way recordPaymentAnomaly calls it. The stars
// behavior is unchanged; stripe is accepted too since the stripe provider
// plan's storage acceptance task added its app-level identity, and the
// allowlist still fails closed for providers without one.
// Feature: shop_bot, Property 2: Round-trip хранилища данных
// Validates: Requirements 12.5, 9.3
// The matrix this test actually exercises: yookassa, stars, stripe, and the
// sepa fail-closed rejection — the name was YooKassa-scoped historically.
func TestAppendPaymentIngressAuditAcceptsYooKassaStarsStripe(t *testing.T) {
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

	order := &Order{
		UserID:        42,
		TotalUSD:      23.00,
		TotalRUB:      1849.08,
		PaymentMethod: PaymentMethodYooKassa,
		Status:        OrderStatusPending,
	}
	orderID, err := store.CreateOrder(ctx, order, []OrderItem{{ProductID: 1, Quantity: 1, PriceUSD: 23.00}})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	// The audited target is a quarantined yookassa fact. The DB CHECKs have
	// accepted yookassa since migration 020, so seed the anomaly row directly
	// and isolate the app-level audit gate.
	res, err := db.Conn().ExecContext(ctx,
		`INSERT INTO payment_anomalies (fingerprint, proposed_order_id, provider, external_id, amount_minor, currency, scale, reason)
		 VALUES ('yoo-audit-fingerprint', ?, 'yookassa', 'pay_2', 100, 'RUB', 2, 'amount_mismatch')`, orderID)
	if err != nil {
		t.Fatalf("seed anomaly: %v", err)
	}
	anomalyID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	audit := PaymentIngressAudit{Actor: "operator:test", Reason: "webhook quarantine reviewed"}
	tx, err := db.Conn().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := appendPaymentIngressAudit(ctx, tx, orderID, PaymentMethodYooKassa,
		PaymentEventCaptured, PaymentIngressTargetAnomaly, anomalyID, audit); err != nil {
		tx.Rollback()
		t.Fatalf("append yookassa audit: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit audit: %v", err)
	}

	var provider, eventKind, targetKind, actor string
	var auditOrderID, targetID int64
	if err := db.Conn().QueryRowContext(ctx,
		`SELECT order_id, provider, event_kind, target_kind, target_id, actor
		 FROM payment_ingress_audits WHERE provider = ?`, PaymentMethodYooKassa).Scan(
		&auditOrderID, &provider, &eventKind, &targetKind, &targetID, &actor); err != nil {
		t.Fatalf("load yookassa audit: %v", err)
	}
	if auditOrderID != orderID || provider != PaymentMethodYooKassa || eventKind != PaymentEventCaptured ||
		targetKind != PaymentIngressTargetAnomaly || targetID != anomalyID || actor != "operator:test" {
		t.Fatalf("yookassa audit: order=%d provider=%s kind=%s target=%s/%d actor=%s",
			auditOrderID, provider, eventKind, targetKind, targetID, actor)
	}

	// Stars behavior is unchanged: the same call shape still appends.
	starsAudit := PaymentIngressAudit{Actor: "operator:stars", Reason: "stars quarantine reviewed"}
	tx, err = db.Conn().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin stars tx: %v", err)
	}
	if err := appendPaymentIngressAudit(ctx, tx, orderID, PaymentMethodStars,
		PaymentEventCaptured, PaymentIngressTargetAnomaly, anomalyID, starsAudit); err != nil {
		tx.Rollback()
		t.Fatalf("append stars audit: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit stars audit: %v", err)
	}

	// Stripe behavior mirrors yookassa: the stripe provider plan's storage
	// acceptance task added its app-level identity, so the same call shape
	// appends and the durable row attributes the operator write.
	stripeAudit := PaymentIngressAudit{Actor: "operator:stripe", Reason: "stripe quarantine reviewed"}
	tx, err = db.Conn().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin stripe tx: %v", err)
	}
	if err := appendPaymentIngressAudit(ctx, tx, orderID, PaymentMethodStripe,
		PaymentEventCaptured, PaymentIngressTargetAnomaly, anomalyID, stripeAudit); err != nil {
		tx.Rollback()
		t.Fatalf("append stripe audit: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit stripe audit: %v", err)
	}
	var stripeProvider string
	if err := db.Conn().QueryRowContext(ctx,
		`SELECT provider FROM payment_ingress_audits WHERE provider = ? AND actor = ?`,
		PaymentMethodStripe, "operator:stripe").Scan(&stripeProvider); err != nil {
		t.Fatalf("load stripe audit: %v", err)
	}
	if stripeProvider != PaymentMethodStripe {
		t.Fatalf("stripe audit provider=%s", stripeProvider)
	}

	// The allowlist still fails closed for providers with no app-level
	// identity.
	tx, err = db.Conn().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin unknown provider tx: %v", err)
	}
	defer tx.Rollback()
	if err := appendPaymentIngressAudit(ctx, tx, orderID, "sepa",
		PaymentEventCaptured, PaymentIngressTargetAnomaly, anomalyID, audit); !errors.Is(err, ErrPaymentReviewConflict) {
		t.Fatalf("unknown provider audit: err=%v, want ErrPaymentReviewConflict", err)
	}
}

// TestAppendPaymentIngressAuditAcceptsTONAndNowpayments proves the operator
// ingress audit gate accepts the ton and nowpayments provider identities,
// and that the allowlist still fails closed for the DB-only balance
// forward-pin. The unexported helper is driven directly inside a
// transaction, exactly the way recordPaymentAnomaly calls it.
// Feature: shop_bot, Property 2: Round-trip хранилища данных
// Validates: Requirements 12.5, 9.3
func TestAppendPaymentIngressAuditAcceptsTONAndNowpayments(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New(:memory:): %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	if _, err := db.Conn().ExecContext(ctx, "INSERT INTO categories (name) VALUES ('Cat')"); err != nil {
		t.Fatalf("seed category: %v", err)
	}
	if _, err := db.Conn().ExecContext(ctx,
		`INSERT INTO products (category_id, name, price_usd, price_stars, stock, is_active)
		 VALUES (1, 'Gadget', 0, 0, 10, 1)`); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	orderID := int64(1)

	// The audited target is a quarantined ton fact. The DB CHECKs have
	// accepted ton/nowpayments since migration 021, so seed the anomaly row
	// directly and isolate the app-level audit gate.
	res, err := db.Conn().ExecContext(ctx,
		`INSERT INTO payment_anomalies (fingerprint, proposed_order_id, provider, external_id, amount_minor, currency, scale, reason)
		 VALUES ('ton-audit-fingerprint', 0, 'ton', 'ton-tx-9', 100, 'TON', 9, 'amount_mismatch')`)
	if err != nil {
		t.Fatalf("seed anomaly: %v", err)
	}
	anomalyID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	for _, provider := range []string{PaymentMethodTON, PaymentMethodNowpayments} {
		audit := PaymentIngressAudit{Actor: "operator:" + provider, Reason: provider + " quarantine reviewed"}
		tx, err := db.Conn().BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin %s tx: %v", provider, err)
		}
		if err := appendPaymentIngressAudit(ctx, tx, orderID, provider,
			PaymentEventCaptured, PaymentIngressTargetAnomaly, anomalyID, audit); err != nil {
			tx.Rollback()
			t.Fatalf("append %s audit: %v", provider, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit %s audit: %v", provider, err)
		}
		var stored string
		if err := db.Conn().QueryRowContext(ctx,
			`SELECT provider FROM payment_ingress_audits WHERE provider = ? AND actor = ?`,
			provider, "operator:"+provider).Scan(&stored); err != nil {
			t.Fatalf("load %s audit: %v", provider, err)
		}
		if stored != provider {
			t.Fatalf("%s audit provider=%s", provider, stored)
		}
	}

	// The balance provider is now accepted app-side; its ingress-audit
	// acceptance leg (operator audit row persisted under the balance
	// identity) lives in balance_acceptance_test.go:
	// TestBalanceCaptureIngressRequiresPositivePayer.
}
