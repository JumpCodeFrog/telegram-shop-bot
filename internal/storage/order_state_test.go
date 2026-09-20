package storage

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

// TestOrderMoneyYooKassa verifies the ledger money mapping for RUB card
// payments: kopecks (minor units), the RUB currency code, decimal scale 2,
// and the fail-closed guards for missing or non-finite totals.
func TestOrderMoneyYooKassa(t *testing.T) {
	tests := []struct {
		name         string
		order        Order
		wantAmount   int64
		wantCurrency string
		wantScale    int
		wantErr      error
	}{
		{
			name:         "yookassa amount converts to kopecks",
			order:        Order{TotalRUB: 1849.08},
			wantAmount:   184908,
			wantCurrency: "RUB",
			wantScale:    2,
		},
		{
			name:    "zero total",
			order:   Order{},
			wantErr: ErrInvalidMoney,
		},
		{
			name:    "not a number",
			order:   Order{TotalRUB: math.NaN()},
			wantErr: ErrInvalidMoney,
		},
		{
			name:    "positive infinity",
			order:   Order{TotalRUB: math.Inf(1)},
			wantErr: ErrInvalidMoney,
		},
	}
	for _, tc := range tests {
		amount, currency, scale, err := orderMoney(tc.order, PaymentMethodYooKassa)
		if !errors.Is(err, tc.wantErr) {
			t.Fatalf("%s: err=%v, want %v", tc.name, err, tc.wantErr)
		}
		if tc.wantErr == nil && (amount != tc.wantAmount || currency != tc.wantCurrency || scale != tc.wantScale) {
			t.Fatalf("%s: got (%d, %q, %d), want (%d, %q, %d)",
				tc.name, amount, currency, scale, tc.wantAmount, tc.wantCurrency, tc.wantScale)
		}
	}

	// Unknown providers keep the unsupported-provider error, not a money one.
	if _, _, _, err := orderMoney(Order{TotalRUB: 1849.08}, "sepa"); err == nil || errors.Is(err, ErrInvalidMoney) {
		t.Fatalf("unknown provider: err=%v, want unsupported-provider error", err)
	}
}

// TestNormalizePaymentProviderYooKassa pins the provider identity used by the
// immutable ledger: the constant passes through normalization unchanged.
func TestNormalizePaymentProviderYooKassa(t *testing.T) {
	if got := normalizePaymentProvider(PaymentMethodYooKassa); got != PaymentMethodYooKassa {
		t.Fatalf("normalizePaymentProvider(%q)=%q, want unchanged", PaymentMethodYooKassa, got)
	}
}

// TestOrderMoneyStripe verifies the ledger money mapping for USD card
// payments through Stripe: cents (minor units), the USD currency code,
// decimal scale 2, and the fail-closed guards for missing or non-finite
// totals. The guards mirror the yookassa legs exactly, with TotalUSD as the
// source amount.
func TestOrderMoneyStripe(t *testing.T) {
	tests := []struct {
		name         string
		order        Order
		wantAmount   int64
		wantCurrency string
		wantScale    int
		wantErr      error
	}{
		{
			name:         "stripe amount converts to cents",
			order:        Order{TotalUSD: 1999.00},
			wantAmount:   199900,
			wantCurrency: "USD",
			wantScale:    2,
		},
		{
			name:    "zero total",
			order:   Order{},
			wantErr: ErrInvalidMoney,
		},
		{
			name:    "negative total",
			order:   Order{TotalUSD: -1},
			wantErr: ErrInvalidMoney,
		},
		{
			name:    "not a number",
			order:   Order{TotalUSD: math.NaN()},
			wantErr: ErrInvalidMoney,
		},
		{
			name:    "positive infinity",
			order:   Order{TotalUSD: math.Inf(1)},
			wantErr: ErrInvalidMoney,
		},
	}
	for _, tc := range tests {
		amount, currency, scale, err := orderMoney(tc.order, PaymentMethodStripe)
		if !errors.Is(err, tc.wantErr) {
			t.Fatalf("%s: err=%v, want %v", tc.name, err, tc.wantErr)
		}
		if tc.wantErr == nil && (amount != tc.wantAmount || currency != tc.wantCurrency || scale != tc.wantScale) {
			t.Fatalf("%s: got (%d, %q, %d), want (%d, %q, %d)",
				tc.name, amount, currency, scale, tc.wantAmount, tc.wantCurrency, tc.wantScale)
		}
	}
}

