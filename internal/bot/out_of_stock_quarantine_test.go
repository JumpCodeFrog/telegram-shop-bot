package bot

// Bot-surface coverage for out_of_stock_after_charge: the buyer's provider
// charge lands AFTER the product sold out, so settlement-time stock decrement
// fails with ErrProductOutOfStock and the handler must quarantine the money
// instead of losing it (roadmap 4.8 — mirrors the TON worker's
// TestTONPollingOutOfStockIsQuarantined and the canonical storage test
// TestRecordUnexpectedPaymentAfterOutOfStock).
//
// The five payment surfaces deliberately quarantine through TWO different
// mechanisms, and each leg below pins the surface its handler ACTUALLY writes:
//
//   - crypto webhook + Stars successful_payment call RecordUnexpectedPayment:
//     the durable evidence is a needs_review payment_attempts row plus a
//     captured/needs_review payment_events row (the review surface
//     ListPaymentReviews reads) and a payment.needs_review order event;
//     payment_anomalies stays EMPTY because no identity conflict occurred.
//     Provider redelivery is a durable no-op: the capture_on_unresolved_order
//     guard finds the exact needs_review attempt and short-circuits.
//   - yookassa/stripe/nowpayments webhooks call RecordPaymentAnomaly with the
//     provider fact: the anomaly inbox row (reason out_of_stock_after_charge,
//     proposed_order_id set) plus a payment.anomaly order event IS the
//     quarantine; no attempt/event rows exist after the first delivery. A
//     provider REDRIVERY then reaches the capture_on_unresolved_order guard,
//     which upgrades the fact into the attempt ledger (one needs_review
//     attempt + captured event) while the anomaly row stays single.
//
// Every leg asserts the order is NOT paid (status pending, payment_state
// needs_review), stock is untouched, the mechanism's quarantine artifacts,
// and the ingress outcome (all five surfaces ACK: the money is durably
// preserved, so retrying the provider event is pointless).

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shop_bot/internal/storage"
)

// depleteStock sells the product out behind an already-created pending order,
// staging the race between order creation and the provider payment event
// (the TON worker fix-round staging pattern).
func depleteStock(e *e2eEnv, productID int64) {
	e.t.Helper()
	if _, err := e.db.Conn().Exec(`UPDATE products SET stock = 0 WHERE id = ?`, productID); err != nil {
		e.t.Fatal(err)
	}
}

// assertOrderQuarantinedNotPaid pins the shared out-of-stock outcome: the
// order never settles and the stock stays depleted.
func assertOrderQuarantinedNotPaid(t *testing.T, e *e2eEnv, orderID, productID int64) {
	t.Helper()
	if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPending {
		t.Fatalf("order status = %q, want pending (money quarantined, never settled)", got)
	}
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id = ?`, orderID); got != storage.PaymentStateNeedsReview {
		t.Fatalf("payment_state = %q, want needs_review", got)
	}
	if got := e.qInt(`SELECT stock FROM products WHERE id = ?`, productID); got != 0 {
		t.Fatalf("stock = %d, want 0 (the failed settlement decremented nothing)", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM order_events
		WHERE order_id = ? AND event_type = 'payment.settled'`, orderID); got != 0 {
		t.Fatalf("payment.settled order events = %d, want 0", got)
	}
}

