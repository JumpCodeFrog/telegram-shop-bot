package bot

// YooKassa webhook handler tests. YooKassa notifications are UNSIGNED: the
// body only tells the handler WHICH payment changed; settlement must happen
// exclusively after the authoritative GetPayment refetch and shop receipt
// validation. These tests pin that security property: a lying body must
// never move money.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"shop_bot/internal/config"
	"shop_bot/internal/service"
	"shop_bot/internal/storage"
)

// yookassaCounterValue reads the SuccessfulPayments{provider="yookassa"}
// counter without pulling the testutil package (which would add a new module
// dependency), mirroring the helper in internal/shop/order_metrics_test.go.
func yookassaCounterValue(t *testing.T, metrics *service.MetricsService) float64 {
	t.Helper()
	var m dto.Metric
	if err := metrics.SuccessfulPayments.WithLabelValues("yookassa").(prometheus.Counter).Write(&m); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	return m.GetCounter().GetValue()
}

// --- fake YooKassa API for the refetch ---

// yookassaWebhookAPIMock answers GET /v3/payments/{id} with a canned payment
// object (or an HTTP error) and counts every hit so tests can prove whether
// the handler consulted the API at all. The method and path are pinned
// (mirroring the payment-package fixture): the ONLY legitimate call is the
// authoritative refetch, so any other method or path fails the test.
type yookassaWebhookAPIMock struct {
	mu     sync.Mutex
	srv    *httptest.Server
	hits   int
	status int
	body   string
}