// TestNormalizePaymentProviderStripe pins the provider identity used by the
// immutable ledger: the constant passes through normalization unchanged, and
// the case-fold/trim passthrough maps operator spelling onto it.
func TestNormalizePaymentProviderStripe(t *testing.T) {
	if got := normalizePaymentProvider(PaymentMethodStripe); got != PaymentMethodStripe {
		t.Fatalf("normalizePaymentProvider(%q)=%q, want unchanged", PaymentMethodStripe, got)
	}
	if got := normalizePaymentProvider(" Stripe "); got != PaymentMethodStripe {
		t.Fatalf("normalizePaymentProvider(%q)=%q, want %q", " Stripe ", got, PaymentMethodStripe)
	}
}

// TestUpdateOrderStatusWithPaymentFactYooKassa proves the app-level receipt
// gate accepts the YooKassa provider identity the Task 3 money mapping
// already produces. A RUB fact in kopecks (1849.08 -> 184908, scale 2)
// settles a pending order through UpdateOrderStatusWithPaymentFact; a
// wrong-currency or wrong-amount fact fails closed with
// ErrPaymentReceiptMismatch while the order stays pending; an exact replay
// is idempotent exactly like the crypto path: the state CAS rejects it with
// ErrOrderStatusConflict and no duplicate ledger or anomaly rows appear.
// Feature: shop_bot, Property 2: Round-trip хранилища данных
// Validates: Requirements 12.5, 9.3
func TestUpdateOrderStatusWithPaymentFactYooKassa(t *testing.T) {
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

	occurred := time.Unix(1_700_000_000, 0).UTC()
	fact := PaymentFact{
		Provider:    PaymentMethodYooKassa,
		ExternalID:  "pay_1",
		Currency:    "RUB",
		AmountMinor: 184908,
		Scale:       2,
		OccurredAt:  occurred,
	}

	// A wrong-currency fact fails closed before anything is written.
	wrongCurrency := fact
	wrongCurrency.Currency = "USD"
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, wrongCurrency); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("wrong currency: err=%v, want ErrPaymentReceiptMismatch", err)
	}
	// A wrong-amount fact fails closed the same way.
	wrongAmount := fact
	wrongAmount.AmountMinor = 184907
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, wrongAmount); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("wrong amount: err=%v, want ErrPaymentReceiptMismatch", err)
	}
	var status string
	if err := db.Conn().QueryRowContext(ctx, `SELECT status FROM orders WHERE id = ?`, orderID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err := db.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM payment_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if status != OrderStatusPending || attempts != 0 {
		t.Fatalf("rejected facts mutated state: status=%s attempts=%d", status, attempts)
	}

	// The validated RUB fact settles the order.
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, fact); err != nil {
		t.Fatalf("yookassa fact settlement: %v", err)
	}
	var paymentMethod, paymentID, paymentState string
	if err := db.Conn().QueryRowContext(ctx,
		`SELECT status, payment_method, payment_id, payment_state FROM orders WHERE id = ?`, orderID).
		Scan(&status, &paymentMethod, &paymentID, &paymentState); err != nil {
		t.Fatal(err)
	}
	if status != OrderStatusPaid || paymentMethod != PaymentMethodYooKassa || paymentID != "pay_1" || paymentState != PaymentStateSettled {
		t.Fatalf("settled order: status=%s method=%s id=%s state=%s", status, paymentMethod, paymentID, paymentState)
	}

	// The immutable ledger pins the provider fact money, not the order's
	// accounting currency: kopecks, RUB, scale 2, payer 0 (YooKassa provides
	// no Telegram payer id).
	var attemptOrderID, attemptPayerID, amountMinor int64
	var provider, currency, attemptStatus string
	var scale int
	if err := db.Conn().QueryRowContext(ctx,
		`SELECT order_id, provider, external_id, payer_id, amount_minor, currency, scale, status
		 FROM payment_attempts WHERE external_id = 'pay_1'`).Scan(
		&attemptOrderID, &provider, new(string), &attemptPayerID, &amountMinor,
		&currency, &scale, &attemptStatus); err != nil {
		t.Fatalf("load settled attempt: %v", err)
	}
	if attemptOrderID != orderID || provider != PaymentMethodYooKassa || attemptPayerID != 0 ||
		amountMinor != 184908 || currency != "RUB" || scale != 2 || attemptStatus != "succeeded" {
		t.Fatalf("settled attempt: order=%d provider=%s payer=%d amount=%d currency=%s scale=%d status=%s",
			attemptOrderID, provider, attemptPayerID, amountMinor, currency, scale, attemptStatus)
	}

	// An exact replay is idempotent, mirroring the crypto settlement replay:
	// the state CAS rejects it and no duplicate ledger rows or anomalies appear.
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, fact); !errors.Is(err, ErrOrderStatusConflict) {
		t.Fatalf("replayed settlement: err=%v, want ErrOrderStatusConflict", err)
	}
	// The replay leaves the settled payment_state untouched.
	var replayState string
	if err := db.Conn().QueryRowContext(ctx, `SELECT payment_state FROM orders WHERE id = ?`, orderID).Scan(&replayState); err != nil {
		t.Fatal(err)
	}
	if replayState != PaymentStateSettled {
		t.Fatalf("payment_state after replay = %s, want %s", replayState, PaymentStateSettled)
	}
	for _, count := range []struct {
		query string
		want  int
	}{
		{`SELECT COUNT(*) FROM payment_attempts WHERE provider = 'yookassa'`, 1},
		{`SELECT COUNT(*) FROM payment_events WHERE provider = 'yookassa'`, 1},
		{`SELECT COUNT(*) FROM payment_anomalies`, 0},
	} {
		var n int
		if err := db.Conn().QueryRowContext(ctx, count.query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != count.want {
			t.Fatalf("replay row count: %s -> %d, want %d", count.query, n, count.want)
		}
	}
}

