package service

import "testing"

func TestConvertUSDToRUB(t *testing.T) {
	s := NewExchangeService(50, 92.5)

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
	disabled := NewExchangeService(50, 0)
	for _, amount := range []float64{19.99, 0.01, 100, 0} {
		if got := disabled.ConvertUSDToRUB(amount); got != 0 {
			t.Errorf("ConvertUSDToRUB(%v) with rate 0 = %v, want 0", amount, got)
		}
	}

	// Float-noise guard: 2.675 with rate 100 must land on exactly 267.50
	// (math.Round(x*100)/100, never FormatFloat chains).
	exact := NewExchangeService(50, 100)
	if got := exact.ConvertUSDToRUB(2.675); got != 267.50 {
		t.Errorf("ConvertUSDToRUB(2.675) with rate 100 = %v, want 267.50", got)
	}
}
