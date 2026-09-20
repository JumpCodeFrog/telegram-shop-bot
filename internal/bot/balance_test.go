package bot

// Balance payment bot surface: the keyboard row appears after NOWPayments
// only for non-subscription orders when the buyer holds a positive balance;
// the tap settles synchronously through ConfirmBalancePayment and reuses
// AnnouncePaidOutcome for the full notification set; /setbalance is the
// admin-only adjustment command.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"shop_bot/internal/config"
	"shop_bot/internal/storage"
)

func TestPaymentMethodKeyboard_BalanceRowAfterNowpayments(t *testing.T) {
	keyboard := paymentMethodKeyboard(15, true, true, true, true, true, 25.00, 1849.08, 100, 19.99, 1500000000, "", nil)

	want := []string{"pay:stars:15", "pay:crypto:15", "pay:yookassa:15", "pay:stripe:15", "pay:ton:15", "pay:nowpayments:15", "pay:balance:15", "terms", "paysupport", "order:cancel:15", "back:orders", "back:menu"}
	if got := styledCallbacks(keyboard); !slices.Equal(got, want) {
		t.Fatalf("callbacks = %v, want %v", got, want)
	}
	assertPaymentButton(t, keyboard[6][0], "💰 Pay $25.00 (balance)", "pay:balance:15")
}

func TestPaymentMethodKeyboard_BalanceRowHiddenAtZero(t *testing.T) {
	keyboard := paymentMethodKeyboard(15, true, true, true, true, true, 0, 1849.08, 100, 19.99, 1500000000, "", nil)
	for _, row := range keyboard {
		for _, button := range row {
			if strings.HasPrefix(button.CallbackData, "pay:balance:") {
				t.Fatalf("balance button must be hidden at zero balance: %+v", button)
			}
		}
	}
}

// grantBalance runs the admin /setbalance command and returns its calls.
func grantBalance(e *e2eEnv, telegramID int64, amount, reason string) []tgCall {
	e.t.Helper()
	return e.cmd(e2eAdminID, fmt.Sprintf("/setbalance %d %s %s", telegramID, amount, reason), "en")
}

