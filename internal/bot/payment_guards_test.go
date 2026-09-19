package bot

import (
	"errors"
	"slices"
	"testing"

	"shop_bot/internal/storage"
)

func TestEnsureOrderPayableForUser_AllowsPendingOwnedOrder(t *testing.T) {
	order := &storage.Order{ID: 1, UserID: 42, Status: storage.OrderStatusPending}
	if err := ensureOrderPayableForUser(order, 42); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestEnsureOrderPayableForUser_BlocksForeignOrder(t *testing.T) {
	order := &storage.Order{ID: 1, UserID: 99, Status: storage.OrderStatusPending}
	err := ensureOrderPayableForUser(order, 42)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestEnsureOrderPayableForUser_BlocksNonPendingOrder(t *testing.T) {
	order := &storage.Order{ID: 1, UserID: 42, Status: storage.OrderStatusPaid}
	err := ensureOrderPayableForUser(order, 42)
	if !errors.Is(err, storage.ErrOrderStatusConflict) {
		t.Fatalf("expected ErrOrderStatusConflict, got %v", err)
	}
}

func TestEnsureOrderPayableForUser_BlocksNeedsReviewOrder(t *testing.T) {
	order := &storage.Order{ID: 1, UserID: 42, Status: storage.OrderStatusPending, PaymentState: storage.PaymentStateNeedsReview}
	err := ensureOrderPayableForUser(order, 42)
	if !errors.Is(err, storage.ErrPaymentNeedsReview) {
		t.Fatalf("expected ErrPaymentNeedsReview, got %v", err)
	}
}

func TestEnsureOrderPayableForUser_BlocksNilOrder(t *testing.T) {
	err := ensureOrderPayableForUser(nil, 42)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestHasPendingOrderWithPromo_DetectsPendingMatch(t *testing.T) {
	orders := []storage.Order{
		{ID: 1, Status: storage.OrderStatusPaid, PromoCode: "WELCOME10"},
		{ID: 2, Status: storage.OrderStatusPending, PromoCode: "WELCOME10"},
	}
	if !hasPendingOrderWithPromo(orders, "WELCOME10") {
		t.Fatal("expected pending promo order to be detected")
	}
}

func TestHasPendingOrderWithPromo_IgnoresNonPendingOrOtherPromo(t *testing.T) {
	orders := []storage.Order{
		{ID: 1, Status: storage.OrderStatusPaid, PromoCode: "WELCOME10"},
		{ID: 2, Status: storage.OrderStatusPending, PromoCode: "SPRING5"},
	}
	if hasPendingOrderWithPromo(orders, "WELCOME10") {
		t.Fatal("did not expect pending promo order match")
	}
}

func TestPaymentMethodKeyboard_HidesCryptoWhenDisabled(t *testing.T) {
	keyboard := paymentMethodKeyboard(15, false, false, false, 0, 100, 1.50, "", nil)

	// Stars row + terms/support row + cancel/orders row + menu row
	if len(keyboard) != 4 {
		t.Fatalf("expected 4 rows, got %d", len(keyboard))
	}

	assertPaymentButton(t, keyboard[0][0], "⭐ Pay 100 Stars", "pay:stars:15")
	assertPaymentButton(t, keyboard[1][0], "📄 Terms", "terms")
	assertPaymentButton(t, keyboard[1][1], "🆘 Payment support", "paysupport")
	assertPaymentButton(t, keyboard[2][0], "❌ Cancel order", "order:cancel:15")
}

func TestPaymentMethodKeyboard_ShowsCryptoWhenEnabled(t *testing.T) {
	keyboard := paymentMethodKeyboard(15, true, false, false, 0, 100, 1.50, "", nil)

	// Stars row + crypto row + terms/support row + cancel/orders row + menu row
	if len(keyboard) != 5 {
		t.Fatalf("expected 5 rows, got %d", len(keyboard))
	}

	assertPaymentButton(t, keyboard[0][0], "⭐ Pay 100 Stars", "pay:stars:15")
	assertPaymentButton(t, keyboard[1][0], "💎 Pay $1.50 USDT", "pay:crypto:15")
	assertPaymentButton(t, keyboard[2][0], "📄 Terms", "terms")
	assertPaymentButton(t, keyboard[2][1], "🆘 Payment support", "paysupport")
	assertPaymentButton(t, keyboard[3][0], "❌ Cancel order", "order:cancel:15")
}

// styledCallbacks flattens a StyledKeyboard into its callback data strings in
// display order, mirroring buttonCallbacks in checkout_totals_test.go for the
// tgbotapi-decoded markup.
func styledCallbacks(kb StyledKeyboard) []string {
	var callbacks []string
	for _, row := range kb {
		for _, button := range row {
			if button.CallbackData != "" {
				callbacks = append(callbacks, button.CallbackData)
			}
		}
	}
	return callbacks
}

func TestPaymentMethodKeyboard_StripeDisabledPathInvariance(t *testing.T) {
	// With Stripe disabled the full callback list must be byte-identical to
	// the pre-Stripe row set, whether or not the other rails are offered.
	for _, tc := range []struct {
		name          string
		cryptoEnabled bool
		yookassaOK    bool
		want          []string
	}{
		{
			name: "stars only",
			want: []string{"pay:stars:15", "terms", "paysupport", "order:cancel:15", "back:orders", "back:menu"},
		},
		{
			name:          "crypto and yookassa offered",
			cryptoEnabled: true,
			yookassaOK:    true,
			want:          []string{"pay:stars:15", "pay:crypto:15", "pay:yookassa:15", "terms", "paysupport", "order:cancel:15", "back:orders", "back:menu"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keyboard := paymentMethodKeyboard(15, tc.cryptoEnabled, tc.yookassaOK, false, 1849.08, 100, 19.99, "", nil)
			if got := styledCallbacks(keyboard); !slices.Equal(got, tc.want) {
				t.Fatalf("callbacks = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPaymentMethodKeyboard_ShowsStripeAfterYooKassa(t *testing.T) {
	keyboard := paymentMethodKeyboard(15, true, true, true, 1849.08, 100, 19.99, "", nil)

	// Row order stays stars → crypto → yookassa → stripe → footer rows.
	want := []string{"pay:stars:15", "pay:crypto:15", "pay:yookassa:15", "pay:stripe:15", "terms", "paysupport", "order:cancel:15", "back:orders", "back:menu"}
	if got := styledCallbacks(keyboard); !slices.Equal(got, want) {
		t.Fatalf("callbacks = %v, want %v", got, want)
	}

	assertPaymentButton(t, keyboard[3][0], "💳 Pay $19.99", "pay:stripe:15")
}

func TestPaymentMethodKeyboard_HidesStripeWhenDisabled(t *testing.T) {
	keyboard := paymentMethodKeyboard(15, true, false, false, 0, 100, 1.50, "", nil)

	// Stars row + crypto row + terms/support row + cancel/orders row + menu row
	if len(keyboard) != 5 {
		t.Fatalf("expected 5 rows, got %d", len(keyboard))
	}
	for _, row := range keyboard {
		for _, button := range row {
			if button.CallbackData == "pay:stripe:15" {
				t.Fatalf("stripe button must be hidden when disabled: %+v", button)
			}
		}
	}
}

func assertPaymentButton(t *testing.T, button StyledButton, wantText, wantData string) {
	t.Helper()

	if button.Text != wantText {
		t.Fatalf("button text = %q, want %q", button.Text, wantText)
	}
	if button.CallbackData != wantData {
		t.Fatalf("button callback data = %q, want %q", button.CallbackData, wantData)
	}
}
