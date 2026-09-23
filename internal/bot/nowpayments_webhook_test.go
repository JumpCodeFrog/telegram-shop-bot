package bot

// NOWPayments IPN webhook handler tests. NOWPayments IPN callbacks are
// HMAC-SHA512-signed over the CANONICALIZED body: once the signature
// verifies, the body itself is authoritative and settlement happens WITHOUT
// any API refetch (the CryptoBot/Stripe pattern — the defining difference
// from the unsigned YooKassa flow, which must re-read the payment). These
// tests pin that property: the mock NOWPayments API's hit counter must stay
// at zero through every settlement, and nothing at all may happen without a
// valid signature.

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"shop_bot/internal/config"
	"shop_bot/internal/service"
	"shop_bot/internal/storage"
)

// nowpaymentsTestIPNSecret must match the secret enableNowpayments configures.
const nowpaymentsTestIPNSecret = "np-ipn-secret"

// nowpaymentsCounterValue reads the SuccessfulPayments{provider="nowpayments"}
// counter without pulling the testutil package, mirroring stripeCounterValue.
func nowpaymentsCounterValue(t *testing.T, metrics *service.MetricsService) float64 {
	t.Helper()
	var m dto.Metric
	if err := metrics.SuccessfulPayments.WithLabelValues("nowpayments").(prometheus.Counter).Write(&m); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	return m.GetCounter().GetValue()
}

// nowpaymentsIPNSignature signs a raw IPN body the way NOWPayments documents:
// the JSON value is re-encoded in canonical form — object keys sorted
// recursively, compact separators, no HTML escaping, no trailing newline —
// then HMAC-SHA512 with the IPN secret, hex-encoded.
//
// This helper is written INDEPENDENTLY from the adapter's unexported
// canonicalizer (same documented algorithm, separate implementation typed
// here from scratch) so a canonicalization bug on one side cannot be masked
// by the other. The bodies built by nowpaymentsIPNBody are deliberately
// non-canonical (unsorted keys, extra whitespace), so the canonicalization
// step is load-bearing: signing the raw bytes would never verify.
func nowpaymentsIPNSignature(secret, body string) string {
	var value any
	if err := json.Unmarshal([]byte(body), &value); err != nil {
		return "" // a body with no canonical form cannot carry a valid signature
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return ""
	}
	canonical := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write(canonical)
	return hex.EncodeToString(mac.Sum(nil))
}

// nowpaymentsIPNBody builds a raw IPN body shaped like a NOWPayments
// callback. Keys are deliberately unsorted and padded with extra whitespace
// so the signature helper's canonicalization step actually matters.
func nowpaymentsIPNBody(paymentID, status string, priceAmount float64, orderID int64) string {
	return fmt.Sprintf(`{ "payment_status": %q,  "order_id": "%d", "price_currency": "usd", `+
		`"payment_id": %s, "price_amount": %s }`,
		status, orderID, paymentID, strconv.FormatFloat(priceAmount, 'f', -1, 64))
}

// nowpaymentsIPNBodyRawOrder builds a finished IPN whose order_id is an
// arbitrary string. With a non-numeric order_id the IPN parses but
// PaymentReceipt must refuse to build an order receipt from it
// (webhook_invalid_receipt quarantine) even though the signature is valid.
func nowpaymentsIPNBodyRawOrder(paymentID, orderIDRaw string, priceAmount float64) string {
	return fmt.Sprintf(`{ "payment_status": "finished",  "order_id": %q, "price_currency": "usd", `+
		`"payment_id": %s, "price_amount": %s }`,
		orderIDRaw, paymentID, strconv.FormatFloat(priceAmount, 'f', -1, 64))
}

func postNowpaymentsWebhook(t *testing.T, b *Bot, signature, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/nowpayments-webhook", strings.NewReader(body))
	if signature != "" {
		req.Header.Set("x-nowpayments-sig", signature)
	}
	b.NowpaymentsWebhookHandler()(rec, req)
	return rec
}

