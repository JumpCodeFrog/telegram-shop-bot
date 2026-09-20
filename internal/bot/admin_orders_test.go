package bot

// Admin order card (/order <id>) tests. Orders are seeded through the real
// storage layer and driven through the production router with the fake
// Telegram API; assertions target the recorded sendMessage calls.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"shop_bot/internal/storage"
)

// seedOrderCardOrder inserts a pending two-item order (Tee ×2, $25 / 1250 ⭐)
// carrying the given RUB/nanoton snapshots and returns its ID.
func seedOrderCardOrder(t *testing.T, e *e2eEnv, userID int64, totalRUB float64, totalTonNano int64) int64 {
	t.Helper()
	store := storage.NewSQLOrderStore(e.db)
	orderID, err := store.CreateOrder(context.Background(), &storage.Order{
		UserID: userID, TotalUSD: 25.00, TotalStars: 1250,
		TotalRUB: totalRUB, TotalTonNano: totalTonNano,
		Status: storage.OrderStatusPending,
	}, []storage.OrderItem{{ProductID: e.prodReg, ProductName: "Tee", Quantity: 2, PriceUSD: 12.50}})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	return orderID
}

// markOrderPaid settles the order through the storage state machine so the
// card shows the payment method and provider payment ID.
func markOrderPaid(t *testing.T, e *e2eEnv, orderID int64, method, paymentID string) {
	t.Helper()
	store := storage.NewSQLOrderStore(e.db)
	if err := store.UpdateOrderStatus(context.Background(), orderID,
		storage.OrderStatusPending, storage.OrderStatusPaid, method, paymentID); err != nil {
		t.Fatalf("mark order paid: %v", err)
	}
}

func TestAdminOrderCardHappyPath(t *testing.T) {
	e := newE2EEnv(t)
	buyer := int64(777)
	// Any buyer command upserts the user row (username u777) via Auth.
	e.cmd(buyer, "/start", "en")

	orderID := seedOrderCardOrder(t, e, buyer, 2312.50, 0)
	markOrderPaid(t, e, orderID, storage.PaymentMethodYooKassa, "yoo-pay-1")

	calls := e.cmd(e2eAdminID, fmt.Sprintf("/order %d", orderID), "en")
	if len(calls) != 1 || calls[0].Method != "sendMessage" {
		t.Fatalf("card calls=%+v", calls)
	}
	text := calls[0].Params.Get("text")
	for _, want := range []string{
		fmt.Sprintf("Order #%d", orderID),
		"User: 777 (@u777)",
		"Tee ×2 — $25.00",
		"$25.00 / 1250 ⭐",
		"2312.50 ₽",
		storage.StatusDisplay[storage.OrderStatusPaid],
		e.bot.t("en", "payment_method_yookassa"),
		"yoo-pay-1",
		"Created:", "Updated:",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("card missing %q:\n%s", want, text)
		}
	}
	// A RUB-card order has no TON snapshot: the TON line stays hidden.
	if strings.Contains(text, "TON") {
		t.Errorf("card must hide the TON line:\n%s", text)
	}
}

func TestAdminOrderCardTonSnapshot(t *testing.T) {
	e := newE2EEnv(t)
	orderID := seedOrderCardOrder(t, e, e2eAdminID, 0, 3896686160)
	markOrderPaid(t, e, orderID, storage.PaymentMethodTON, "ton-tx-1")

	calls := e.cmd(e2eAdminID, fmt.Sprintf("/order %d", orderID), "en")
	text := tgText(calls)
	if !strings.Contains(text, "3.89668616 TON") {
		t.Fatalf("card missing TON total:\n%s", text)
	}
	if strings.Contains(text, "₽") {
		t.Fatalf("card must hide the RUB line:\n%s", text)
	}
	if !strings.Contains(text, e.bot.t("en", "payment_method_ton")) {
		t.Fatalf("card missing TON method name:\n%s", text)
	}
}

func TestAdminOrderCardBalancePayment(t *testing.T) {
	e := newE2EEnv(t)
	orderID := seedOrderCardOrder(t, e, e2eAdminID, 0, 0)
	markOrderPaid(t, e, orderID, storage.PaymentMethodBalance, "balance-1")

	calls := e.cmd(e2eAdminID, fmt.Sprintf("/order %d", orderID), "en")
	text := tgText(calls)
	if !strings.Contains(text, e.bot.t("en", "payment_method_balance")) {
		t.Fatalf("card missing balance method name:\n%s", text)
	}
	// A pure USD balance order has neither RUB nor TON snapshots.
	if strings.Contains(text, "₽") || strings.Contains(text, "TON") {
		t.Fatalf("card must hide zero-valued currency lines:\n%s", text)
	}
}

func TestAdminOrderCardPendingOrderHidesPaymentFacts(t *testing.T) {
	e := newE2EEnv(t)
	orderID := seedOrderCardOrder(t, e, e2eAdminID, 0, 0)

	calls := e.cmd(e2eAdminID, fmt.Sprintf("/order %d", orderID), "en")
	text := tgText(calls)
	if !strings.Contains(text, fmt.Sprintf("Order #%d", orderID)) {
		t.Fatalf("card missing title:\n%s", text)
	}
	if strings.Contains(text, e.bot.t("en", "payment_method_stars")) ||
		strings.Contains(text, "Payment ID") {
		t.Fatalf("pending order must hide payment facts:\n%s", text)
	}
}

func TestAdminOrderCardUnknownID(t *testing.T) {
	e := newE2EEnv(t)

	calls := e.cmd(e2eAdminID, "/order 424242", "en")
	if got, want := tgText(calls), fmt.Sprintf(e.bot.t("en", "admin_order_not_found"), int64(424242)); got != want {
		t.Fatalf("unknown id text = %q, want %q", got, want)
	}
}

func TestAdminOrderCardUsage(t *testing.T) {
	e := newE2EEnv(t)
	want := e.bot.t("en", "admin_order_usage")

	for _, cmd := range []string{"/order", "/order abc"} {
		if got := tgText(e.cmd(e2eAdminID, cmd, "en")); got != want {
			t.Fatalf("%s text = %q, want %q", cmd, got, want)
		}
	}
}

func TestAdminOrderCardNonAdminInert(t *testing.T) {
	e := newE2EEnv(t)
	orderID := seedOrderCardOrder(t, e, e2eAdminID, 0, 0)

	if calls := e.cmd(1234, fmt.Sprintf("/order %d", orderID), "en"); len(calls) != 0 {
		t.Fatalf("non-admin /order produced calls=%+v", calls)
	}
}