func newYookassaWebhookAPIMock(t *testing.T, status int, body string) *yookassaWebhookAPIMock {
	t.Helper()
	m := &yookassaWebhookAPIMock{status: status, body: body}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("yookassa API mock: method = %s, want GET (the only call is the payment refetch)", r.Method)
		}
		if !strings.HasPrefix(r.URL.Path, "/v3/payments/") {
			t.Errorf("yookassa API mock: path = %q, want a /v3/payments/ prefix", r.URL.Path)
		}
		m.mu.Lock()
		m.hits++
		body, status := m.body, m.status
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"type":"error","id":"err-1","code":"internal_error","description":"boom"}`))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *yookassaWebhookAPIMock) setBody(body string) {
	m.mu.Lock()
	m.body = body
	m.mu.Unlock()
}

func (m *yookassaWebhookAPIMock) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hits
}

// yookassaRefetchJSON builds the authoritative payment object the mock API
// returns for GET /v3/payments/{id}. Field shapes mirror the fixtures in
// internal/payment/yookassa_test.go.
func yookassaRefetchJSON(id, status, amount string, paid bool, orderID int64) string {
	return fmt.Sprintf(`{"id":%q,"status":%q,"paid":%t,`+
		`"amount":{"value":%q,"currency":"RUB"},`+
		`"metadata":{"order_id":"%d"},`+
		`"created_at":"2026-09-19T10:00:00Z","captured_at":"2026-09-19T10:01:00Z"}`,
		id, status, paid, amount, orderID)
}

// yookassaRefetchNoOrderJSON builds an authoritative PAID+SUCCEEDED payment
// whose metadata carries no order reference: the payment itself is terminal
// and well-formed, but PaymentReceipt must refuse to build an order receipt
// from it (webhook_invalid_receipt quarantine).
func yookassaRefetchNoOrderJSON(id, amount string) string {
	return fmt.Sprintf(`{"id":%q,"status":"succeeded","paid":true,`+
		`"amount":{"value":%q,"currency":"RUB"},`+
		`"metadata":{"note":"no order reference here"},`+
		`"created_at":"2026-09-19T10:00:00Z","captured_at":"2026-09-19T10:01:00Z"}`,
		id, amount)
}

// --- outbound webhook capture ---

// outboundCapture records the events the bot fires to the configured
// outbound webhook URL. OutboundWebhookService.Send is asynchronous, so
// events are delivered through a buffered channel and awaited explicitly.
type outboundCapture struct {
	srv    *httptest.Server
	events chan service.OutboundWebhookEvent
}

func newOutboundCapture(t *testing.T) *outboundCapture {
	t.Helper()
	c := &outboundCapture{events: make(chan service.OutboundWebhookEvent, 8)}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev service.OutboundWebhookEvent
		if err := json.NewDecoder(r.Body).Decode(&ev); err == nil {
			c.events <- ev
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *outboundCapture) wait(t *testing.T) service.OutboundWebhookEvent {
	t.Helper()
	select {
	case ev := <-c.events:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no outbound webhook event arrived")
		return service.OutboundWebhookEvent{}
	}
}

func (c *outboundCapture) drained() bool {
	select {
	case <-c.events:
		return false
	default:
		return true
	}
}

// --- fixture helpers ---

// newYooKassaWebhookEnv builds a full e2e env with YooKassa credentials, the
// outbound capture wired through config, and the bot's adapter pointed at the
// mock API via the SetBaseURL test seam.
func newYooKassaWebhookEnv(t *testing.T, api *yookassaWebhookAPIMock, out *outboundCapture) *e2eEnv {
	t.Helper()
	e := newE2EEnvWithConfig(t, func(c *config.Config) {
		enableYooKassa(c)
		if out != nil {
			c.OutboundWebhookURL = out.srv.URL
		}
	})
	// The mock pins the production /v3 path prefix, so the adapter's base
	// URL carries it exactly like https://api.yookassa.ru/v3 would.
	e.bot.yookassa.SetBaseURL(api.srv.URL + "/v3")
	return e
}

// placeRUBOrder leaves a pending order whose RUB snapshot is 1849.08
// ($19.99 at the 92.5 rate configured by enableYooKassa).
func placeRUBOrder(e *e2eEnv, buyer int64) int64 {
	e.t.Helper()
	if _, err := e.db.Conn().Exec(`UPDATE products SET price_usd = 19.99 WHERE id = ?`, e.prodReg); err != nil {
		e.t.Fatal(err)
	}
	e.cmd(buyer, "/start", "en")
	orderID := e.placeOrder(buyer, e.prodReg, "")
	if got := e.qStr(`SELECT printf('%.2f', total_rub) FROM orders WHERE id = ?`, orderID); got != "1849.08" {
		e.t.Fatalf("order total_rub = %q, want 1849.08", got)
	}
	return orderID
}

func yookassaNotificationBody(event, paymentID string) string {
	return fmt.Sprintf(`{"event":%q,"object":{"id":%q}}`, event, paymentID)
}

func postYooKassaWebhook(t *testing.T, b *Bot, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/yookassa-webhook", strings.NewReader(body))
	b.YooKassaWebhookHandler()(rec, req)
	return rec
}

// findMessage reports whether a sendMessage call to chatID with exactly the
// wanted text was recorded.
func findMessage(calls []tgCall, chatID int64, text string) bool {
	for _, c := range calls {
		if c.Method == "sendMessage" && c.Params.Get("chat_id") == strconv.FormatInt(chatID, 10) &&
			c.Params.Get("text") == text {
			return true
		}
	}
	return false
}

func TestYooKassaWebhookSettlesAfterRefetch(t *testing.T) {
	out := newOutboundCapture(t)
	api := newYookassaWebhookAPIMock(t, http.StatusOK, "")
	e := newYooKassaWebhookEnv(t, api, out)
	// Pin the settlement attribution (docs/payment-operations.md §12): the
	// success path must log the webhook actor. The bot's logger is injected
	// (NewWithAPI), so the swap needs no slog.SetDefault dance.
	var settleLogs bytes.Buffer
	e.bot.logger = slog.New(slog.NewTextHandler(&settleLogs, nil))
	const buyer = int64(7101)
	orderID := placeRUBOrder(e, buyer)
	api.setBody(yookassaRefetchJSON("pay_1", "succeeded", "1849.08", true, orderID))

	// The body only names the payment; it must not be trusted for money.
	body := yookassaNotificationBody("payment.succeeded", "pay_1")
	before := e.tg.count()
	rec := postYooKassaWebhook(t, e.bot, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if got := api.count(); got != 1 {
		t.Fatalf("refetch calls = %d, want 1 (settlement must go through the API)", got)
	}
	if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPaid {
		t.Fatalf("order status = %q, want paid", got)
	}
	if got := e.qStr(`SELECT payment_method FROM orders WHERE id = ?`, orderID); got != storage.PaymentMethodYooKassa {
		t.Fatalf("payment_method = %q, want yookassa", got)
	}
	if got := e.qStr(`SELECT payment_id FROM orders WHERE id = ?`, orderID); got != "pay_1" {
		t.Fatalf("payment_id = %q, want pay_1", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_attempts
		WHERE provider='yookassa' AND external_id='pay_1' AND status='succeeded'`); got != 1 {
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
	// The admin got the yookassa card notification with the RUB total.
	adminCalls := e.tg.since(before)
	adminNotified := false
	for _, c := range adminCalls {
		if c.Method == "sendMessage" && c.Params.Get("chat_id") == strconv.FormatInt(e2eAdminID, 10) {
			if strings.Contains(c.Params.Get("text"), "YooKassa") &&
				strings.Contains(c.Params.Get("text"), "1849.08") &&
				strings.Contains(c.Params.Get("text"), fmt.Sprintf("#%d", orderID)) {
				adminNotified = true
			}
		}
	}
	if !adminNotified {
		t.Fatalf("no admin_order_paid_yookassa message to admin %d:\n%s", e2eAdminID, dumpCalls(adminCalls))
	}

	if got := yookassaCounterValue(t, e.bot.metrics); got != 1 {
		t.Fatalf("SuccessfulPayments{yookassa} = %v, want 1", got)
	}

	ev := out.wait(t)
	if ev.Event != "order.paid" || ev.OrderID != orderID || ev.UserID != buyer ||
		ev.Method != "yookassa" || ev.PaymentID != "pay_1" {
		t.Fatalf("outbound webhook event = %+v, want order.paid for order %d via yookassa pay_1", ev, orderID)
	}

	if got := strings.Count(settleLogs.String(), "actor=webhook:yookassa"); got != 1 {
		t.Fatalf("settlement log actor=webhook:yookassa count = %d, want exactly 1; logs:\n%s",
			got, settleLogs.String())
	}
}

