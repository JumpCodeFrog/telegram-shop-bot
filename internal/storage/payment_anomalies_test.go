package storage

import (
	"context"
	"errors"
	"testing"
)

// TestRecordPaymentAnomalyAcceptsYooKassa proves the anomaly quarantine gate
// accepts the yookassa provider identity: a normalized webhook fact that
// cannot be attached safely is durably recorded (the documented
// ErrPaymentNeedsReview class) with its provider money, and the proposed
// order is quarantined in the same transaction.
// Feature: shop_bot, Property 2: Round-trip хранилища данных
// Validates: Requirements 12.5, 9.3
func TestRecordPaymentAnomalyAcceptsYooKassa(t *testing.T) {
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

	anomaly := PaymentAnomaly{
		ProposedOrderID: orderID,
		Provider:        PaymentMethodYooKassa,
		ExternalID:      "pay_2",
		AmountMinor:     100,
		Currency:        "RUB",
		Scale:           2,
		RawAmount:       "1.00",
		Reason:          "amount_mismatch",
	}
	if err := store.RecordPaymentAnomaly(ctx, anomaly); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("record yookassa anomaly: err=%v, want ErrPaymentNeedsReview", err)
	}

	var provider, externalID, currency, rawAmount, reason string
	var amountMinor int64
	var scale int
	if err := db.Conn().QueryRowContext(ctx,
		`SELECT provider, external_id, amount_minor, currency, scale, raw_amount, reason
		 FROM payment_anomalies WHERE provider = ? AND external_id = ?`,
		PaymentMethodYooKassa, "pay_2").Scan(
		&provider, &externalID, &amountMinor, &currency, &scale, &rawAmount, &reason); err != nil {
		t.Fatalf("load yookassa anomaly: %v", err)
	}
	if provider != PaymentMethodYooKassa || externalID != "pay_2" || amountMinor != 100 ||
		currency != "RUB" || scale != 2 || rawAmount != "1.00" || reason != "amount_mismatch" {
		t.Fatalf("yookassa anomaly: provider=%s id=%s amount=%d currency=%s scale=%d raw=%q reason=%q",
			provider, externalID, amountMinor, currency, scale, rawAmount, reason)
	}

	// Quarantine is part of the record contract: the proposed order moves to
	// needs_review so the webhook path cannot silently drop the fact.
	var paymentState string
	if err := db.Conn().QueryRowContext(ctx,
		`SELECT payment_state FROM orders WHERE id = ?`, orderID).Scan(&paymentState); err != nil {
		t.Fatal(err)
	}
	if paymentState != PaymentStateNeedsReview {
		t.Fatalf("quarantined order payment_state=%s, want %s", paymentState, PaymentStateNeedsReview)
	}
}

// TestRecordPaymentAnomalyAcceptsStripe proves the anomaly quarantine gate
// accepts the stripe provider identity: a normalized webhook fact that
// cannot be attached safely is durably recorded (the documented
// ErrPaymentNeedsReview class) with its provider money, and the proposed
// order is quarantined in the same transaction.
// Feature: shop_bot, Property 2: Round-trip хранилища данных
// Validates: Requirements 12.5, 9.3
func TestRecordPaymentAnomalyAcceptsStripe(t *testing.T) {
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
		 VALUES (1, 'Gadget', 1999.00, 0, 10, 1)`); err != nil {
		t.Fatalf("seed product: %v", err)
	}

	order := &Order{
		UserID:        42,
		TotalUSD:      1999.00,
		PaymentMethod: PaymentMethodStripe,
		Status:        OrderStatusPending,
	}
	orderID, err := store.CreateOrder(ctx, order, []OrderItem{{ProductID: 1, Quantity: 1, PriceUSD: 1999.00}})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	anomaly := PaymentAnomaly{
		ProposedOrderID: orderID,
		Provider:        PaymentMethodStripe,
		ExternalID:      "cs_test_2",
		AmountMinor:     100,
		Currency:        "USD",
		Scale:           2,
		RawAmount:       "1.00",
		Reason:          "amount_mismatch",
	}
	if err := store.RecordPaymentAnomaly(ctx, anomaly); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("record stripe anomaly: err=%v, want ErrPaymentNeedsReview", err)
	}

	var provider, externalID, currency, rawAmount, reason string
	var amountMinor int64
	var scale int
	if err := db.Conn().QueryRowContext(ctx,
		`SELECT provider, external_id, amount_minor, currency, scale, raw_amount, reason
		 FROM payment_anomalies WHERE provider = ? AND external_id = ?`,
		PaymentMethodStripe, "cs_test_2").Scan(
		&provider, &externalID, &amountMinor, &currency, &scale, &rawAmount, &reason); err != nil {
		t.Fatalf("load stripe anomaly: %v", err)
	}
	if provider != PaymentMethodStripe || externalID != "cs_test_2" || amountMinor != 100 ||
		currency != "USD" || scale != 2 || rawAmount != "1.00" || reason != "amount_mismatch" {
		t.Fatalf("stripe anomaly: provider=%s id=%s amount=%d currency=%s scale=%d raw=%q reason=%q",
			provider, externalID, amountMinor, currency, scale, rawAmount, reason)
	}

	// Quarantine is part of the record contract: the proposed order moves to
	// needs_review so the webhook path cannot silently drop the fact.
	var paymentState string
	if err := db.Conn().QueryRowContext(ctx,
		`SELECT payment_state FROM orders WHERE id = ?`, orderID).Scan(&paymentState); err != nil {
		t.Fatal(err)
	}
	if paymentState != PaymentStateNeedsReview {
		t.Fatalf("quarantined order payment_state=%s, want %s", paymentState, PaymentStateNeedsReview)
	}
}