// TestUpdateOrderStatusWithPaymentFactStripe proves the app-level receipt
// gate accepts the Stripe provider identity the Task 3 money mapping
// produces. A USD fact in cents (1999.00 -> 199900, scale 2) settles a
// pending order through UpdateOrderStatusWithPaymentFact; a wrong-currency
// or wrong-amount fact fails closed with ErrPaymentReceiptMismatch while the
// order stays pending; an exact replay is idempotent exactly like the crypto
// and yookassa paths: the state CAS rejects it with ErrOrderStatusConflict
// and no duplicate ledger or anomaly rows appear.
// Feature: shop_bot, Property 2: Round-trip хранилища данных
// Validates: Requirements 12.5, 9.3
func TestUpdateOrderStatusWithPaymentFactStripe(t *testing.T) {
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

	occurred := time.Unix(1_700_000_000, 0).UTC()
	fact := PaymentFact{
		Provider:    PaymentMethodStripe,
		ExternalID:  "cs_test_1",
		Currency:    "USD",
		AmountMinor: 199900,
		Scale:       2,
		OccurredAt:  occurred,
	}

	// A wrong-currency fact fails closed before anything is written.
	wrongCurrency := fact
	wrongCurrency.Currency = "RUB"
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, wrongCurrency); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("wrong currency: err=%v, want ErrPaymentReceiptMismatch", err)
	}
	// A wrong-amount fact fails closed the same way.
	wrongAmount := fact
	wrongAmount.AmountMinor = 199899
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, wrongAmount); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("wrong amount: err=%v, want ErrPaymentReceiptMismatch", err)
	}
	var status string
	if err := db.Conn().QueryRowContext(ctx, `SELECT status FROM orders WHERE id = ?`, orderID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err := db.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM payment_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if status != OrderStatusPending || attempts != 0 {
		t.Fatalf("rejected facts mutated state: status=%s attempts=%d", status, attempts)
	}

	// The validated USD fact settles the order.
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, fact); err != nil {
		t.Fatalf("stripe fact settlement: %v", err)
	}
	var paymentMethod, paymentID, paymentState string
	if err := db.Conn().QueryRowContext(ctx,
		`SELECT status, payment_method, payment_id, payment_state FROM orders WHERE id = ?`, orderID).
		Scan(&status, &paymentMethod, &paymentID, &paymentState); err != nil {
		t.Fatal(err)
	}
	if status != OrderStatusPaid || paymentMethod != PaymentMethodStripe || paymentID != "cs_test_1" || paymentState != PaymentStateSettled {
		t.Fatalf("settled order: status=%s method=%s id=%s state=%s", status, paymentMethod, paymentID, paymentState)
	}

	// The immutable ledger pins the provider fact money: cents, USD, scale 2,
	// payer 0 (Stripe provides no Telegram payer id).
	var attemptOrderID, attemptPayerID, amountMinor int64
	var provider, currency, attemptStatus string
	var scale int
	if err := db.Conn().QueryRowContext(ctx,
		`SELECT order_id, provider, external_id, payer_id, amount_minor, currency, scale, status
		 FROM payment_attempts WHERE external_id = 'cs_test_1'`).Scan(
		&attemptOrderID, &provider, new(string), &attemptPayerID, &amountMinor,
		&currency, &scale, &attemptStatus); err != nil {
		t.Fatalf("load settled attempt: %v", err)
	}
	if attemptOrderID != orderID || provider != PaymentMethodStripe || attemptPayerID != 0 ||
		amountMinor != 199900 || currency != "USD" || scale != 2 || attemptStatus != "succeeded" {
		t.Fatalf("settled attempt: order=%d provider=%s payer=%d amount=%d currency=%s scale=%d status=%s",
			attemptOrderID, provider, attemptPayerID, amountMinor, currency, scale, attemptStatus)
	}

	// An exact replay is idempotent, mirroring the yookassa settlement replay:
	// the state CAS rejects it and no duplicate ledger rows or anomalies appear.
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, fact); !errors.Is(err, ErrOrderStatusConflict) {
		t.Fatalf("replayed settlement: err=%v, want ErrOrderStatusConflict", err)
	}
	// The replay leaves the settled payment_state untouched.
	var replayState string
	if err := db.Conn().QueryRowContext(ctx, `SELECT payment_state FROM orders WHERE id = ?`, orderID).Scan(&replayState); err != nil {
		t.Fatal(err)
	}
	if replayState != PaymentStateSettled {
		t.Fatalf("payment_state after replay = %s, want %s", replayState, PaymentStateSettled)
	}
	for _, count := range []struct {
		query string
		want  int
	}{
		{`SELECT COUNT(*) FROM payment_attempts WHERE provider = 'stripe'`, 1},
		{`SELECT COUNT(*) FROM payment_events WHERE provider = 'stripe'`, 1},
		{`SELECT COUNT(*) FROM payment_anomalies`, 0},
	} {
		var n int
		if err := db.Conn().QueryRowContext(ctx, count.query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != count.want {
			t.Fatalf("replay row count: %s -> %d, want %d", count.query, n, count.want)
		}
	}
}