func TestYooKassaWebhookBodyIsNeverTrusted(t *testing.T) {
	t.Run("non-terminal refetch is acknowledged without settlement", func(t *testing.T) {
		api := newYookassaWebhookAPIMock(t, http.StatusOK, "")
		e := newYooKassaWebhookEnv(t, api, nil)
		const buyer = int64(7201)
		orderID := placeRUBOrder(e, buyer)
		// The body claims success; the API says the payment is still pending.
		api.setBody(yookassaRefetchJSON("pay_np", "pending", "1849.08", false, orderID))

		before := e.tg.count()
		rec := postYooKassaWebhook(t, e.bot, yookassaNotificationBody("payment.succeeded", "pay_np"))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (retrying a non-terminal payment is pointless)", rec.Code)
		}
		if got := api.count(); got != 1 {
			t.Fatalf("refetch calls = %d, want 1", got)
		}
		if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPending {
			t.Fatalf("lying body settled order: status = %q", got)
		}
		if got := e.qInt(`SELECT COUNT(*) FROM payment_attempts WHERE provider='yookassa'`); got != 0 {
			t.Fatalf("payment attempts = %d, want 0", got)
		}
		if got := e.qInt(`SELECT COUNT(*) FROM payment_anomalies WHERE provider='yookassa'`); got != 0 {
			t.Fatalf("anomalies = %d, want 0 (non-terminal is not a fact)", got)
		}
		if got := e.tg.count() - before; got != 0 {
			t.Fatalf("messages sent = %d, want 0:\n%s", got, dumpCalls(e.tg.since(before)))
		}
	})

	t.Run("amount mismatch is quarantined and never settled", func(t *testing.T) {
		api := newYookassaWebhookAPIMock(t, http.StatusOK, "")
		e := newYooKassaWebhookEnv(t, api, nil)
		const buyer = int64(7202)
		orderID := placeRUBOrder(e, buyer)
		// The API confirms 1.00 RUB against a 1849.08 RUB order snapshot.
		api.setBody(yookassaRefetchJSON("pay_mm", "succeeded", "1.00", true, orderID))

		before := e.tg.count()
		rec := postYooKassaWebhook(t, e.bot, yookassaNotificationBody("payment.succeeded", "pay_mm"))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (mismatch is durably quarantined)", rec.Code)
		}
		if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPending {
			t.Fatalf("mismatched receipt settled order: status = %q", got)
		}
		if got := e.qStr(`SELECT payment_state FROM orders WHERE id = ?`, orderID); got != storage.PaymentStateNeedsReview {
			t.Fatalf("payment_state = %q, want needs_review", got)
		}
		if got := e.qInt(`SELECT COUNT(*) FROM payment_attempts WHERE provider='yookassa'`); got != 0 {
			t.Fatalf("payment attempts = %d, want 0", got)
		}
		var reason string
		var externalID string
		if err := e.db.Conn().QueryRow(`SELECT reason, external_id FROM payment_anomalies
			WHERE provider='yookassa' AND external_id='pay_mm'`).Scan(&reason, &externalID); err != nil {
			t.Fatalf("no yookassa anomaly recorded for the mismatch: %v", err)
		}
		if reason != "receipt_mismatch" || externalID != "pay_mm" {
			t.Fatalf("anomaly reason=%q external_id=%q, want receipt_mismatch / pay_mm", reason, externalID)
		}
		if got := e.tg.count() - before; got != 0 {
			t.Fatalf("messages sent = %d, want 0:\n%s", got, dumpCalls(e.tg.since(before)))
		}
	})
}