// newNowpaymentsWebhookEnv builds a full e2e env with NOWPayments
// credentials, the outbound capture wired through config, and the bot's
// adapter pointed at the mock API so tests can prove the handler never
// issues any API call (no refetch exists for signed IPNs).
func newNowpaymentsWebhookEnv(t *testing.T, api *nowpaymentsMock, out *outboundCapture) *e2eEnv {
	t.Helper()
	e := newE2EEnvWithConfig(t, func(c *config.Config) {
		enableNowpayments(c)
		if out != nil {
			c.OutboundWebhookURL = out.srv.URL
		}
	})
	e.bot.nowpayments.SetBaseURL(api.srv.URL)
	return e
}

func TestNowpaymentsWebhookSettlesFromSignedBody(t *testing.T) {
	out := newOutboundCapture(t)
	api := newNowpaymentsMock(t)
	e := newNowpaymentsWebhookEnv(t, api, out)
	const buyer = int64(8701)
	orderID := placeUSDOrder(e, buyer)

	body := nowpaymentsIPNBody("5077125051", "finished", 10, orderID)
	before := e.tg.count()
	rec := postNowpaymentsWebhook(t, e.bot, nowpaymentsIPNSignature(nowpaymentsTestIPNSecret, body), body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if got := api.count(); got != 0 {
		t.Fatalf("nowpayments API calls = %d, want 0 (the signed body is authoritative — no refetch)", got)
	}
	if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPaid {
		t.Fatalf("order status = %q, want paid", got)
	}
	if got := e.qStr(`SELECT payment_method FROM orders WHERE id = ?`, orderID); got != storage.PaymentMethodNowpayments {
		t.Fatalf("payment_method = %q, want nowpayments", got)
	}
	if got := e.qStr(`SELECT payment_id FROM orders WHERE id = ?`, orderID); got != "5077125051" {
		t.Fatalf("payment_id = %q, want 5077125051", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_attempts
		WHERE provider='nowpayments' AND external_id='5077125051' AND status='succeeded'`); got != 1 {
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
	// The admin got the nowpayments notification with the USD total.
	adminCalls := e.tg.since(before)
	adminNotified := false
	for _, c := range adminCalls {
		if c.Method == "sendMessage" && c.Params.Get("chat_id") == strconv.FormatInt(e2eAdminID, 10) {
			if strings.Contains(c.Params.Get("text"), "NOWPayments") &&
				strings.Contains(c.Params.Get("text"), "10.00") &&
				strings.Contains(c.Params.Get("text"), fmt.Sprintf("#%d", orderID)) {
				adminNotified = true
			}
		}
	}
	if !adminNotified {
		t.Fatalf("no admin_order_paid_nowpayments message to admin %d:\n%s", e2eAdminID, dumpCalls(adminCalls))
	}

	if got := nowpaymentsCounterValue(t, e.bot.metrics); got != 1 {
		t.Fatalf("SuccessfulPayments{nowpayments} = %v, want 1", got)
	}

	ev := out.wait(t)
	if ev.Event != "order.paid" || ev.OrderID != orderID || ev.UserID != buyer ||
		ev.Method != "nowpayments" || ev.PaymentID != "5077125051" {
		t.Fatalf("outbound webhook event = %+v, want order.paid for order %d via nowpayments 5077125051", ev, orderID)
	}
}

func TestNowpaymentsWebhookRejectsInvalidSignature(t *testing.T) {
	validBody := func(orderID int64) string {
		return nowpaymentsIPNBody("5077125051", "finished", 10, orderID)
	}

	t.Run("malformed header", func(t *testing.T) {
		api := newNowpaymentsMock(t)
		e := newNowpaymentsWebhookEnv(t, api, nil)
		const buyer = int64(8711)
		orderID := placeUSDOrder(e, buyer)
		body := validBody(orderID)

		before := e.tg.count()
		rec := postNowpaymentsWebhook(t, e.bot, "not-a-signature", body)

		assertNowpaymentsWebhookRejected(t, e, api, rec, orderID, before)
	})

	t.Run("missing header", func(t *testing.T) {
		api := newNowpaymentsMock(t)
		e := newNowpaymentsWebhookEnv(t, api, nil)
		const buyer = int64(8712)
		orderID := placeUSDOrder(e, buyer)
		body := validBody(orderID)

		before := e.tg.count()
		rec := postNowpaymentsWebhook(t, e.bot, "", body)

		assertNowpaymentsWebhookRejected(t, e, api, rec, orderID, before)
	})

	t.Run("wrong secret", func(t *testing.T) {
		api := newNowpaymentsMock(t)
		e := newNowpaymentsWebhookEnv(t, api, nil)
		const buyer = int64(8713)
		orderID := placeUSDOrder(e, buyer)
		body := validBody(orderID)

		before := e.tg.count()
		rec := postNowpaymentsWebhook(t, e.bot, nowpaymentsIPNSignature("np-wrong-secret", body), body)

		assertNowpaymentsWebhookRejected(t, e, api, rec, orderID, before)
	})

	t.Run("tampered body", func(t *testing.T) {
		api := newNowpaymentsMock(t)
		e := newNowpaymentsWebhookEnv(t, api, nil)
		const buyer = int64(8714)
		orderID := placeUSDOrder(e, buyer)
		body := validBody(orderID)
		signature := nowpaymentsIPNSignature(nowpaymentsTestIPNSecret, body)

		// The signature covers the CANONICAL form, so a tamper must change a
		// value, not just whitespace: swap the payment id the signature was
		// computed over for a different one.
		tampered := strings.Replace(body, "5077125051", "5077129999", 1)
		before := e.tg.count()
		rec := postNowpaymentsWebhook(t, e.bot, signature, tampered)

		assertNowpaymentsWebhookRejected(t, e, api, rec, orderID, before)
	})
}

// assertNowpaymentsWebhookRejected pins the invalid-signature contract: 403,
// and absolutely no local artifacts — no state change, no anomaly row
// (unauthenticated junk is never recorded), no messages, no API calls.
func assertNowpaymentsWebhookRejected(t *testing.T, e *e2eEnv, api *nowpaymentsMock, rec *httptest.ResponseRecorder, orderID int64, tgBefore int) {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	if got := api.count(); got != 0 {
		t.Fatalf("nowpayments API calls = %d, want 0", got)
	}
	if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPending {
		t.Fatalf("invalid signature settled order: status = %q", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_attempts WHERE provider='nowpayments'`); got != 0 {
		t.Fatalf("payment attempts = %d, want 0", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_anomalies`); got != 0 {
		t.Fatalf("anomalies = %d, want 0 (unauthenticated junk is never recorded)", got)
	}
	if got := e.tg.count() - tgBefore; got != 0 {
		t.Fatalf("messages sent = %d, want 0:\n%s", got, dumpCalls(e.tg.since(tgBefore)))
	}
}

func TestNowpaymentsWebhookIgnoresNonFinishedStatuses(t *testing.T) {
	api := newNowpaymentsMock(t)
	e := newNowpaymentsWebhookEnv(t, api, nil)
	const buyer = int64(8721)
	orderID := placeUSDOrder(e, buyer)

	// Only "finished" settles. Every other lifecycle status — including the
	// money-adjacent partially_paid and the terminal failed/expired — is
	// normal provider noise: acknowledge, settle nothing, record nothing.
	statuses := []string{"waiting", "confirming", "partially_paid", "failed", "expired"}
	for _, status := range statuses {
		body := nowpaymentsIPNBody("5077125051", status, 10, orderID)
		before := e.tg.count()
		rec := postNowpaymentsWebhook(t, e.bot, nowpaymentsIPNSignature(nowpaymentsTestIPNSecret, body), body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200 (%s)", status, rec.Code, rec.Body.String())
		}
		if got := api.count(); got != 0 {
			t.Fatalf("%s triggered %d API calls, want 0", status, got)
		}
		if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPending {
			t.Fatalf("%s changed order status to %q", status, got)
		}
		if got := e.qInt(`SELECT COUNT(*) FROM payment_anomalies`); got != 0 {
			t.Fatalf("%s recorded %d anomalies, want 0", status, got)
		}
		if got := e.tg.count() - before; got != 0 {
			t.Fatalf("%s sent %d messages, want 0", status, got)
		}
	}
}

func TestNowpaymentsWebhookGarbageBodyWithValidSignatureQuarantines(t *testing.T) {
	api := newNowpaymentsMock(t)
	e := newNowpaymentsWebhookEnv(t, api, nil)

	// The signature scheme covers the canonicalized body, so garbage must
	// still be VALID JSON to carry a valid signature at all — but a document
	// whose field types do not match the IPN shape fails ParseIPN. The
	// signature is VALID, so the undecodable body is a real provider fact:
	// quarantine a digest and ACK so NOWPayments stops retrying.
	body := `{"payment_id": 5077125051, "payment_status": 42}`
	rec := postNowpaymentsWebhook(t, e.bot, nowpaymentsIPNSignature(nowpaymentsTestIPNSecret, body), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("garbage status = %d, want 200 so NOWPayments stops retrying (%s)", rec.Code, rec.Body.String())
	}

	var count int
	var reason, rawPayload, externalID, actor string
	if err := e.db.Conn().QueryRow(`SELECT COUNT(*), reason, raw_payload, external_id, COALESCE(actor, '')
		FROM payment_anomalies WHERE provider='nowpayments'`).Scan(&count, &reason, &rawPayload, &externalID, &actor); err != nil {
		t.Fatalf("no nowpayments anomaly recorded for signed garbage body: %v", err)
	}
	if count != 1 || reason != "webhook_parse_failure" {
		t.Fatalf("anomaly count=%d reason=%q, want 1 / webhook_parse_failure", count, reason)
	}
	if !strings.HasPrefix(rawPayload, "sha256:") || strings.Contains(rawPayload, "payment_status") {
		t.Fatalf("raw_payload was not safely digested: %q", rawPayload)
	}
	if externalID != "" {
		t.Fatalf("external_id = %q, want empty for an unparsable body", externalID)
	}
	// 4.15 durable actor: the digest quarantine carries the nowpayments
	// webhook's ingress identity.
	if actor != "webhook:nowpayments" {
		t.Fatalf("anomaly actor = %q, want webhook:nowpayments", actor)
	}
	if got := api.count(); got != 0 {
		t.Fatalf("garbage body triggered %d API calls, want 0", got)
	}

	// Oversized body: rejected by MaxBytesReader before any verification.
	oversized := strings.Repeat("a", 1<<20+1)
	rec = postNowpaymentsWebhook(t, e.bot, "", oversized)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_anomalies WHERE provider='nowpayments'`); got != 1 {
		t.Fatalf("anomalies after oversized body = %d, want still 1", got)
	}
}

func TestNowpaymentsWebhookReplayIsIdempotent(t *testing.T) {
	api := newNowpaymentsMock(t)
	e := newNowpaymentsWebhookEnv(t, api, nil)
	const buyer = int64(8731)
	orderID := placeUSDOrder(e, buyer)

	body := nowpaymentsIPNBody("5077125051", "finished", 10, orderID)
	signature := nowpaymentsIPNSignature(nowpaymentsTestIPNSecret, body)
	if rec := postNowpaymentsWebhook(t, e.bot, signature, body); rec.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	before := e.tg.count()
	if rec := postNowpaymentsWebhook(t, e.bot, signature, body); rec.Code != http.StatusOK {
		t.Fatalf("replay status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if got := api.count(); got != 0 {
		t.Fatalf("nowpayments API calls = %d, want 0", got)
	}
	if got := e.qInt(`SELECT stock FROM products WHERE id = ?`, e.prodReg); got != 4 {
		t.Fatalf("stock after replay = %d, want 4 (decremented once)", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM loyalty_txs WHERE user_id = ?`, e.userDBID(buyer)); got != 1 {
		t.Fatalf("loyalty_txs after replay = %d, want 1", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_attempts
		WHERE provider='nowpayments' AND external_id='5077125051'`); got != 1 {
		t.Fatalf("payment_attempts after replay = %d, want 1", got)
	}
	// The replay leaves the ledger projection exactly as the first settlement
	// wrote it (the storage level pins settled; pin it at the bot level too).
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id = ?`, orderID); got != storage.PaymentStateSettled {
		t.Fatalf("payment_state after replay = %q, want settled", got)
	}
	if got := e.tg.count() - before; got != 0 {
		t.Fatalf("replay sent %d messages, want 0:\n%s", got, dumpCalls(e.tg.since(before)))
	}
}

func TestNowpaymentsWebhookAmountMismatchQuarantines(t *testing.T) {
	api := newNowpaymentsMock(t)
	e := newNowpaymentsWebhookEnv(t, api, nil)
	const buyer = int64(8741)
	orderID := placeUSDOrder(e, buyer)

	// A validly signed finished IPN for $1.00 against a $10.00 order snapshot.
	body := nowpaymentsIPNBody("5077125099", "finished", 1, orderID)
	before := e.tg.count()
	rec := postNowpaymentsWebhook(t, e.bot, nowpaymentsIPNSignature(nowpaymentsTestIPNSecret, body), body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (mismatch is durably quarantined)", rec.Code)
	}
	if got := api.count(); got != 0 {
		t.Fatalf("nowpayments API calls = %d, want 0", got)
	}
	if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPending {
		t.Fatalf("mismatched receipt settled order: status = %q", got)
	}
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id = ?`, orderID); got != storage.PaymentStateNeedsReview {
		t.Fatalf("payment_state = %q, want needs_review", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_attempts WHERE provider='nowpayments'`); got != 0 {
		t.Fatalf("payment attempts = %d, want 0", got)
	}
	var reason, externalID string
	if err := e.db.Conn().QueryRow(`SELECT reason, external_id FROM payment_anomalies
		WHERE provider='nowpayments' AND external_id='5077125099'`).Scan(&reason, &externalID); err != nil {
		t.Fatalf("no nowpayments anomaly recorded for the mismatch: %v", err)
	}
	if reason != "receipt_mismatch" || externalID != "5077125099" {
		t.Fatalf("anomaly reason=%q external_id=%q, want receipt_mismatch / 5077125099", reason, externalID)
	}
	if got := e.tg.count() - before; got != 0 {
		t.Fatalf("messages sent = %d, want 0:\n%s", got, dumpCalls(e.tg.since(before)))
	}
}

func TestNowpaymentsWebhookInvalidReceiptQuarantines(t *testing.T) {
	api := newNowpaymentsMock(t)
	e := newNowpaymentsWebhookEnv(t, api, nil)
	const buyer = int64(8761)
	orderID := placeUSDOrder(e, buyer)

	// A validly signed finished IPN whose order_id is not a positive integer:
	// no receipt can be built, so the provider fact is quarantined
	// (webhook_invalid_receipt) and ACKed — never settled.
	body := nowpaymentsIPNBodyRawOrder("5077129998", "ORD-42", 10)
	before := e.tg.count()
	rec := postNowpaymentsWebhook(t, e.bot, nowpaymentsIPNSignature(nowpaymentsTestIPNSecret, body), body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the invalid receipt is durably quarantined) (%s)", rec.Code, rec.Body.String())
	}
	if got := api.count(); got != 0 {
		t.Fatalf("nowpayments API calls = %d, want 0 (the signed body is authoritative — no refetch)", got)
	}
	var reason, externalID string
	var proposed int64
	var actor string
	if err := e.db.Conn().QueryRow(`SELECT reason, external_id, proposed_order_id, COALESCE(actor, '')
		FROM payment_anomalies WHERE provider='nowpayments'`).Scan(&reason, &externalID, &proposed, &actor); err != nil {
		t.Fatalf("no nowpayments anomaly recorded for the invalid receipt: %v", err)
	}
	if reason != "webhook_invalid_receipt" || externalID != "5077129998" || proposed != 0 {
		t.Fatalf("anomaly reason=%q external_id=%q proposed_order_id=%d, want webhook_invalid_receipt / 5077129998 / 0",
			reason, externalID, proposed)
	}
	// 4.15 durable actor: the invalid-receipt quarantine carries the
	// nowpayments webhook's ingress identity.
	if actor != "webhook:nowpayments" {
		t.Fatalf("anomaly actor = %q, want webhook:nowpayments", actor)
	}
	if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPending {
		t.Fatalf("order status = %q, want pending (nothing settled)", got)
	}
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id = ?`, orderID); got != storage.PaymentStatePending {
		t.Fatalf("payment_state = %q, want pending (an orphan fact touches no order)", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_attempts WHERE provider='nowpayments'`); got != 0 {
		t.Fatalf("payment attempts = %d, want 0", got)
	}
	if got := e.tg.count() - before; got != 0 {
		t.Fatalf("messages sent = %d, want 0:\n%s", got, dumpCalls(e.tg.since(before)))
	}
}

func TestNowpaymentsWebhookQuarantineFailureWithholdsACK(t *testing.T) {
	api := newNowpaymentsMock(t)
	e := newNowpaymentsWebhookEnv(t, api, nil)
	const buyer = int64(8762)
	orderID := placeUSDOrder(e, buyer)
	body := nowpaymentsIPNBodyRawOrder("5077129997", "ORD-42", 10)

	// The quarantine write itself fails: the handler must NOT acknowledge a
	// provider fact it could not durably record — 500 so NOWPayments retries.
	e.failAnomalyRecording(errors.New("injected quarantine write failure"))

	before := e.tg.count()
	rec := postNowpaymentsWebhook(t, e.bot, nowpaymentsIPNSignature(nowpaymentsTestIPNSecret, body), body)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 so NOWPayments retries (%s)", rec.Code, rec.Body.String())
	}
	if got := api.count(); got != 0 {
		t.Fatalf("nowpayments API calls = %d, want 0", got)
	}
	if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPending {
		t.Fatalf("order status = %q, want pending (nothing settled)", got)
	}
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id = ?`, orderID); got != storage.PaymentStatePending {
		t.Fatalf("payment_state = %q, want pending (the failed write left no marker)", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_anomalies`); got != 0 {
		t.Fatalf("anomalies = %d, want 0 (no partial quarantine write)", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_attempts WHERE provider='nowpayments'`); got != 0 {
		t.Fatalf("payment attempts = %d, want 0", got)
	}
	if got := e.tg.count() - before; got != 0 {
		t.Fatalf("messages sent = %d, want 0:\n%s", got, dumpCalls(e.tg.since(before)))
	}
}

func TestNowpaymentsWebhookUnconfiguredIsInert(t *testing.T) {
	e := newE2EEnv(t) // no NOWPayments credentials
	const buyer = int64(8751)
	e.cmd(buyer, "/start", "en")
	orderID := e.placeOrder(buyer, e.prodReg, "")

	before := e.tg.count()
	body := nowpaymentsIPNBody("5077125051", "finished", 10, orderID)
	rec := postNowpaymentsWebhook(t, e.bot, nowpaymentsIPNSignature(nowpaymentsTestIPNSecret, body), body)

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

func TestNowpaymentsWebhookRejectsNonPost(t *testing.T) {
	api := newNowpaymentsMock(t)
	e := newNowpaymentsWebhookEnv(t, api, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/nowpayments-webhook", nil)
	e.bot.NowpaymentsWebhookHandler()(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := api.count(); got != 0 {
		t.Fatalf("nowpayments API calls = %d, want 0", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_anomalies`); got != 0 {
		t.Fatalf("anomalies = %d, want 0", got)
	}
}