// TestOrderMoneyTON verifies the ledger money mapping for on-chain TON
// payments: TotalTonNano is already an integer nanoton count, so it maps
// directly (no float rounding, no NaN/Inf guards — those apply only to the
// float-priced rails), with the TON currency code and scale 9. The <= 0
// guard suffices for the int64 column.
func TestOrderMoneyTON(t *testing.T) {
	tests := []struct {
		name         string
		order        Order
		wantAmount   int64
		wantCurrency string
		wantScale    int
		wantErr      error
	}{
		{
			name:         "ton total passes through as nanoton",
			order:        Order{TotalTonNano: 1_500_000_000},
			wantAmount:   1_500_000_000,
			wantCurrency: "TON",
			wantScale:    9,
		},
		{
			name:         "single nanoton",
			order:        Order{TotalTonNano: 1},
			wantAmount:   1,
			wantCurrency: "TON",
			wantScale:    9,
		},
		{
			name:    "zero total",
			order:   Order{},
			wantErr: ErrInvalidMoney,
		},
		{
			name:    "negative total",
			order:   Order{TotalTonNano: -1},
			wantErr: ErrInvalidMoney,
		},
	}
	for _, tc := range tests {
		amount, currency, scale, err := orderMoney(tc.order, PaymentMethodTON)
		if !errors.Is(err, tc.wantErr) {
			t.Fatalf("%s: err=%v, want %v", tc.name, err, tc.wantErr)
		}
		if tc.wantErr == nil && (amount != tc.wantAmount || currency != tc.wantCurrency || scale != tc.wantScale) {
			t.Fatalf("%s: got (%d, %q, %d), want (%d, %q, %d)",
				tc.name, amount, currency, scale, tc.wantAmount, tc.wantCurrency, tc.wantScale)
		}
	}
}

// TestNormalizePaymentProviderTON pins the provider identity used by the
// immutable ledger: the constant passes through normalization unchanged, and
// the case-fold/trim passthrough maps operator spelling onto it.
func TestNormalizePaymentProviderTON(t *testing.T) {
	if got := normalizePaymentProvider(PaymentMethodTON); got != PaymentMethodTON {
		t.Fatalf("normalizePaymentProvider(%q)=%q, want unchanged", PaymentMethodTON, got)
	}
	if got := normalizePaymentProvider(" TON "); got != PaymentMethodTON {
		t.Fatalf("normalizePaymentProvider(%q)=%q, want %q", " TON ", got, PaymentMethodTON)
	}
}

