package bot

// Stripe webhook handler tests. Stripe events are HMAC-SIGNED: once the
// signature verifies, the body itself is authoritative and settlement happens
// WITHOUT any API refetch (the CryptoBot pattern — the defining difference
// from the unsigned YooKassa flow, which must re-read the payment). These
// tests pin that property: the mock Stripe API's hit counter must stay at
// zero through every settlement, and nothing at all may happen without a
// valid signature.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"shop_bot/internal/config"
	"shop_bot/internal/service"
	"shop_bot/internal/storage"
)

// stripeTestWebhookSecret must match the secret enableStripe configures.
const stripeTestWebhookSecret = "whsec_e2e"

// stripeCounterValue reads the SuccessfulPayments{provider="stripe"} counter
// without pulling the testutil package, mirroring yookassaCounterValue.
func stripeCounterValue(t *testing.T, metrics *service.MetricsService) float64 {
	t.Helper()
	var m dto.Metric
	if err := metrics.SuccessfulPayments.WithLabelValues("stripe").(prometheus.Counter).Write(&m); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	return m.GetCounter().GetValue()
}

// stripeWebhookSignature builds a Stripe-Signature header value for the given
// timestamp and body, signed with the given secret — exactly as Stripe does.
func stripeWebhookSignature(secret string, timestamp int64, body string) string {
	ts := strconv.FormatInt(timestamp, 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write([]byte(body))
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// stripeEventBody builds a Stripe event envelope embedding a checkout session
// object, shaped like the fixtures in internal/payment/stripe_test.go.
func stripeEventBody(eventType, sessionID, status, paymentStatus string, amountTotal, orderID int64) string {
	return fmt.Sprintf(`{"id":"evt_1","type":%q,"data":{"object":{`+
		`"id":%q,"status":%q,"payment_status":%q,"amount_total":%d,`+
		`"currency":"usd","metadata":{"order_id":"%d"}}}}`,
		eventType, sessionID, status, paymentStatus, amountTotal, orderID)
}

func postStripeWebhook(t *testing.T, b *Bot, signature, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/stripe-webhook", strings.NewReader(body))
	if signature != "" {
		req.Header.Set("Stripe-Signature", signature)
	}
	b.StripeWebhookHandler()(rec, req)
	return rec
}

// newStripeWebhookEnv builds a full e2e env with Stripe credentials, the
// outbound capture wired through config, and the bot's adapter pointed at the
// mock API so tests can prove the handler never refetches.
func newStripeWebhookEnv(t *testing.T, api *stripeMock, out *outboundCapture) *e2eEnv {
	t.Helper()
	e := newE2EEnvWithConfig(t, func(c *config.Config) {
		enableStripe(c)
		if out != nil {
			c.OutboundWebhookURL = out.srv.URL
		}
	})
	e.bot.stripe.SetBaseURL(api.srv.URL)
	return e
}

// placeUSDOrder leaves a pending order whose USD snapshot is $10.00
// (1000 cents) for the seeded regular product.
func placeUSDOrder(e *e2eEnv, buyer int64) int64 {
	e.t.Helper()
	e.cmd(buyer, "/start", "en")
	orderID := e.placeOrder(buyer, e.prodReg, "")
	if got := e.qStr(`SELECT printf('%.2f', total_usd) FROM orders WHERE id = ?`, orderID); got != "10.00" {
		e.t.Fatalf("order total_usd = %q, want 10.00", got)
	}
	return orderID
}

func TestStripeWebhookSettlesFromSignedBody(t *testing.T) {
	out := newOutboundCapture(t)
	api := newStripeMock(t)
	e := newStripeWebhookEnv(t, api, out)
	const buyer = int64(8101)
	orderID := placeUSDOrder(e, buyer)

	body := stripeEventBody("checkout.session.completed", "cs_test_1", "complete", "paid", 1000, orderID)
	before := e.tg.count()
	rec := postStripeWebhook(t, e.bot, stripeWebhookSignature(stripeTestWebhookSecret, time.Now().Unix(), body), body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if got := api.count(); got != 0 {
		t.Fatalf("stripe API calls = %d, want 0 (the signed body is authoritative — no refetch)", got)
	}
	if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPaid {
		t.Fatalf("order status = %q, want paid", got)
	}
	if got := e.qStr(`SELECT payment_method FROM orders WHERE id = ?`, orderID); got != storage.PaymentMethodStripe {
		t.Fatalf("payment_method = %q, want stripe", got)
	}
	if got := e.qStr(`SELECT payment_id FROM orders WHERE id = ?`, orderID); got != "cs_test_1" {
		t.Fatalf("payment_id = %q, want cs_test_1", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_attempts
		WHERE provider='stripe' AND external_id='cs_test_1' AND status='succeeded'`); got != 1 {
		t.Fatalf("settled payment attempts = %d, want 1", got)
	}
	if got := e.qInt(`SELECT stock FROM products WHERE id = ?`, e.prodReg); got != 4 {
		t.Fatalf("stock = %d, want 4 (decremented once)", got)
	}

	// The buyer got the localized payment_success message.
	lang := e.qStr(`SELECT COALESCE(language_code, '') FROM users WHERE telegram_id = ?`, buyer)
	wantText := fmt.Sprintf(e.bot.t(lang, "payment_success"), orderID)
	if !findMessage(e.tg.since(before), buyer, wantText) {
		t.Fatalf("no payment_success message to buyer %d (lang %q):\n%s", buyer, lang,
			dumpCalls(e.tg.since(before)))
	}
	// The admin got the stripe card notification with the USD total.
	adminCalls := e.tg.since(before)
	adminNotified := false
	for _, c := range adminCalls {
		if c.Method == "sendMessage" && c.Params.Get("chat_id") == strconv.FormatInt(e2eAdminID, 10) {
			if strings.Contains(c.Params.Get("text"), "Stripe") &&
				strings.Contains(c.Params.Get("text"), "10.00") &&
				strings.Contains(c.Params.Get("text"), fmt.Sprintf("#%d", orderID)) {
				adminNotified = true
			}
		}
	}
	if !adminNotified {
		t.Fatalf("no admin_order_paid_stripe message to admin %d:\n%s", e2eAdminID, dumpCalls(adminCalls))
	}

	if got := stripeCounterValue(t, e.bot.metrics); got != 1 {
		t.Fatalf("SuccessfulPayments{stripe} = %v, want 1", got)
	}

	ev := out.wait(t)
	if ev.Event != "order.paid" || ev.OrderID != orderID || ev.UserID != buyer ||
		ev.Method != "stripe" || ev.PaymentID != "cs_test_1" {
		t.Fatalf("outbound webhook event = %+v, want order.paid for order %d via stripe cs_test_1", ev, orderID)
	}
}

func TestStripeWebhookRejectsInvalidSignature(t *testing.T) {
	validBody := func(orderID int64) string {
		return stripeEventBody("checkout.session.completed", "cs_test_1", "complete", "paid", 1000, orderID)
	}

	t.Run("malformed header", func(t *testing.T) {
		api := newStripeMock(t)
		e := newStripeWebhookEnv(t, api, nil)
		const buyer = int64(8201)
		orderID := placeUSDOrder(e, buyer)
		body := validBody(orderID)

		before := e.tg.count()
		rec := postStripeWebhook(t, e.bot, "not-a-signature", body)

		assertStripeWebhookRejected(t, e, api, rec, orderID, before)
	})

	t.Run("missing header", func(t *testing.T) {
		api := newStripeMock(t)
		e := newStripeWebhookEnv(t, api, nil)
		const buyer = int64(8202)
		orderID := placeUSDOrder(e, buyer)
		body := validBody(orderID)

		before := e.tg.count()
		rec := postStripeWebhook(t, e.bot, "", body)

		assertStripeWebhookRejected(t, e, api, rec, orderID, before)
	})

	t.Run("wrong secret", func(t *testing.T) {
		api := newStripeMock(t)
		e := newStripeWebhookEnv(t, api, nil)
		const buyer = int64(8203)
		orderID := placeUSDOrder(e, buyer)
		body := validBody(orderID)

		before := e.tg.count()
		rec := postStripeWebhook(t, e.bot, stripeWebhookSignature("whsec_wrong", time.Now().Unix(), body), body)

		assertStripeWebhookRejected(t, e, api, rec, orderID, before)
	})

	t.Run("tampered body", func(t *testing.T) {
		api := newStripeMock(t)
		e := newStripeWebhookEnv(t, api, nil)
		const buyer = int64(8204)
		orderID := placeUSDOrder(e, buyer)
		body := validBody(orderID)
		signature := stripeWebhookSignature(stripeTestWebhookSecret, time.Now().Unix(), body)

		before := e.tg.count()
		rec := postStripeWebhook(t, e.bot, signature, body+" ")

		assertStripeWebhookRejected(t, e, api, rec, orderID, before)
	})

	t.Run("stale timestamp", func(t *testing.T) {
		api := newStripeMock(t)
		e := newStripeWebhookEnv(t, api, nil)
		const buyer = int64(8205)
		orderID := placeUSDOrder(e, buyer)
		body := validBody(orderID)
		stale := time.Now().Unix() - 301

		before := e.tg.count()
		rec := postStripeWebhook(t, e.bot, stripeWebhookSignature(stripeTestWebhookSecret, stale, body), body)

		assertStripeWebhookRejected(t, e, api, rec, orderID, before)
	})
}

// assertStripeWebhookRejected pins the invalid-signature contract: 403, and
// absolutely no local artifacts — no state change, no anomaly row (mirroring
// the crypto webhook, unauthenticated junk is never recorded), no messages,
// no API calls.
func assertStripeWebhookRejected(t *testing.T, e *e2eEnv, api *stripeMock, rec *httptest.ResponseRecorder, orderID int64, tgBefore int) {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	if got := api.count(); got != 0 {
		t.Fatalf("stripe API calls = %d, want 0", got)
	}
	if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPending {
		t.Fatalf("invalid signature settled order: status = %q", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_attempts WHERE provider='stripe'`); got != 0 {
		t.Fatalf("payment attempts = %d, want 0", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_anomalies`); got != 0 {
		t.Fatalf("anomalies = %d, want 0 (unauthenticated junk is never recorded)", got)
	}
	if got := e.tg.count() - tgBefore; got != 0 {
		t.Fatalf("messages sent = %d, want 0:\n%s", got, dumpCalls(e.tg.since(tgBefore)))
	}
}

func TestStripeWebhookIgnoresNonSettlementEvents(t *testing.T) {
	api := newStripeMock(t)
	e := newStripeWebhookEnv(t, api, nil)
	const buyer = int64(8301)
	orderID := placeUSDOrder(e, buyer)

	bodies := map[string]string{
		"payment_intent.succeeded":  stripeEventBody("payment_intent.succeeded", "cs_test_1", "complete", "paid", 1000, orderID),
		"checkout.session.expired":  stripeEventBody("checkout.session.expired", "cs_test_1", "expired", "unpaid", 1000, orderID),
		"completed without session": `{"id":"evt_1","type":"checkout.session.completed","data":{"object":{}}}`,
	}
	for name, body := range bodies {
		before := e.tg.count()
		rec := postStripeWebhook(t, e.bot, stripeWebhookSignature(stripeTestWebhookSecret, time.Now().Unix(), body), body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200 (%s)", name, rec.Code, rec.Body.String())
		}
		if got := api.count(); got != 0 {
			t.Fatalf("%s triggered %d API calls, want 0", name, got)
		}
		if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPending {
			t.Fatalf("%s changed order status to %q", name, got)
		}
		if got := e.qInt(`SELECT COUNT(*) FROM payment_anomalies`); got != 0 {
			t.Fatalf("%s recorded %d anomalies, want 0", name, got)
		}
		if got := e.tg.count() - before; got != 0 {
			t.Fatalf("%s sent %d messages, want 0", name, got)
		}
	}
}

func TestStripeWebhookGarbageBodyWithValidSignatureQuarantines(t *testing.T) {
	api := newStripeMock(t)
	e := newStripeWebhookEnv(t, api, nil)

	// The signature is VALID, so undecodable JSON is a real provider fact:
	// quarantine a digest and ACK so Stripe stops retrying.
	body := "not json at all"
	rec := postStripeWebhook(t, e.bot, stripeWebhookSignature(stripeTestWebhookSecret, time.Now().Unix(), body), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("garbage status = %d, want 200 so Stripe stops retrying (%s)", rec.Code, rec.Body.String())
	}

	var count int
	var reason, rawPayload, externalID string
	if err := e.db.Conn().QueryRow(`SELECT COUNT(*), reason, raw_payload, external_id
		FROM payment_anomalies WHERE provider='stripe'`).Scan(&count, &reason, &rawPayload, &externalID); err != nil {
		t.Fatalf("no stripe anomaly recorded for signed garbage body: %v", err)
	}
	if count != 1 || reason != "webhook_parse_failure" {
		t.Fatalf("anomaly count=%d reason=%q, want 1 / webhook_parse_failure", count, reason)
	}
	if !strings.HasPrefix(rawPayload, "sha256:") || strings.Contains(rawPayload, "not json") {
		t.Fatalf("raw_payload was not safely digested: %q", rawPayload)
	}
	if externalID != "" {
		t.Fatalf("external_id = %q, want empty for an unparsable body", externalID)
	}
	if got := api.count(); got != 0 {
		t.Fatalf("garbage body triggered %d API calls, want 0", got)
	}

	// Oversized body: rejected by MaxBytesReader before any verification.
	oversized := strings.Repeat("a", 1<<20+1)
	rec = postStripeWebhook(t, e.bot, "", oversized)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_anomalies WHERE provider='stripe'`); got != 1 {
		t.Fatalf("anomalies after oversized body = %d, want still 1", got)
	}
}

func TestStripeWebhookReplayIsIdempotent(t *testing.T) {
	api := newStripeMock(t)
	e := newStripeWebhookEnv(t, api, nil)
	const buyer = int64(8401)
	orderID := placeUSDOrder(e, buyer)

	body := stripeEventBody("checkout.session.completed", "cs_test_1", "complete", "paid", 1000, orderID)
	signature := stripeWebhookSignature(stripeTestWebhookSecret, time.Now().Unix(), body)
	if rec := postStripeWebhook(t, e.bot, signature, body); rec.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	before := e.tg.count()
	if rec := postStripeWebhook(t, e.bot, signature, body); rec.Code != http.StatusOK {
		t.Fatalf("replay status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if got := api.count(); got != 0 {
		t.Fatalf("stripe API calls = %d, want 0", got)
	}
	if got := e.qInt(`SELECT stock FROM products WHERE id = ?`, e.prodReg); got != 4 {
		t.Fatalf("stock after replay = %d, want 4 (decremented once)", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM loyalty_txs WHERE user_id = ?`, e.userDBID(buyer)); got != 1 {
		t.Fatalf("loyalty_txs after replay = %d, want 1", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_attempts
		WHERE provider='stripe' AND external_id='cs_test_1'`); got != 1 {
		t.Fatalf("payment_attempts after replay = %d, want 1", got)
	}
	if got := e.tg.count() - before; got != 0 {
		t.Fatalf("replay sent %d messages, want 0:\n%s", got, dumpCalls(e.tg.since(before)))
	}
}

func TestStripeWebhookAmountMismatchQuarantines(t *testing.T) {
	api := newStripeMock(t)
	e := newStripeWebhookEnv(t, api, nil)
	const buyer = int64(8501)
	orderID := placeUSDOrder(e, buyer)

	// A validly signed session for $1.00 against a $10.00 order snapshot.
	body := stripeEventBody("checkout.session.completed", "cs_test_mm", "complete", "paid", 100, orderID)
	before := e.tg.count()
	rec := postStripeWebhook(t, e.bot, stripeWebhookSignature(stripeTestWebhookSecret, time.Now().Unix(), body), body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (mismatch is durably quarantined)", rec.Code)
	}
	if got := api.count(); got != 0 {
		t.Fatalf("stripe API calls = %d, want 0", got)
	}
	if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPending {
		t.Fatalf("mismatched receipt settled order: status = %q", got)
	}
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id = ?`, orderID); got != storage.PaymentStateNeedsReview {
		t.Fatalf("payment_state = %q, want needs_review", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_attempts WHERE provider='stripe'`); got != 0 {
		t.Fatalf("payment attempts = %d, want 0", got)
	}
	var reason, externalID string
	if err := e.db.Conn().QueryRow(`SELECT reason, external_id FROM payment_anomalies
		WHERE provider='stripe' AND external_id='cs_test_mm'`).Scan(&reason, &externalID); err != nil {
		t.Fatalf("no stripe anomaly recorded for the mismatch: %v", err)
	}
	if reason != "receipt_mismatch" || externalID != "cs_test_mm" {
		t.Fatalf("anomaly reason=%q external_id=%q, want receipt_mismatch / cs_test_mm", reason, externalID)
	}
	if got := e.tg.count() - before; got != 0 {
		t.Fatalf("messages sent = %d, want 0:\n%s", got, dumpCalls(e.tg.since(before)))
	}
}

func TestStripeWebhookUnconfiguredIsInert(t *testing.T) {
	e := newE2EEnv(t) // no Stripe credentials
	const buyer = int64(8601)
	e.cmd(buyer, "/start", "en")
	orderID := e.placeOrder(buyer, e.prodReg, "")

	before := e.tg.count()
	body := stripeEventBody("checkout.session.completed", "cs_test_1", "complete", "paid", 1000, orderID)
	rec := postStripeWebhook(t, e.bot, stripeWebhookSignature(stripeTestWebhookSecret, time.Now().Unix(), body), body)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (%s)", rec.Code, rec.Body.String())
	}
	if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPending {
		t.Fatalf("order status = %q, want pending", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_anomalies`); got != 0 {
		t.Fatalf("anomalies = %d, want 0", got)
	}
	if got := e.tg.count() - before; got != 0 {
		t.Fatalf("messages sent = %d, want 0", got)
	}
}

func TestStripeWebhookRejectsNonPost(t *testing.T) {
	api := newStripeMock(t)
	e := newStripeWebhookEnv(t, api, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/stripe-webhook", nil)
	e.bot.StripeWebhookHandler()(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := api.count(); got != 0 {
		t.Fatalf("stripe API calls = %d, want 0", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_anomalies`); got != 0 {
		t.Fatalf("anomalies = %d, want 0", got)
	}
}