func TestYooKassaWebhookIgnoresNonPaymentEvents(t *testing.T) {
	api := newYookassaWebhookAPIMock(t, http.StatusOK, "{}")
	e := newYooKassaWebhookEnv(t, api, nil)
	const buyer = int64(7301)
	orderID := placeRUBOrder(e, buyer)

	for _, event := range []string{"refund.succeeded", "payment.canceled"} {
		before := e.tg.count()
		rec := postYooKassaWebhook(t, e.bot, yookassaNotificationBody(event, "pay_1"))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200 (%s)", event, rec.Code, rec.Body.String())
		}
		if got := api.count(); got != 0 {
			t.Fatalf("%s triggered %d API calls, want 0", event, got)
		}
		if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPending {
			t.Fatalf("%s changed order status to %q", event, got)
		}
		if got := e.qInt(`SELECT COUNT(*) FROM payment_anomalies WHERE provider='yookassa'`); got != 0 {
			t.Fatalf("%s recorded %d anomalies, want 0", event, got)
		}
		if got := e.tg.count() - before; got != 0 {
			t.Fatalf("%s sent %d messages, want 0", event, got)
		}
	}
}

func TestYooKassaWebhookReplayIsIdempotent(t *testing.T) {
	api := newYookassaWebhookAPIMock(t, http.StatusOK, "")
	e := newYooKassaWebhookEnv(t, api, nil)
	const buyer = int64(7401)
	orderID := placeRUBOrder(e, buyer)
	api.setBody(yookassaRefetchJSON("pay_1", "succeeded", "1849.08", true, orderID))

	body := yookassaNotificationBody("payment.succeeded", "pay_1")
	if rec := postYooKassaWebhook(t, e.bot, body); rec.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	before := e.tg.count()
	if rec := postYooKassaWebhook(t, e.bot, body); rec.Code != http.StatusOK {
		t.Fatalf("replay status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if got := api.count(); got != 2 {
		t.Fatalf("refetch calls = %d, want 2 (every notification is re-verified)", got)
	}
	if got := e.qInt(`SELECT stock FROM products WHERE id = ?`, e.prodReg); got != 4 {
		t.Fatalf("stock after replay = %d, want 4 (decremented once)", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM loyalty_txs WHERE user_id = ?`, e.userDBID(buyer)); got != 1 {
		t.Fatalf("loyalty_txs after replay = %d, want 1", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_attempts
		WHERE provider='yookassa' AND external_id='pay_1'`); got != 1 {
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

func TestYooKassaWebhookUnconfiguredIsInert(t *testing.T) {
	e := newE2EEnv(t) // no YooKassa credentials
	const buyer = int64(7501)
	e.cmd(buyer, "/start", "en")
	orderID := e.placeOrder(buyer, e.prodReg, "")

	before := e.tg.count()
	rec := postYooKassaWebhook(t, e.bot, yookassaNotificationBody("payment.succeeded", "pay_1"))

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

func TestYooKassaWebhookGarbageBodyQuarantines(t *testing.T) {
	api := newYookassaWebhookAPIMock(t, http.StatusOK, "{}")
	e := newYooKassaWebhookEnv(t, api, nil)

	rec := postYooKassaWebhook(t, e.bot, "not json at all")
	if rec.Code != http.StatusOK {
		t.Fatalf("garbage status = %d, want 200 so YooKassa stops retrying (%s)", rec.Code, rec.Body.String())
	}

	var count int
	var reason, rawPayload, externalID, actor string
	if err := e.db.Conn().QueryRow(`SELECT COUNT(*), reason, raw_payload, external_id, COALESCE(actor, '')
		FROM payment_anomalies WHERE provider='yookassa'`).Scan(&count, &reason, &rawPayload, &externalID, &actor); err != nil {
		t.Fatalf("no yookassa anomaly recorded for garbage body: %v", err)
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
	// 4.15 durable actor: the digest quarantine carries the yookassa
	// webhook's ingress identity.
	if actor != "webhook:yookassa" {
		t.Fatalf("anomaly actor = %q, want webhook:yookassa", actor)
	}
	if got := api.count(); got != 0 {
		t.Fatalf("garbage body triggered %d API calls, want 0", got)
	}

	// Oversized body: rejected by MaxBytesReader before parsing.
	oversized := strings.Repeat("a", 1<<20+1)
	rec = postYooKassaWebhook(t, e.bot, oversized)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_anomalies WHERE provider='yookassa'`); got != 1 {
		t.Fatalf("anomalies after oversized body = %d, want still 1", got)
	}
}

// TestYooKassaWebhookFactlessEnvelopeRecordsMissingPaymentID pins the
// factless-envelope branch: a body that parses cleanly into a valid envelope
// but carries NO payment id is not a parse failure — its anomaly reason is
// webhook_missing_payment_id (the deliberate roadmap-4.6 retag from the
// sweep-era webhook_parse_failure). True parse failures keep
// webhook_parse_failure, pinned by TestYooKassaWebhookGarbageBodyQuarantines.
func TestYooKassaWebhookFactlessEnvelopeRecordsMissingPaymentID(t *testing.T) {
	api := newYookassaWebhookAPIMock(t, http.StatusOK, "{}")
	e := newYooKassaWebhookEnv(t, api, nil)

	rec := postYooKassaWebhook(t, e.bot,
		`{"event":"payment.waiting_for_capture","object":{"status":"waiting_for_capture"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("factless status = %d, want 200 (valid envelope, no payment id: nothing to do) (%s)",
			rec.Code, rec.Body.String())
	}

	var count int
	var reason, rawPayload, externalID string
	if err := e.db.Conn().QueryRow(`SELECT COUNT(*), reason, raw_payload, external_id
		FROM payment_anomalies WHERE provider='yookassa'`).Scan(&count, &reason, &rawPayload, &externalID); err != nil {
		t.Fatalf("no yookassa anomaly recorded for the factless envelope: %v", err)
	}
	if count != 1 || reason != "webhook_missing_payment_id" {
		t.Fatalf("anomaly count=%d reason=%q, want 1 / webhook_missing_payment_id", count, reason)
	}
	if !strings.HasPrefix(rawPayload, "sha256:") {
		t.Fatalf("raw_payload was not safely digested: %q", rawPayload)
	}
	if externalID != "" {
		t.Fatalf("external_id = %q, want empty (the envelope carried no payment id)", externalID)
	}
	if got := api.count(); got != 0 {
		t.Fatalf("factless body triggered %d API calls, want 0 (nothing to refetch)", got)
	}
}

func TestYooKassaWebhookInvalidReceiptQuarantines(t *testing.T) {
	api := newYookassaWebhookAPIMock(t, http.StatusOK, "")
	e := newYooKassaWebhookEnv(t, api, nil)
	const buyer = int64(7701)
	orderID := placeRUBOrder(e, buyer)

	// The refetched payment is terminal and paid, but carries no order
	// reference: no receipt can be built, so the provider fact is quarantined
	// (webhook_invalid_receipt) and ACKed — never settled.
	api.setBody(yookassaRefetchNoOrderJSON("pay_noref", "1849.08"))

	before := e.tg.count()
	rec := postYooKassaWebhook(t, e.bot, yookassaNotificationBody("payment.succeeded", "pay_noref"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the invalid receipt is durably quarantined) (%s)", rec.Code, rec.Body.String())
	}
	if got := api.count(); got != 1 {
		t.Fatalf("refetch calls = %d, want 1", got)
	}
	var reason, externalID string
	var proposed int64
	var actor string
	if err := e.db.Conn().QueryRow(`SELECT reason, external_id, proposed_order_id, COALESCE(actor, '')
		FROM payment_anomalies WHERE provider='yookassa'`).Scan(&reason, &externalID, &proposed, &actor); err != nil {
		t.Fatalf("no yookassa anomaly recorded for the invalid receipt: %v", err)
	}
	if reason != "webhook_invalid_receipt" || externalID != "pay_noref" || proposed != 0 {
		t.Fatalf("anomaly reason=%q external_id=%q proposed_order_id=%d, want webhook_invalid_receipt / pay_noref / 0",
			reason, externalID, proposed)
	}
	// 4.15 durable actor: the invalid-receipt quarantine carries the
	// yookassa webhook's ingress identity.
	if actor != "webhook:yookassa" {
		t.Fatalf("anomaly actor = %q, want webhook:yookassa", actor)
	}
	if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPending {
		t.Fatalf("order status = %q, want pending (nothing settled)", got)
	}
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id = ?`, orderID); got != storage.PaymentStatePending {
		t.Fatalf("payment_state = %q, want pending (an orphan fact touches no order)", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_attempts WHERE provider='yookassa'`); got != 0 {
		t.Fatalf("payment attempts = %d, want 0", got)
	}
	if got := e.tg.count() - before; got != 0 {
		t.Fatalf("messages sent = %d, want 0:\n%s", got, dumpCalls(e.tg.since(before)))
	}
}

func TestYooKassaWebhookQuarantineFailureWithholdsACK(t *testing.T) {
	api := newYookassaWebhookAPIMock(t, http.StatusOK, "")
	e := newYooKassaWebhookEnv(t, api, nil)
	const buyer = int64(7702)
	orderID := placeRUBOrder(e, buyer)
	api.setBody(yookassaRefetchNoOrderJSON("pay_noref", "1849.08"))

	// The quarantine write itself fails: the handler must NOT acknowledge a
	// provider fact it could not durably record — 500 so YooKassa retries.
	e.failAnomalyRecording(errors.New("injected quarantine write failure"))

	before := e.tg.count()
	rec := postYooKassaWebhook(t, e.bot, yookassaNotificationBody("payment.succeeded", "pay_noref"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 so YooKassa retries (%s)", rec.Code, rec.Body.String())
	}
	if got := api.count(); got != 1 {
		t.Fatalf("refetch calls = %d, want 1", got)
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
	if got := e.qInt(`SELECT COUNT(*) FROM payment_attempts WHERE provider='yookassa'`); got != 0 {
		t.Fatalf("payment attempts = %d, want 0", got)
	}
	if got := e.tg.count() - before; got != 0 {
		t.Fatalf("messages sent = %d, want 0:\n%s", got, dumpCalls(e.tg.since(before)))
	}
}

func TestYooKassaWebhookRefetchFailureWithholdsACK(t *testing.T) {
	api := newYookassaWebhookAPIMock(t, http.StatusInternalServerError, "")
	e := newYooKassaWebhookEnv(t, api, nil)
	const buyer = int64(7601)
	orderID := placeRUBOrder(e, buyer)

	before := e.tg.count()
	rec := postYooKassaWebhook(t, e.bot, yookassaNotificationBody("payment.succeeded", "pay_1"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 so YooKassa retries later (%s)", rec.Code, rec.Body.String())
	}
	if got := api.count(); got != 1 {
		t.Fatalf("refetch calls = %d, want 1", got)
	}
	if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPending {
		t.Fatalf("order status = %q, want pending", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_attempts WHERE provider='yookassa'`); got != 0 {
		t.Fatalf("payment attempts = %d, want 0", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_anomalies WHERE provider='yookassa'`); got != 0 {
		t.Fatalf("anomalies = %d, want 0 (a transient refetch failure is not a fact)", got)
	}
	if got := e.tg.count() - before; got != 0 {
		t.Fatalf("messages sent = %d, want 0:\n%s", got, dumpCalls(e.tg.since(before)))
	}
}
