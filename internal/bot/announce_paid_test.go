package bot

// AnnouncePaidOutcome unit tests: worker-path settlements (the TON poller —
// TON's ONLY settlement path — and the CryptoBot polling backup) must deliver
// the same notification set the webhook handlers own: buyer payment_success,
// loyalty/referral outcome, admin message and outbound webhook.

import (
	"context"
	"fmt"
	"testing"

	"shop_bot/internal/config"
	"shop_bot/internal/shop"
	"shop_bot/internal/storage"
)

func TestAnnouncePaidOutcome(t *testing.T) {
	t.Run("ton delivers the full settlement surface", func(t *testing.T) {
		out := newOutboundCapture(t)
		e := newE2EEnvWithConfig(t, func(c *config.Config) { c.OutboundWebhookURL = out.srv.URL })
		const buyer = int64(7201)
		e.cmd(buyer, "/start", "en")
		before := e.tg.count()

		outcome := &shop.PaymentOutcome{
			Order: &storage.Order{
				ID: 42, UserID: buyer, TotalUSD: 10, TotalStars: 500,
				TotalTonNano: 2000000000, PaymentID: "1720000000042:e2ehash",
			},
			PointsAwarded: 10,
		}
		e.bot.AnnouncePaidOutcome(context.Background(), outcome, storage.PaymentMethodTON)

		calls := e.tg.since(before)
		lang := e.qStr(`SELECT COALESCE(language_code, '') FROM users WHERE telegram_id = ?`, buyer)
		if !findMessage(calls, buyer, fmt.Sprintf(e.bot.t(lang, "payment_success"), 42)) {
			t.Fatalf("no payment_success message to buyer %d:\n%s", buyer, dumpCalls(calls))
		}
		if !findMessage(calls, buyer, fmt.Sprintf(e.bot.t(lang, "loyalty_points_awarded"), 10)) {
			t.Fatalf("no loyalty outcome message to buyer %d:\n%s", buyer, dumpCalls(calls))
		}
		// Exact text: pins the #<id> and the TON amount string (2 TON).
		adminWant := fmt.Sprintf(e.bot.t("en", "admin_order_paid_ton"), int64(42), buyer, "2")
		if !findMessage(calls, e2eAdminID, adminWant) {
			t.Fatalf("no admin_order_paid_ton message to admin %d:\n%s", e2eAdminID, dumpCalls(calls))
		}
		ev := out.wait(t)
		if ev.Event != "order.paid" || ev.OrderID != 42 || ev.UserID != buyer ||
			ev.TotalUSD != 10 || ev.TotalStars != 500 ||
			ev.Method != "ton" || ev.PaymentID != "1720000000042:e2ehash" {
			t.Fatalf("outbound event = %+v, want order.paid/ton for order 42", ev)
		}
	})

	t.Run("crypto takes the crypto admin message", func(t *testing.T) {
		out := newOutboundCapture(t)
		e := newE2EEnvWithConfig(t, func(c *config.Config) { c.OutboundWebhookURL = out.srv.URL })
		const buyer = int64(7202)
		e.cmd(buyer, "/start", "en")
		before := e.tg.count()

		outcome := &shop.PaymentOutcome{
			Order: &storage.Order{ID: 43, UserID: buyer, TotalUSD: 10, TotalStars: 500, PaymentID: "inv_1"},
		}
		e.bot.AnnouncePaidOutcome(context.Background(), outcome, storage.PaymentMethodCrypto)

		calls := e.tg.since(before)
		adminWant := fmt.Sprintf(e.bot.t("en", "admin_order_paid_crypto"), int64(43), buyer, 10.0)
		if !findMessage(calls, e2eAdminID, adminWant) {
			t.Fatalf("no admin_order_paid_crypto message to admin %d:\n%s", e2eAdminID, dumpCalls(calls))
		}
		ev := out.wait(t)
		if ev.Event != "order.paid" || ev.OrderID != 43 || ev.Method != "crypto" || ev.PaymentID != "inv_1" {
			t.Fatalf("outbound event = %+v, want order.paid/crypto for order 43", ev)
		}
	})

	t.Run("nil outcome is a no-op", func(t *testing.T) {
		out := newOutboundCapture(t)
		e := newE2EEnvWithConfig(t, func(c *config.Config) { c.OutboundWebhookURL = out.srv.URL })
		before := e.tg.count()

		e.bot.AnnouncePaidOutcome(context.Background(), nil, storage.PaymentMethodTON)
		e.bot.AnnouncePaidOutcome(context.Background(), &shop.PaymentOutcome{}, storage.PaymentMethodTON)

		if got := e.tg.count() - before; got != 0 {
			t.Fatalf("nil outcomes sent %d messages, want 0", got)
		}
		if !out.drained() {
			t.Fatal("nil outcomes fired an outbound webhook event")
		}
	})
}