func TestE2E_BalancePurchaseJourney(t *testing.T) {
	out := newOutboundCapture(t)
	e := newE2EEnvWithConfig(t, func(c *config.Config) { c.OutboundWebhookURL = out.srv.URL })
	const buyer = int64(7101)
	e.cmd(buyer, "/start", "en")

	// Admin grants $25 with a reason; the echo confirms the new balance.
	calls := grantBalance(e, buyer, "25.00", "welcome grant")
	echo := requireRender(t, calls, "25.00")
	if !strings.Contains(echo.Params.Get("text"), strconv.FormatInt(buyer, 10)) {
		t.Fatalf("setbalance echo misses the user id: %q", echo.Params.Get("text"))
	}
	if got := e.qStr(`SELECT printf('%.2f', balance_usd) FROM users WHERE telegram_id = ?`, buyer); got != "25.00" {
		t.Fatalf("balance = %s, want 25.00", got)
	}
	// The adjustment is audited: internal user id, signed amount, reason,
	// admin reference.
	var txType, refID string
	var txAmount float64
	if err := e.db.Conn().QueryRow(`SELECT type, amount_usd, COALESCE(ref_id, '') FROM balance_txs`).Scan(&txType, &txAmount, &refID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(txType, "admin_adjust") || !strings.Contains(txType, "welcome grant") || txAmount != 25.00 || refID != strconv.FormatInt(e2eAdminID, 10) {
		t.Fatalf("balance_txs row: type=%q amount=%v ref=%q", txType, txAmount, refID)
	}

	// Checkout offers the balance row after the other rails.
	e.cb(buyer, fmt.Sprintf("cart:add:%d", e.prodReg), "en")
	calls = e.cb(buyer, "order:confirm", "en")
	orderID := e.qInt(`SELECT MAX(id) FROM orders WHERE user_id = ?`, buyer)
	payScreen := requireRender(t, calls, fmt.Sprintf("pay:stars:%d", orderID))
	if !strings.Contains(payScreen.markup(), fmt.Sprintf("pay:balance:%d", orderID)) {
		t.Fatalf("balance pay button missing: %s", payScreen.markup())
	}

	// Tap: synchronous settlement.
	before := e.tg.count()
	e.cb(buyer, fmt.Sprintf("pay:balance:%d", orderID), "en")
	if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPaid {
		t.Fatalf("order status = %q, want paid", got)
	}
	if got := e.qStr(`SELECT payment_method FROM orders WHERE id = ?`, orderID); got != storage.PaymentMethodBalance {
		t.Fatalf("payment_method = %q, want balance", got)
	}
	if got := e.qStr(`SELECT payment_id FROM orders WHERE id = ?`, orderID); got != fmt.Sprintf("balance:%d", orderID) {
		t.Fatalf("payment_id = %q, want balance:%d", got, orderID)
	}
	if got := e.qStr(`SELECT printf('%.2f', balance_usd) FROM users WHERE telegram_id = ?`, buyer); got != "15.00" {
		t.Fatalf("balance after payment = %s, want 15.00", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_attempts
		WHERE provider='balance' AND external_id=? AND payer_id=? AND amount_minor=1000
		  AND currency='USD' AND scale=2 AND status='succeeded'`,
		fmt.Sprintf("balance:%d", orderID), buyer); got != 1 {
		t.Fatalf("settled balance attempts = %d, want 1", got)
	}
	if got := e.qInt(`SELECT stock FROM products WHERE id = ?`, e.prodReg); got != 4 {
		t.Fatalf("stock = %d, want 4", got)
	}
	// Loyalty side effects ran through the shared outcome path.
	if got := e.qInt(`SELECT loyalty_pts FROM users WHERE telegram_id = ?`, buyer); got != 10 {
		t.Fatalf("loyalty_pts = %d, want 10", got)
	}

	// AnnouncePaidOutcome surface: buyer payment_success + admin balance
	// message + the outbound webhook.
	calls = e.tg.since(before)
	lang := e.qStr(`SELECT COALESCE(language_code, '') FROM users WHERE telegram_id = ?`, buyer)
	if !findMessage(calls, buyer, fmt.Sprintf(e.bot.t(lang, "payment_success"), orderID)) {
		t.Fatalf("no payment_success message to buyer %d:\n%s", buyer, dumpCalls(calls))
	}
	adminWant := fmt.Sprintf(e.bot.t("en", "admin_order_paid_balance"), orderID, buyer, 10.0)
	if !findMessage(calls, e2eAdminID, adminWant) {
		t.Fatalf("no admin_order_paid_balance to admin %d:\n%s", e2eAdminID, dumpCalls(calls))
	}
	ev := out.wait(t)
	if ev.Event != "order.paid" || ev.OrderID != orderID || ev.UserID != buyer ||
		ev.Method != storage.PaymentMethodBalance || ev.PaymentID != fmt.Sprintf("balance:%d", orderID) {
		t.Fatalf("outbound event = %+v, want order.paid/balance for order %d", ev, orderID)
	}

	// Replay: a second tap settles nothing and debits nothing.
	beforeReplay := e.tg.count()
	e.cb(buyer, fmt.Sprintf("pay:balance:%d", orderID), "en")
	if got := e.qStr(`SELECT printf('%.2f', balance_usd) FROM users WHERE telegram_id = ?`, buyer); got != "15.00" {
		t.Fatalf("balance after replay = %s, want still 15.00", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_attempts WHERE order_id = ?`, orderID); got != 1 {
		t.Fatalf("attempts after replay = %d, want 1", got)
	}
	if got := e.tg.count() - beforeReplay; got != 1 { // exactly the order_already_paid alert
		t.Fatalf("replay produced %d calls, want 1:\n%s", got, dumpCalls(e.tg.since(beforeReplay)))
	}
	if !out.drained() {
		t.Fatal("replay fired another outbound webhook event")
	}
}

func TestE2E_BalanceButtonVisibility(t *testing.T) {
	e := newE2EEnv(t)
	const buyer = int64(7102)
	e.cmd(buyer, "/start", "en")

	// Zero balance: no balance row.
	e.cb(buyer, fmt.Sprintf("cart:add:%d", e.prodReg), "en")
	calls := e.cb(buyer, "order:confirm", "en")
	payScreen := requireRender(t, calls, "pay:stars:")
	if strings.Contains(payScreen.markup(), "pay:balance:") {
		t.Fatalf("zero-balance buyer sees the balance row: %s", payScreen.markup())
	}

	// Subscription cart with a positive balance: still Stars-only.
	grantBalance(e, buyer, "25.00", "grant")
	e.cb(buyer, fmt.Sprintf("cart:add:%d", e.prodSub), "en")
	calls = e.cb(buyer, "order:confirm", "en")
	subScreen := requireRender(t, calls, "pay:stars:")
	if strings.Contains(subScreen.markup(), "pay:balance:") {
		t.Fatalf("subscription order offers the balance row: %s", subScreen.markup())
	}
}

func TestE2E_BalanceInsufficientTap(t *testing.T) {
	e := newE2EEnv(t)
	const buyer = int64(7103)
	e.cmd(buyer, "/start", "en")
	grantBalance(e, buyer, "5.00", "grant")

	orderID := e.placeOrder(buyer, e.prodReg, "")
	calls := e.cb(buyer, fmt.Sprintf("pay:balance:%d", orderID), "en")

	cb := requireCall(t, calls, "answerCallbackQuery", "")
	if got := cb.Params.Get("show_alert"); got != "true" {
		t.Fatalf("insufficient tap answer show_alert = %q, want true", got)
	}
	want := fmt.Sprintf(e.bot.t("en", "balance_insufficient"), 5.00)
	if got := cb.Params.Get("text"); got != want {
		t.Fatalf("insufficient alert = %q, want %q", got, want)
	}
	if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPending {
		t.Fatalf("order status = %q, want still pending", got)
	}
	if got := e.qStr(`SELECT printf('%.2f', balance_usd) FROM users WHERE telegram_id = ?`, buyer); got != "5.00" {
		t.Fatalf("balance = %s, want unchanged 5.00", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_attempts WHERE order_id = ?`, orderID); got != 0 {
		t.Fatalf("attempts = %d, want 0", got)
	}
}

func TestE2E_BalanceSubscriptionTapGuard(t *testing.T) {
	e := newE2EEnv(t)
	const buyer = int64(7104)
	e.cmd(buyer, "/start", "en")
	grantBalance(e, buyer, "25.00", "grant")

	// Even with a positive balance, a direct callback against a subscription
	// order hits the Stars-only guard.
	orderID := e.placeOrder(buyer, e.prodSub, "")
	calls := e.cb(buyer, fmt.Sprintf("pay:balance:%d", orderID), "en")
	cb := requireCall(t, calls, "answerCallbackQuery", "")
	if got, want := cb.Params.Get("text"), e.bot.t("en", "sub_stars_only"); got != want {
		t.Fatalf("subscription guard alert = %q, want %q", got, want)
	}
	if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPending {
		t.Fatalf("order status = %q, want still pending", got)
	}
	if got := e.qStr(`SELECT printf('%.2f', balance_usd) FROM users WHERE telegram_id = ?`, buyer); got != "25.00" {
		t.Fatalf("balance = %s, want unchanged 25.00", got)
	}
}

func TestE2E_SetbalanceCommand(t *testing.T) {
	e := newE2EEnv(t)
	const buyer = int64(7105)
	e.cmd(buyer, "/start", "en")

	t.Run("usage on missing or malformed args", func(t *testing.T) {
		for _, cmd := range []string{"/setbalance", "/setbalance 7105", "/setbalance abc 5", "/setbalance 7105 notanumber"} {
			calls := e.cmd(e2eAdminID, cmd, "en")
			render := requireRender(t, calls, "")
			if got, want := render.Params.Get("text"), e.bot.t("en", "admin_setbalance_usage"); got != want {
				t.Fatalf("%s → %q, want usage %q", cmd, got, want)
			}
		}
	})

	t.Run("unknown user", func(t *testing.T) {
		calls := e.cmd(e2eAdminID, "/setbalance 424242 5.00", "en")
		render := requireRender(t, calls, "")
		if got, want := render.Params.Get("text"),
			fmt.Sprintf(e.bot.t("en", "admin_setbalance_unknown_user"), int64(424242)); got != want {
			t.Fatalf("unknown user → %q, want %q", got, want)
		}
	})

	t.Run("negative overdraft rejected", func(t *testing.T) {
		calls := e.cmd(e2eAdminID, "/setbalance 7105 -999.00", "en")
		render := requireRender(t, calls, "")
		if got, want := render.Params.Get("text"),
			fmt.Sprintf(e.bot.t("en", "admin_setbalance_insufficient"), int64(buyer)); got != want {
			t.Fatalf("overdraft → %q, want %q", got, want)
		}
		if got := e.qStr(`SELECT printf('%.2f', balance_usd) FROM users WHERE telegram_id = ?`, buyer); got != "0.00" {
			t.Fatalf("balance = %s, want unchanged 0.00", got)
		}
	})

	t.Run("non-admin is silently ignored", func(t *testing.T) {
		before := e.tg.count()
		e.cmd(buyer, "/setbalance 7105 50.00", "en")
		for _, c := range e.tg.since(before) {
			if c.Method == "sendMessage" {
				t.Fatalf("non-admin /setbalance produced a message: %q", c.Params.Get("text"))
			}
		}
		if got := e.qStr(`SELECT printf('%.2f', balance_usd) FROM users WHERE telegram_id = ?`, buyer); got != "0.00" {
			t.Fatalf("balance = %s, want unchanged 0.00", got)
		}
	})

	t.Run("happy path grants and echoes", func(t *testing.T) {
		calls := e.cmd(e2eAdminID, "/setbalance 7105 12.50 prize", "en")
		render := requireRender(t, calls, "12.50")
		if got, want := render.Params.Get("text"),
			fmt.Sprintf(e.bot.t("en", "admin_setbalance_ok"), int64(buyer), 12.50); got != want {
			t.Fatalf("echo = %q, want %q", got, want)
		}
		if got := e.qStr(`SELECT printf('%.2f', balance_usd) FROM users WHERE telegram_id = ?`, buyer); got != "12.50" {
			t.Fatalf("balance = %s, want 12.50", got)
		}
	})
}

// TestPaymentMethodTextBalance pins the order-history label for the new rail.
func TestPaymentMethodTextBalance(t *testing.T) {
	e := newE2EEnv(t)
	if got, want := e.bot.paymentMethodText("en", storage.PaymentMethodBalance), e.bot.t("en", "payment_method_balance"); got != want {
		t.Fatalf("paymentMethodText(balance) = %q, want %q", got, want)
	}
}
