package storage

import (
	"errors"
	"math"
	"testing"
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