// assertUnexpectedPaymentQuarantine pins the RecordUnexpectedPayment surface
// (crypto webhook + Stars handler mechanism): exactly one needs_review
// attempt and one captured/needs_review event for the provider fact, a
// payment.needs_review order event, and NO payment_anomalies row.
func assertUnexpectedPaymentQuarantine(t *testing.T, e *e2eEnv, orderID int64, provider, externalID string) {
	t.Helper()
	if got := e.qInt(`SELECT COUNT(*) FROM payment_attempts
		WHERE order_id = ? AND provider = ? AND external_id = ? AND status = 'needs_review'`,
		orderID, provider, externalID); got != 1 {
		t.Fatalf("needs_review attempts (%s/%s) = %d, want 1", provider, externalID, got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_events
		WHERE order_id = ? AND provider = ? AND external_id = ?
		  AND event_kind = 'captured' AND disposition = 'needs_review'`,
		orderID, provider, externalID); got != 1 {
		t.Fatalf("captured/needs_review events (%s/%s) = %d, want 1", provider, externalID, got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_anomalies WHERE proposed_order_id = ?`, orderID); got != 0 {
		t.Fatalf("anomalies = %d, want 0 (the attempt/event rows are the quarantine)", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM order_events
		WHERE order_id = ? AND event_type = 'payment.needs_review'`, orderID); got != 1 {
		t.Fatalf("payment.needs_review order events = %d, want 1", got)
	}
}

// assertAnomalyQuarantine pins the RecordPaymentAnomaly surface (the
// yookassa/stripe/nowpayments webhook mechanism): exactly one anomaly inbox
// row carrying out_of_stock_after_charge and the proposed order, a
// payment.anomaly order event, and NO attempt/event rows.
func assertAnomalyQuarantine(t *testing.T, e *e2eEnv, orderID int64, provider, externalID string) {
	t.Helper()
	var reason string
	var proposed int64
	if err := e.db.Conn().QueryRow(`SELECT reason, proposed_order_id FROM payment_anomalies
		WHERE provider = ? AND external_id = ?`, provider, externalID).Scan(&reason, &proposed); err != nil {
		t.Fatalf("no %s anomaly recorded for %s: %v", provider, externalID, err)
	}
	if reason != "out_of_stock_after_charge" || proposed != orderID {
		t.Fatalf("anomaly reason=%q proposed_order_id=%d, want out_of_stock_after_charge / %d",
			reason, proposed, orderID)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_anomalies WHERE proposed_order_id = ?`, orderID); got != 1 {
		t.Fatalf("anomalies = %d, want exactly 1", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_attempts WHERE order_id = ?`, orderID); got != 0 {
		t.Fatalf("payment attempts = %d, want 0 (this mechanism quarantines via the anomaly inbox)", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_events WHERE order_id = ?`, orderID); got != 0 {
		t.Fatalf("payment events = %d, want 0", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM order_events
		WHERE order_id = ? AND event_type = 'payment.anomaly'`, orderID); got != 1 {
		t.Fatalf("payment.anomaly order events = %d, want 1", got)
	}
}

// assertAnomalyRedeliveryUpgraded pins what a provider REDRIVERY does after
// the anomaly-inbox quarantine: the capture_on_unresolved_order guard in
// ConfirmPaymentReceipt finds the pending/needs_review order and records the
// fact into the attempt ledger, so the review surface sees the money. The
// anomaly row stays single; exactly one needs_review attempt + captured event
// appear; a further redelivery is then an exact no-op.
func assertAnomalyRedeliveryUpgraded(t *testing.T, e *e2eEnv, orderID int64, provider, externalID string) {
	t.Helper()
	if got := e.qInt(`SELECT COUNT(*) FROM payment_anomalies WHERE proposed_order_id = ?`, orderID); got != 1 {
		t.Fatalf("anomalies after redelivery = %d, want still 1", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_attempts
		WHERE order_id = ? AND provider = ? AND external_id = ? AND status = 'needs_review'`,
		orderID, provider, externalID); got != 1 {
		t.Fatalf("needs_review attempts after redelivery = %d, want 1 (guard upgrade)", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_events
		WHERE order_id = ? AND provider = ? AND external_id = ?
		  AND event_kind = 'captured' AND disposition = 'needs_review'`,
		orderID, provider, externalID); got != 1 {
		t.Fatalf("captured/needs_review events after redelivery = %d, want 1 (guard upgrade)", got)
	}
}

// TestCryptoWebhookOutOfStockAfterChargeIsQuarantined: a signed CryptoBot
// invoice_paid webhook for an order whose product sold out after checkout.
// The handler's ErrProductOutOfStock branch calls RecordUnexpectedPayment
// (reason out_of_stock_after_charge) and ACKs.
func TestCryptoWebhookOutOfStockAfterChargeIsQuarantined(t *testing.T) {
	e := newE2EEnv(t)
	const buyer = int64(9601)
	e.cmd(buyer, "/start", "en")
	orderID := e.placeOrder(buyer, e.prodReg, "")

	// The stock sells out between order creation and the webhook.
	depleteStock(e, e.prodReg)

	body := fmt.Sprintf(`{"update_type":"invoice_paid","payload":{"invoice_id":9611,"status":"paid",`+
		`"asset":"USDT","amount":"10.00","paid_at":"2026-09-20T10:00:00Z","payload":"%d"}}`, orderID)
	post := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/cryptobot-webhook", strings.NewReader(body))
		req.Header.Set("crypto-pay-api-signature", cryptoSign(body))
		e.bot.CryptoBotWebhookHandler()(rec, req)
		return rec
	}

	before := e.tg.count()
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the receipt is durably quarantined) (%s)", rec.Code, rec.Body.String())
	}

	assertOrderQuarantinedNotPaid(t, e, orderID, e.prodReg)
	assertUnexpectedPaymentQuarantine(t, e, orderID, storage.PaymentMethodCrypto, "9611")
	if got := e.tg.count() - before; got != 0 {
		t.Fatalf("messages sent = %d, want 0 (nothing settled, nothing to announce):\n%s",
			got, dumpCalls(e.tg.since(before)))
	}

	// Redelivery: the guard short-circuits on the exact needs_review attempt —
	// ACK again, no second row of any kind.
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("redelivery status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	assertUnexpectedPaymentQuarantine(t, e, orderID, storage.PaymentMethodCrypto, "9611")
	assertOrderQuarantinedNotPaid(t, e, orderID, e.prodReg)
	if got := e.tg.count() - before; got != 0 {
		t.Fatalf("redelivery sent %d messages, want 0:\n%s", got, dumpCalls(e.tg.since(before)))
	}
}

// TestYooKassaWebhookOutOfStockAfterChargeIsQuarantined: the authoritative
// refetch confirms a succeeded RUB charge for an order whose product sold
// out. The handler's ErrProductOutOfStock branch quarantines the refetched
// provider fact via RecordPaymentAnomaly and ACKs.
func TestYooKassaWebhookOutOfStockAfterChargeIsQuarantined(t *testing.T) {
	api := newYookassaWebhookAPIMock(t, http.StatusOK, "")
	e := newYooKassaWebhookEnv(t, api, nil)
	const buyer = int64(9602)
	orderID := placeRUBOrder(e, buyer)

	depleteStock(e, e.prodReg)
	api.setBody(yookassaRefetchJSON("pay_oos", "succeeded", "1849.08", true, orderID))

	post := func() *httptest.ResponseRecorder {
		return postYooKassaWebhook(t, e.bot, yookassaNotificationBody("payment.succeeded", "pay_oos"))
	}

	before := e.tg.count()
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the fact is durably quarantined) (%s)", rec.Code, rec.Body.String())
	}
	if got := api.count(); got != 1 {
		t.Fatalf("refetch calls = %d, want 1 (settlement went through the API)", got)
	}

	assertOrderQuarantinedNotPaid(t, e, orderID, e.prodReg)
	assertAnomalyQuarantine(t, e, orderID, storage.PaymentMethodYooKassa, "pay_oos")
	if got := e.tg.count() - before; got != 0 {
		t.Fatalf("messages sent = %d, want 0:\n%s", got, dumpCalls(e.tg.since(before)))
	}

	// Redelivery: the capture_on_unresolved_order guard upgrades the fact into
	// the attempt ledger; the anomaly row stays single.
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("redelivery status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if got := api.count(); got != 2 {
		t.Fatalf("refetch calls after redelivery = %d, want 2 (every notification is re-verified)", got)
	}
	assertAnomalyRedeliveryUpgraded(t, e, orderID, storage.PaymentMethodYooKassa, "pay_oos")
	assertOrderQuarantinedNotPaid(t, e, orderID, e.prodReg)
	if got := e.tg.count() - before; got != 0 {
		t.Fatalf("redelivery sent %d messages, want 0:\n%s", got, dumpCalls(e.tg.since(before)))
	}
}

// TestStripeWebhookOutOfStockAfterChargeIsQuarantined: a signed
// checkout.session.completed event for an order whose product sold out. The
// handler's ErrProductOutOfStock branch quarantines the session fact via
// RecordPaymentAnomaly and ACKs — with no API call at all (signed bodies are
// authoritative).
func TestStripeWebhookOutOfStockAfterChargeIsQuarantined(t *testing.T) {
	api := newStripeMock(t)
	e := newStripeWebhookEnv(t, api, nil)
	const buyer = int64(9603)
	orderID := placeUSDOrder(e, buyer)

	depleteStock(e, e.prodReg)

	body := stripeEventBody("checkout.session.completed", "cs_oos", "complete", "paid", 1000, orderID)
	signature := stripeWebhookSignature(stripeTestWebhookSecret, time.Now().Unix(), body)
	post := func() *httptest.ResponseRecorder {
		return postStripeWebhook(t, e.bot, signature, body)
	}

	before := e.tg.count()
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the fact is durably quarantined) (%s)", rec.Code, rec.Body.String())
	}
	if got := api.count(); got != 0 {
		t.Fatalf("stripe API calls = %d, want 0 (the signed body is authoritative)", got)
	}

	assertOrderQuarantinedNotPaid(t, e, orderID, e.prodReg)
	assertAnomalyQuarantine(t, e, orderID, storage.PaymentMethodStripe, "cs_oos")
	if got := e.tg.count() - before; got != 0 {
		t.Fatalf("messages sent = %d, want 0:\n%s", got, dumpCalls(e.tg.since(before)))
	}

	// Redelivery: guard upgrade into the attempt ledger, anomaly stays single.
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("redelivery status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	assertAnomalyRedeliveryUpgraded(t, e, orderID, storage.PaymentMethodStripe, "cs_oos")
	assertOrderQuarantinedNotPaid(t, e, orderID, e.prodReg)
	if got := e.tg.count() - before; got != 0 {
		t.Fatalf("redelivery sent %d messages, want 0:\n%s", got, dumpCalls(e.tg.since(before)))
	}
}

// TestNowpaymentsWebhookOutOfStockAfterChargeIsQuarantined: a signed
// finished IPN for an order whose product sold out. The handler's
// ErrProductOutOfStock branch quarantines the IPN fact via
// RecordPaymentAnomaly and ACKs — with no API call at all.
func TestNowpaymentsWebhookOutOfStockAfterChargeIsQuarantined(t *testing.T) {
	api := newNowpaymentsMock(t)
	e := newNowpaymentsWebhookEnv(t, api, nil)
	const buyer = int64(9604)
	orderID := placeUSDOrder(e, buyer)

	depleteStock(e, e.prodReg)

	body := nowpaymentsIPNBody("5099001122", "finished", 10, orderID)
	post := func() *httptest.ResponseRecorder {
		return postNowpaymentsWebhook(t, e.bot, nowpaymentsIPNSignature(nowpaymentsTestIPNSecret, body), body)
	}

	before := e.tg.count()
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the fact is durably quarantined) (%s)", rec.Code, rec.Body.String())
	}
	if got := api.count(); got != 0 {
		t.Fatalf("nowpayments API calls = %d, want 0 (the signed IPN is authoritative)", got)
	}

	assertOrderQuarantinedNotPaid(t, e, orderID, e.prodReg)
	assertAnomalyQuarantine(t, e, orderID, storage.PaymentMethodNowpayments, "5099001122")
	if got := e.tg.count() - before; got != 0 {
		t.Fatalf("messages sent = %d, want 0:\n%s", got, dumpCalls(e.tg.since(before)))
	}

	// Redelivery: guard upgrade into the attempt ledger, anomaly stays single.
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("redelivery status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	assertAnomalyRedeliveryUpgraded(t, e, orderID, storage.PaymentMethodNowpayments, "5099001122")
	assertOrderQuarantinedNotPaid(t, e, orderID, e.prodReg)
	if got := e.tg.count() - before; got != 0 {
		t.Fatalf("redelivery sent %d messages, want 0:\n%s", got, dumpCalls(e.tg.since(before)))
	}
}

// TestStarsSuccessfulPaymentOutOfStockAfterChargeIsQuarantined: a Telegram
// Stars successful_payment update (delivered through the production webhook
// ingress) for an order whose product sold out. processSuccessfulPayment's
// ErrProductOutOfStock branch calls RecordUnexpectedPayment — the crypto
// surface — and returns nil, so the ingress ACKs with 200.
func TestStarsSuccessfulPaymentOutOfStockAfterChargeIsQuarantined(t *testing.T) {
	e := newE2EEnv(t)
	e.bot.cfg.TelegramWebhookSecret = testTelegramWebhookSecret
	const buyer = int64(9605)
	e.cmd(buyer, "/start", "en")
	orderID := e.placeOrder(buyer, e.prodReg, "")

	depleteStock(e, e.prodReg)

	body := telegramSuccessfulPaymentBody(41, buyer, fmt.Sprint(orderID), "stars-oos-1", 500)
	post := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/telegram-webhook", strings.NewReader(body))
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", testTelegramWebhookSecret)
		e.bot.TelegramWebhookHandler()(rec, req)
		return rec
	}

	before := e.tg.count()
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the charge is durably quarantined) (%s)", rec.Code, rec.Body.String())
	}

	assertOrderQuarantinedNotPaid(t, e, orderID, e.prodReg)
	assertUnexpectedPaymentQuarantine(t, e, orderID, storage.PaymentMethodStars, "stars-oos-1")
	if got := e.tg.count() - before; got != 0 {
		t.Fatalf("messages sent = %d, want 0 (no receipt, no admin card — nothing settled):\n%s",
			got, dumpCalls(e.tg.since(before)))
	}

	// Redelivery of the identical update: exact needs_review replay, no new
	// rows, ACK again.
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("redelivery status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	assertUnexpectedPaymentQuarantine(t, e, orderID, storage.PaymentMethodStars, "stars-oos-1")
	assertOrderQuarantinedNotPaid(t, e, orderID, e.prodReg)
	if got := e.tg.count() - before; got != 0 {
		t.Fatalf("redelivery sent %d messages, want 0:\n%s", got, dumpCalls(e.tg.since(before)))
	}
}