// TestOrderMoneyNowpayments verifies the ledger money mapping for
// NOWPayments invoices: USD cents (minor units) derived from TotalUSD, the
// USD currency code, decimal scale 2, and the fail-closed guards for missing
// or non-finite totals. The guards mirror the stripe legs exactly.
func TestOrderMoneyNowpayments(t *testing.T) {
	tests := []struct {
		name         string
		order        Order
		wantAmount   int64
		wantCurrency string
		wantScale    int
		wantErr      error
	}{
		{
			name:         "nowpayments amount converts to cents",
			order:        Order{TotalUSD: 1999.00},
			wantAmount:   199900,
			wantCurrency: "USD",
			wantScale:    2,
		},
		{
			name:    "zero total",
			order:   Order{},
			wantErr: ErrInvalidMoney,
		},
		{
			name:    "negative total",
			order:   Order{TotalUSD: -1},
			wantErr: ErrInvalidMoney,
		},
		{
			name:    "not a number",
			order:   Order{TotalUSD: math.NaN()},
			wantErr: ErrInvalidMoney,
		},
		{
			name:    "positive infinity",
			order:   Order{TotalUSD: math.Inf(1)},
			wantErr: ErrInvalidMoney,
		},
	}
	for _, tc := range tests {
		amount, currency, scale, err := orderMoney(tc.order, PaymentMethodNowpayments)
		if !errors.Is(err, tc.wantErr) {
			t.Fatalf("%s: err=%v, want %v", tc.name, err, tc.wantErr)
		}
		if tc.wantErr == nil && (amount != tc.wantAmount || currency != tc.wantCurrency || scale != tc.wantScale) {
			t.Fatalf("%s: got (%d, %q, %d), want (%d, %q, %d)",
				tc.name, amount, currency, scale, tc.wantAmount, tc.wantCurrency, tc.wantScale)
		}
	}
}

// TestNormalizePaymentProviderNowpayments pins the provider identity used by
// the immutable ledger: the constant passes through normalization unchanged,
// and the case-fold/trim passthrough maps operator spelling onto it.
func TestNormalizePaymentProviderNowpayments(t *testing.T) {
	if got := normalizePaymentProvider(PaymentMethodNowpayments); got != PaymentMethodNowpayments {
		t.Fatalf("normalizePaymentProvider(%q)=%q, want unchanged", PaymentMethodNowpayments, got)
	}
	if got := normalizePaymentProvider(" NowPayments "); got != PaymentMethodNowpayments {
		t.Fatalf("normalizePaymentProvider(%q)=%q, want %q", " NowPayments ", got, PaymentMethodNowpayments)
	}
}

// The balance provider's money mapping is now accepted app-side; its
// acceptance legs (USD cents at scale 2, invalid-total rejection) live in
// balance_acceptance_test.go: TestOrderMoneyBalance.

// TestValidatePaymentFactTON pins the ton fact gate: currency and scale are
// exact, no payer check applies (on-chain transfers carry no Telegram
// identity), and the overpay-tolerant amount rule is enforced HERE, at the
// single-source storage gate: a ton fact must carry at least the order's
// nanoton snapshot (>=), so an overpaying fact passes and an underpaying
// fact is rejected for every caller. The receipt layer
// (ConfirmPaymentReceipt) keeps its own >= check as defense-in-depth.
func TestValidatePaymentFactTON(t *testing.T) {
	order := Order{UserID: 42, TotalTonNano: 1_500_000_000}
	fact := PaymentFact{
		Provider: PaymentMethodTON, ExternalID: "ton-tx-1",
		PayerID: 0, AmountMinor: 1_500_000_000, Currency: "TON", Scale: 9,
	}
	validated, err := validatePaymentFact(order, fact)
	if err != nil {
		t.Fatalf("validate ton fact: %v", err)
	}
	if validated.Provider != PaymentMethodTON {
		t.Fatalf("provider=%q, want %q", validated.Provider, PaymentMethodTON)
	}

	// Overpay tolerance: the ton gate enforces >= the frozen snapshot, so
	// a larger-than-order fact (real money received on-chain) passes.
	overpay := fact
	overpay.AmountMinor = 2_000_000_000
	if _, err := validatePaymentFact(order, overpay); err != nil {
		t.Fatalf("overpaying ton fact must pass the fact gate: %v", err)
	}

	// Underpay is rejected at the fact gate: the former split — exactness
	// skipped here, >= enforced only at the receipt layer — is closed, so
	// EVERY caller of the storage gate rejects an underpaying ton fact.
	underpay := fact
	underpay.AmountMinor = 1_499_999_999
	if _, err := validatePaymentFact(order, underpay); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("underpaying ton fact: err=%v, want ErrPaymentReceiptMismatch", err)
	}

	wrongCurrency := fact
	wrongCurrency.Currency = "USDT"
	if _, err := validatePaymentFact(order, wrongCurrency); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("wrong currency: err=%v, want ErrPaymentReceiptMismatch", err)
	}
	wrongScale := fact
	wrongScale.Scale = 2
	if _, err := validatePaymentFact(order, wrongScale); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("wrong scale: err=%v, want ErrPaymentReceiptMismatch", err)
	}
	noExternalID := fact
	noExternalID.ExternalID = ""
	if _, err := validatePaymentFact(order, noExternalID); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("missing external id: err=%v, want ErrPaymentReceiptMismatch", err)
	}
}

