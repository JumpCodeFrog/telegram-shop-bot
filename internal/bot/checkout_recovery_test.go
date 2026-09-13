package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/shop"
	"shop_bot/internal/storage"
)

type failCheckoutSummaryReadStore struct {
	*storage.SQLOrderStore
	failNextGet bool
}

func (s *failCheckoutSummaryReadStore) GetOrder(ctx context.Context, id int64) (*storage.Order, error) {
	if s.failNextGet {
		s.failNextGet = false
		return nil, errors.New("injected transient checkout summary read failure")
	}
	return s.SQLOrderStore.GetOrder(ctx, id)
}

func TestOrderConfirmKeepsCommittedSubscriptionPayableWhenSummaryReadFails(t *testing.T) {
	e := newE2EEnv(t)
	const buyer = int64(1001)
	orders := &failCheckoutSummaryReadStore{
		SQLOrderStore: storage.NewSQLOrderStore(e.db),
		failNextGet:   true,
	}
	e.bot.order = shop.NewOrderService(orders, storage.NewCartStore(e.db.Conn()),
		e.bot.products, shop.PaymentDeps{}, e.bot.logger)
	e.cmd(buyer, "/start", "en")
	e.cb(buyer, fmt.Sprintf("cart:add:%d", e.prodSub), "en")

	calls := e.cb(buyer, "order:confirm:promo:SAVE10", "en")
	// A checkout that returns the committed snapshot need not read it again.
	// Stop injecting before separate payment and cancellation requests.
	orders.failNextGet = false
	orderID := e.qInt(`SELECT MAX(id) FROM orders WHERE user_id = ?`, buyer)
	if got := e.qInt(`SELECT COUNT(*) FROM orders WHERE user_id = ?`, buyer); got != 1 {
		t.Fatalf("created orders = %d, want exactly one", got)
	}
	if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPending {
		t.Fatalf("order status = %q, want pending", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM cart_items WHERE user_id = ?`, buyer); got != 0 {
		t.Fatalf("cart rows = %d, want successful cart clear", got)
	}
	if got := e.qInt(`SELECT total_stars FROM orders WHERE id = ?`, orderID); got != 90 {
		t.Fatalf("stored total = %d Stars, want 90 after SAVE10", got)
	}
	if got := e.qStr(`SELECT printf('%.2f', total_usd) FROM orders WHERE id = ?`, orderID); got != "1.80" {
		t.Fatalf("stored total = $%s, want $1.80 after SAVE10", got)
	}

	payload := strconv.FormatInt(orderID, 10)
	payment := requireRender(t, calls, "pay:stars:"+payload)
	requireRender(t, calls, "order:cancel:"+payload)
	if hasRender(calls, "pay:crypto:"+payload) {
		t.Error("subscription checkout offered crypto payment")
	}
	if !strings.Contains(payment.Params.Get("text"), "To pay: $1.80 / 90 ⭐") {
		t.Errorf("checkout summary lost committed discount: %q", payment.Params.Get("text"))
	}
	var markup tgbotapi.InlineKeyboardMarkup
	if err := json.Unmarshal([]byte(payment.markup()), &markup); err != nil {
		t.Fatal(err)
	}
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			if button.CallbackData != nil && *button.CallbackData == "pay:stars:"+payload &&
				!strings.Contains(button.Text, "(90 ⭐)") {
				t.Errorf("payment button lost committed discount: %q", button.Text)
			}
		}
	}
	admin := requireCall(t, calls, "sendMessage", "New order #")
	if !strings.Contains(admin.Params.Get("text"), "$1.80 / 90 ⭐") {
		t.Errorf("admin notification lost committed discount: %q", admin.Params.Get("text"))
	}

	invoice := requireCall(t, e.cb(buyer, "pay:stars:"+payload, "en"), "sendInvoice", "")
	var prices []tgbotapi.LabeledPrice
	if err := json.Unmarshal([]byte(invoice.Params.Get("prices")), &prices); err != nil {
		t.Fatal(err)
	}
	if len(prices) != 1 || prices[0].Amount != 90 || invoice.Params.Get("payload") != payload ||
		invoice.Params.Get("subscription_period") != "2592000" {
		t.Fatalf("invoice does not match committed discounted subscription: %v", invoice.Params)
	}

	e.cb(buyer, "order:cancel:"+payload, "en")
	if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusCancelled {
		t.Fatalf("cancel callback left order status %q", got)
	}
	e.cb(buyer, fmt.Sprintf("cart:add:%d", e.prodSub), "en")
	replacement := e.cb(buyer, "order:confirm:promo:SAVE10", "en")
	if got := e.qInt(`SELECT COUNT(*) FROM orders WHERE user_id = ? AND status = 'pending'`, buyer); got != 1 {
		t.Fatalf("pending replacements = %d, want reservation released after cancel", got)
	}
	replacementID := e.qInt(`SELECT MAX(id) FROM orders WHERE user_id = ?`, buyer)
	requireRender(t, replacement, fmt.Sprintf("pay:stars:%d", replacementID))
}
