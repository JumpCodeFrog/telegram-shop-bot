package bot

import (
	"errors"
	"testing"

	"shop_bot/internal/storage"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
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
	keyboard := paymentMethodKeyboard(15, false, "", nil)

	// Stars row + terms/support row + cancel/orders row
	if len(keyboard.InlineKeyboard) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(keyboard.InlineKeyboard))
	}

	assertPaymentButton(t, keyboard.InlineKeyboard[0][0], "⭐ Telegram Stars", "pay:stars:15")
	assertPaymentButton(t, keyboard.InlineKeyboard[1][0], "📄 Terms", "terms")
	assertPaymentButton(t, keyboard.InlineKeyboard[1][1], "🆘 Payment support", "paysupport")
	assertPaymentButton(t, keyboard.InlineKeyboard[2][0], "❌ Cancel order", "order:cancel:15")
}

func TestPaymentMethodKeyboard_ShowsCryptoWhenEnabled(t *testing.T) {
	keyboard := paymentMethodKeyboard(15, true, "", nil)

	// Stars row + crypto row + terms/support row + cancel/orders row
	if len(keyboard.InlineKeyboard) != 4 {
		t.Fatalf("expected 4 rows, got %d", len(keyboard.InlineKeyboard))
	}

	assertPaymentButton(t, keyboard.InlineKeyboard[0][0], "⭐ Telegram Stars", "pay:stars:15")
	assertPaymentButton(t, keyboard.InlineKeyboard[1][0], "💎 Crypto (USDT)", "pay:crypto:15")
	assertPaymentButton(t, keyboard.InlineKeyboard[2][0], "📄 Terms", "terms")
	assertPaymentButton(t, keyboard.InlineKeyboard[2][1], "🆘 Payment support", "paysupport")
	assertPaymentButton(t, keyboard.InlineKeyboard[3][0], "❌ Cancel order", "order:cancel:15")
}

func assertPaymentButton(t *testing.T, button tgbotapi.InlineKeyboardButton, wantText, wantData string) {
	t.Helper()

	if button.Text != wantText {
		t.Fatalf("button text = %q, want %q", button.Text, wantText)
	}
	if button.CallbackData == nil || *button.CallbackData != wantData {
		got := "<nil>"
		if button.CallbackData != nil {
			got = *button.CallbackData
		}
		t.Fatalf("button callback data = %q, want %q", got, wantData)
	}
}