// TestValidatePaymentFactNowpayments pins the nowpayments fact gate: USD
// currency, scale 2 and exact minor-unit equality against the order total,
// with no payer check (NOWPayments carries no Telegram payer identity),
// mirroring the stripe case.
func TestValidatePaymentFactNowpayments(t *testing.T) {
	order := Order{UserID: 42, TotalUSD: 1999.00}
	fact := PaymentFact{
		Provider: PaymentMethodNowpayments, ExternalID: "np-1",
		PayerID: 0, AmountMinor: 199900, Currency: "USD", Scale: 2,
	}
	if _, err := validatePaymentFact(order, fact); err != nil {
		t.Fatalf("validate nowpayments fact: %v", err)
	}
	wrongCurrency := fact
	wrongCurrency.Currency = "EUR"
	if _, err := validatePaymentFact(order, wrongCurrency); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("wrong currency: err=%v, want ErrPaymentReceiptMismatch", err)
	}
	wrongAmount := fact
	wrongAmount.AmountMinor = 199899
	if _, err := validatePaymentFact(order, wrongAmount); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("wrong amount: err=%v, want ErrPaymentReceiptMismatch", err)
	}
}

// The balance fact gate is now accepted app-side; its acceptance legs
// (required positive payer equal to the order user, USD/scale-2 money,
// exact amount) live in balance_acceptance_test.go: TestValidatePaymentFactBalance.

