package service

import (
	"math"
	"testing"
)

func TestConvertUSDToRUB(t *testing.T) {
	s := NewExchangeService(50, 92.5, 5.13)

	tests := []struct {
		name      string
		amountUSD float64
		want      float64
	}{
		{name: "19.99 USD at 92.5", amountUSD: 19.99, want: 1849.08},
		{name: "zero amount", amountUSD: 0, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.ConvertUSDToRUB(tt.amountUSD); got != tt.want {
				t.Errorf("ConvertUSDToRUB(%v) = %v, want %v", tt.amountUSD, got, tt.want)
			}
		})
	}

	// Rate 0 (RUB disabled) -> 0 for any input.
	disabled := NewExchangeService(50, 0, 0)
	for _, amount := range []float64{19.99, 0.01, 100, 0} {
		if got := disabled.ConvertUSDToRUB(amount); got != 0 {
			t.Errorf("ConvertUSDToRUB(%v) with rate 0 = %v, want 0", amount, got)
		}
	}

	// Float-noise guard: 2.675 with rate 100 must land on exactly 267.50
	// (math.Round(x*100)/100, never FormatFloat chains).
	exact := NewExchangeService(50, 100, 0)
	if got := exact.ConvertUSDToRUB(2.675); got != 267.50 {
		t.Errorf("ConvertUSDToRUB(2.675) with rate 100 = %v, want 267.50", got)
	}
}

func TestConvertUSDToNanoTON(t *testing.T) {
	// Pins computed with the exact Go expression
	// int64(math.Round(usd * 1e9 / usdPerTon)) — e.g. 19.99*1e9/5.13 is the
	// float64 3896686159.8440547, which rounds to 3896686160.
	tests := []struct {
		name      string
		usd       float64
		usdPerTon float64
		want      int64
	}{
		{name: "10 USD at 5.0", usd: 10, usdPerTon: 5.0, want: 2000000000},
		{name: "19.99 USD at 5.13", usd: 19.99, usdPerTon: 5.13, want: 3896686160},
		{name: "0.01 USD at 5.13", usd: 0.01, usdPerTon: 5.13, want: 1949318},
		{name: "1 USD at 2.5", usd: 1, usdPerTon: 2.5, want: 400000000},
		{name: "zero amount", usd: 0, usdPerTon: 5.13, want: 0},
		{name: "negative amount", usd: -1, usdPerTon: 5.13, want: 0},
		{name: "zero rate", usd: 10, usdPerTon: 0, want: 0},
		{name: "negative rate", usd: 10, usdPerTon: -5.13, want: 0},
		{name: "NaN amount", usd: math.NaN(), usdPerTon: 5.13, want: 0},
		{name: "NaN rate", usd: 10, usdPerTon: math.NaN(), want: 0},
		{name: "Inf amount", usd: math.Inf(1), usdPerTon: 5.13, want: 0},
		{name: "Inf rate", usd: 10, usdPerTon: math.Inf(1), want: 0},
		// Finite but absurd: usd*1e9 overflows to +Inf before the result
		// guard can run, and int64(+Inf) is platform garbage, not 0.
		{name: "overflowing product", usd: 1e300, usdPerTon: 5.13, want: 0},
		// Finite operands AND a finite product (1e200*1e9 = 1e209 passes the
		// product guard), but the quotient overflows: 1e209/1e-200 = +Inf, and
		// int64(+Inf) is platform garbage, not 0. No real rate configuration
		// does this; the guard is defense against absurd operator configs.
		{name: "overflowing quotient", usd: 1e200, usdPerTon: 1e-200, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ConvertUSDToNanoTON(tt.usd, tt.usdPerTon); got != tt.want {
				t.Errorf("ConvertUSDToNanoTON(%v, %v) = %d, want %d", tt.usd, tt.usdPerTon, got, tt.want)
			}
		})
	}
}

func TestExchangeServiceConvertUSDToNanoTON(t *testing.T) {
	// The method reads the constructor-configured USD-per-TON rate and
	// delegates to the package-level conversion: 19.99 USD at 5.13 USD/TON
	// is the float64 3896686159.8440547, rounding to 3896686160 nanotons.
	s := NewExchangeService(50, 92.5, 5.13)
	if got := s.ConvertUSDToNanoTON(19.99); got != 3896686160 {
		t.Errorf("ConvertUSDToNanoTON(19.99) = %d, want 3896686160", got)
	}

	// Rate 0 (TON disabled) -> 0 for any input.
	disabled := NewExchangeService(50, 92.5, 0)
	for _, amount := range []float64{19.99, 0.01, 100, 0} {
		if got := disabled.ConvertUSDToNanoTON(amount); got != 0 {
			t.Errorf("ConvertUSDToNanoTON(%v) with rate 0 = %d, want 0", amount, got)
		}
	}
}