// TestUpdateOrderStatusWithPaymentFactTON proves the app-level receipt gate
// accepts the TON provider identity. A TON fact in nanoton
// (1.5 TON -> 1500000000, scale 9) settles a pending order through
// UpdateOrderStatusWithPaymentFact; a wrong-currency or wrong-scale fact
// fails closed with ErrPaymentReceiptMismatch while the order stays pending;
// an exact replay is idempotent exactly like the stripe path: the state CAS
// rejects it with ErrOrderStatusConflict and no duplicate ledger or anomaly
// rows appear.
// Feature: shop_bot, Property 2: Round-trip хранилища данных
// Validates: Requirements 12.5, 9.3
func TestUpdateOrderStatusWithPaymentFactTON(t *testing.T) {
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
		 VALUES (1, 'Gadget', 0, 0, 10, 1)`); err != nil {
		t.Fatalf("seed product: %v", err)
	}

	order := &Order{
		UserID:        42,
		TotalTonNano:  1_500_000_000,
		PaymentMethod: PaymentMethodTON,
		Status:        OrderStatusPending,
	}
	orderID, err := store.CreateOrder(ctx, order, []OrderItem{{ProductID: 1, Quantity: 1, PriceUSD: 0}})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	occurred := time.Unix(1_700_000_000, 0).UTC()
	fact := PaymentFact{
		Provider:    PaymentMethodTON,
		ExternalID:  "ton-tx-hash-1",
		Currency:    "TON",
		AmountMinor: 1_500_000_000,
		Scale:       9,
		OccurredAt:  occurred,
	}

	// A wrong-currency fact fails closed before anything is written.
	wrongCurrency := fact
	wrongCurrency.Currency = "USD"
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, wrongCurrency); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("wrong currency: err=%v, want ErrPaymentReceiptMismatch", err)
	}
	// A wrong-scale fact fails closed the same way.
	wrongScale := fact
	wrongScale.Scale = 2
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, wrongScale); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("wrong scale: err=%v, want ErrPaymentReceiptMismatch", err)
	}
	var status string
	if err := db.Conn().QueryRowContext(ctx, `SELECT status FROM orders WHERE id = ?`, orderID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err := db.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM payment_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if status != OrderStatusPending || attempts != 0 {
		t.Fatalf("rejected facts mutated state: status=%s attempts=%d", status, attempts)
	}

	// The validated TON fact settles the order.
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, fact); err != nil {
		t.Fatalf("ton fact settlement: %v", err)
	}
	var paymentMethod, paymentID, paymentState string
	if err := db.Conn().QueryRowContext(ctx,
		`SELECT status, payment_method, payment_id, payment_state FROM orders WHERE id = ?`, orderID).
		Scan(&status, &paymentMethod, &paymentID, &paymentState); err != nil {
		t.Fatal(err)
	}
	if status != OrderStatusPaid || paymentMethod != PaymentMethodTON || paymentID != "ton-tx-hash-1" || paymentState != PaymentStateSettled {
		t.Fatalf("settled order: status=%s method=%s id=%s state=%s", status, paymentMethod, paymentID, paymentState)
	}

	// The immutable ledger pins the provider fact money: nanoton, TON,
	// scale 9, payer 0 (on-chain transfers carry no Telegram payer id).
	var attemptOrderID, attemptPayerID, amountMinor int64
	var provider, currency, attemptStatus string
	var scale int
	if err := db.Conn().QueryRowContext(ctx,
		`SELECT order_id, provider, external_id, payer_id, amount_minor, currency, scale, status
		 FROM payment_attempts WHERE external_id = 'ton-tx-hash-1'`).Scan(
		&attemptOrderID, &provider, new(string), &attemptPayerID, &amountMinor,
		&currency, &scale, &attemptStatus); err != nil {
		t.Fatalf("load settled attempt: %v", err)
	}
	if attemptOrderID != orderID || provider != PaymentMethodTON || attemptPayerID != 0 ||
		amountMinor != 1_500_000_000 || currency != "TON" || scale != 9 || attemptStatus != "succeeded" {
		t.Fatalf("settled attempt: order=%d provider=%s payer=%d amount=%d currency=%s scale=%d status=%s",
			attemptOrderID, provider, attemptPayerID, amountMinor, currency, scale, attemptStatus)
	}

	// An exact replay is idempotent, mirroring the stripe settlement replay:
	// the state CAS rejects it and no duplicate ledger rows or anomalies appear.
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, fact); !errors.Is(err, ErrOrderStatusConflict) {
		t.Fatalf("replayed settlement: err=%v, want ErrOrderStatusConflict", err)
	}
	// The replay leaves the settled payment_state untouched.
	var replayState string
	if err := db.Conn().QueryRowContext(ctx, `SELECT payment_state FROM orders WHERE id = ?`, orderID).Scan(&replayState); err != nil {
		t.Fatal(err)
	}
	if replayState != PaymentStateSettled {
		t.Fatalf("payment_state after replay = %s, want %s", replayState, PaymentStateSettled)
	}
	for _, count := range []struct {
		query string
		want  int
	}{
		{`SELECT COUNT(*) FROM payment_attempts WHERE provider = 'ton'`, 1},
		{`SELECT COUNT(*) FROM payment_events WHERE provider = 'ton'`, 1},
		{`SELECT COUNT(*) FROM payment_anomalies`, 0},
	} {
		var n int
		if err := db.Conn().QueryRowContext(ctx, count.query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != count.want {
			t.Fatalf("replay row count: %s -> %d, want %d", count.query, n, count.want)
		}
	}
}

// TestUpdateOrderStatusWithPaymentFactNowpayments proves the app-level
// receipt gate accepts the NOWPayments provider identity. A USD fact in
// cents (1999.00 -> 199900, scale 2) settles a pending order through
// UpdateOrderStatusWithPaymentFact; a wrong-currency or wrong-amount fact
// fails closed with ErrPaymentReceiptMismatch while the order stays pending;
// an exact replay is idempotent exactly like the stripe path.
// Feature: shop_bot, Property 2: Round-trip хранилища данных
// Validates: Requirements 12.5, 9.3
func TestUpdateOrderStatusWithPaymentFactNowpayments(t *testing.T) {
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
		PaymentMethod: PaymentMethodNowpayments,
		Status:        OrderStatusPending,
	}
	orderID, err := store.CreateOrder(ctx, order, []OrderItem{{ProductID: 1, Quantity: 1, PriceUSD: 1999.00}})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	occurred := time.Unix(1_700_000_000, 0).UTC()
	fact := PaymentFact{
		Provider:    PaymentMethodNowpayments,
		ExternalID:  "np-payment-1",
		Currency:    "USD",
		AmountMinor: 199900,
		Scale:       2,
		OccurredAt:  occurred,
	}

	// A wrong-currency fact fails closed before anything is written.
	wrongCurrency := fact
	wrongCurrency.Currency = "RUB"
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, wrongCurrency); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("wrong currency: err=%v, want ErrPaymentReceiptMismatch", err)
	}
	// A wrong-amount fact fails closed the same way.
	wrongAmount := fact
	wrongAmount.AmountMinor = 199899
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, wrongAmount); !errors.Is(err, ErrPaymentReceiptMismatch) {
		t.Fatalf("wrong amount: err=%v, want ErrPaymentReceiptMismatch", err)
	}
	var status string
	if err := db.Conn().QueryRowContext(ctx, `SELECT status FROM orders WHERE id = ?`, orderID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err := db.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM payment_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if status != OrderStatusPending || attempts != 0 {
		t.Fatalf("rejected facts mutated state: status=%s attempts=%d", status, attempts)
	}

	// The validated USD fact settles the order.
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, fact); err != nil {
		t.Fatalf("nowpayments fact settlement: %v", err)
	}
	var paymentMethod, paymentID, paymentState string
	if err := db.Conn().QueryRowContext(ctx,
		`SELECT status, payment_method, payment_id, payment_state FROM orders WHERE id = ?`, orderID).
		Scan(&status, &paymentMethod, &paymentID, &paymentState); err != nil {
		t.Fatal(err)
	}
	if status != OrderStatusPaid || paymentMethod != PaymentMethodNowpayments || paymentID != "np-payment-1" || paymentState != PaymentStateSettled {
		t.Fatalf("settled order: status=%s method=%s id=%s state=%s", status, paymentMethod, paymentID, paymentState)
	}

	// The immutable ledger pins the provider fact money: cents, USD, scale 2,
	// payer 0 (NOWPayments provides no Telegram payer id).
	var attemptOrderID, attemptPayerID, amountMinor int64
	var provider, currency, attemptStatus string
	var scale int
	if err := db.Conn().QueryRowContext(ctx,
		`SELECT order_id, provider, external_id, payer_id, amount_minor, currency, scale, status
		 FROM payment_attempts WHERE external_id = 'np-payment-1'`).Scan(
		&attemptOrderID, &provider, new(string), &attemptPayerID, &amountMinor,
		&currency, &scale, &attemptStatus); err != nil {
		t.Fatalf("load settled attempt: %v", err)
	}
	if attemptOrderID != orderID || provider != PaymentMethodNowpayments || attemptPayerID != 0 ||
		amountMinor != 199900 || currency != "USD" || scale != 2 || attemptStatus != "succeeded" {
		t.Fatalf("settled attempt: order=%d provider=%s payer=%d amount=%d currency=%s scale=%d status=%s",
			attemptOrderID, provider, attemptPayerID, amountMinor, currency, scale, attemptStatus)
	}

	// An exact replay is idempotent, mirroring the stripe settlement replay:
	// the state CAS rejects it and no duplicate ledger rows or anomalies appear.
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID, OrderStatusPending, OrderStatusPaid, fact); !errors.Is(err, ErrOrderStatusConflict) {
		t.Fatalf("replayed settlement: err=%v, want ErrOrderStatusConflict", err)
	}
	// The replay leaves the settled payment_state untouched.
	var replayState string
	if err := db.Conn().QueryRowContext(ctx, `SELECT payment_state FROM orders WHERE id = ?`, orderID).Scan(&replayState); err != nil {
		t.Fatal(err)
	}
	if replayState != PaymentStateSettled {
		t.Fatalf("payment_state after replay = %s, want %s", replayState, PaymentStateSettled)
	}
	for _, count := range []struct {
		query string
		want  int
	}{
		{`SELECT COUNT(*) FROM payment_attempts WHERE provider = 'nowpayments'`, 1},
		{`SELECT COUNT(*) FROM payment_events WHERE provider = 'nowpayments'`, 1},
		{`SELECT COUNT(*) FROM payment_anomalies`, 0},
	} {
		var n int
		if err := db.Conn().QueryRowContext(ctx, count.query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != count.want {
			t.Fatalf("replay row count: %s -> %d, want %d", count.query, n, count.want)
		}
	}
}
