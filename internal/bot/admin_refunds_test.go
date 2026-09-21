package bot

// Admin /refund tests. Orders are seeded through the real storage fact path
// (UpdateOrderStatusWithPaymentFact on the e2eEnv SQLite fixture) so refund
// previews find genuine parent captures, then driven through the production
// router with the fake Telegram API. Provider APIs are mocked per rail:
// stripe/yookassa via SetBaseURL + httptest (pinning request bodies AND the
// deterministic idempotency keys), stars via the fake Telegram's
// refundStarPayment recording, balance via the real balance store.
// Assertions target provider call counts and params, ledger refund rows and
// audits, order payment state, and the rendered messages.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/storage"
)

const (
	refundBuyer         = int64(7001)
	refundStarsCharge   = "tg_charge_refund_1"
	refundStripeSession = "cs_test_refund_1"
	refundStripeIntent  = "pi_test_refund_1"
	refundYooPayment    = "pay_test_refund_1"
)

// Fixture money: TotalUSD 12.50 → 1250 cents (stripe/balance/crypto/
// nowpayments), TotalStars 500 → 500 minor XTR, TotalRUB 1156.25 → 115625
// kopecks, TotalTonNano 2.5 TON.
const (
	refundFullUSD        = int64(1250)
	refundFullStars      = int64(500)
	refundFullRUB        = int64(115625)
	refundFullTonNano    = int64(2_500_000_000)
	refundStripeRefundID = "re_test_refund_1"
	refundYooRefundID    = "rf_test_refund_1"
)

// seedRefundOrder creates an order for refundBuyer and settles it through the
// real fact path with the rail's exact frozen money, so the ledger preview
// finds a succeeded parent capture. paymentID overrides the capture identity;
// the balance rail derives its deterministic "balance:<orderID>" id when
// paymentID is empty.
func seedRefundOrder(t *testing.T, e *e2eEnv, provider, paymentID string) int64 {
	t.Helper()
	ctx := context.Background()
	store := storage.NewSQLOrderStore(e.db)
	orderID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: refundBuyer, TotalUSD: 12.50, TotalStars: 500,
		TotalRUB: 1156.25, TotalTonNano: refundFullTonNano,
		Status: storage.OrderStatusPending,
	}, []storage.OrderItem{{ProductID: e.prodReg, ProductName: "Tee", Quantity: 1, PriceUSD: 12.50}})
	if err != nil {
		t.Fatalf("create refund order: %v", err)
	}
	if provider == storage.PaymentMethodBalance && paymentID == "" {
		paymentID = fmt.Sprintf("balance:%d", orderID)
	}
	occurredAt := time.Now().Add(-time.Hour).UTC()
	var fact storage.PaymentFact
	switch provider {
	case storage.PaymentMethodStars:
		fact = storage.PaymentFact{Provider: provider, ExternalID: paymentID, PayerID: refundBuyer,
			AmountMinor: refundFullStars, Currency: "XTR", Scale: 0, OccurredAt: occurredAt}
	case storage.PaymentMethodStripe:
		fact = storage.PaymentFact{Provider: provider, ExternalID: paymentID, PayerID: 0,
			AmountMinor: refundFullUSD, Currency: "USD", Scale: 2, OccurredAt: occurredAt}
	case storage.PaymentMethodYooKassa:
		fact = storage.PaymentFact{Provider: provider, ExternalID: paymentID, PayerID: 0,
			AmountMinor: refundFullRUB, Currency: "RUB", Scale: 2, OccurredAt: occurredAt}
	case storage.PaymentMethodBalance:
		fact = storage.PaymentFact{Provider: provider, ExternalID: paymentID, PayerID: refundBuyer,
			AmountMinor: refundFullUSD, Currency: "USD", Scale: 2, OccurredAt: occurredAt}
	case storage.PaymentMethodCrypto:
		fact = storage.PaymentFact{Provider: provider, ExternalID: paymentID, PayerID: refundBuyer,
			AmountMinor: refundFullUSD, Currency: "USD", Scale: 2, OccurredAt: occurredAt}
	case storage.PaymentMethodTON:
		fact = storage.PaymentFact{Provider: provider, ExternalID: paymentID, PayerID: 0,
			AmountMinor: refundFullTonNano, Currency: "TON", Scale: 9, OccurredAt: occurredAt}
	case storage.PaymentMethodNowpayments:
		fact = storage.PaymentFact{Provider: provider, ExternalID: paymentID, PayerID: 0,
			AmountMinor: refundFullUSD, Currency: "USD", Scale: 2, OccurredAt: occurredAt}
	default:
		t.Fatalf("seedRefundOrder: unsupported provider %q", provider)
	}
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, orderID,
		storage.OrderStatusPending, storage.OrderStatusPaid, fact); err != nil {
		t.Fatalf("settle %s order: %v", provider, err)
	}
	return orderID
}

// refundCBData builds the confirm callback for one order/amount pair.
func refundCBData(orderID, amountMinor int64) string {
	return fmt.Sprintf("admin:refund:%d:%d", orderID, amountMinor)
}

// --- provider mocks ---------------------------------------------------------

// stripeRefundMock fakes the two Stripe routes the refund flow touches:
// GET /v1/checkout/sessions/{id} (payment_intent resolution) and
// POST /v1/refunds (the money-out). Every refund request is recorded with
// its form and Idempotency-Key header so tests pin the deterministic key
// and the exact call count.
type stripeRefundMock struct {
	mu              sync.Mutex
	srv             *httptest.Server
	sessionHits     int
	refundHits      int
	refundForms     []url.Values
	idempotencyKeys []string
	failRefund      bool
	refundStatus    string
}

func newStripeRefundMock(t *testing.T) *stripeRefundMock {
	t.Helper()
	m := &stripeRefundMock{refundStatus: "succeeded"}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/"):
			m.mu.Lock()
			m.sessionHits++
			m.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":             strings.TrimPrefix(r.URL.Path, "/v1/checkout/sessions/"),
				"status":         "complete",
				"payment_status": "paid",
				"amount_total":   refundFullUSD,
				"currency":       "usd",
				"payment_intent": refundStripeIntent,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/refunds":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse refund form: %v", err)
			}
			m.mu.Lock()
			m.refundHits++
			m.refundForms = append(m.refundForms, r.PostForm)
			m.idempotencyKeys = append(m.idempotencyKeys, r.Header.Get("Idempotency-Key"))
			fail, status := m.failRefund, m.refundStatus
			m.mu.Unlock()
			if fail {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"type":"card_error","code":"charge_already_refunded","message":"mock stripe refund failure"}}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": refundStripeRefundID, "status": status})
		default:
			t.Errorf("stripe refund mock: unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"unrecognized route"}}`))
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *stripeRefundMock) setFailRefund(v bool) {
	m.mu.Lock()
	m.failRefund = v
	m.mu.Unlock()
}

func (m *stripeRefundMock) stats() (sessions, refunds int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessionHits, m.refundHits
}

func (m *stripeRefundMock) keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.idempotencyKeys...)
}

func (m *stripeRefundMock) forms() []url.Values {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]url.Values(nil), m.refundForms...)
}

// yookassaRefundMock fakes POST /v3/refunds, recording every JSON body and
// Idempotence-Key header.
type yookassaRefundMock struct {
	mu              sync.Mutex
	srv             *httptest.Server
	refundHits      int
	refundBodies    []map[string]any
	idempotencyKeys []string
	failRefund      bool
	refundStatus    string
}

func newYookassaRefundMock(t *testing.T) *yookassaRefundMock {
	t.Helper()
	m := &yookassaRefundMock{refundStatus: "succeeded"}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost || r.URL.Path != "/v3/refunds" {
			t.Errorf("yookassa refund mock: unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"not_found","description":"unexpected request"}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("parse refund body %q: %v", raw, err)
		}
		m.mu.Lock()
		m.refundHits++
		m.refundBodies = append(m.refundBodies, body)
		m.idempotencyKeys = append(m.idempotencyKeys, r.Header.Get("Idempotence-Key"))
		fail, status := m.failRefund, m.refundStatus
		m.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":"invalid_parameter","description":"mock yookassa refund failure"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": refundYooRefundID, "status": status})
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *yookassaRefundMock) setFailRefund(v bool) {
	m.mu.Lock()
	m.failRefund = v
	m.mu.Unlock()
}

func (m *yookassaRefundMock) stats() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.refundHits
}

func (m *yookassaRefundMock) keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.idempotencyKeys...)
}

func (m *yookassaRefundMock) bodies() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]map[string]any(nil), m.refundBodies...)
}

// failingRefundLedger wraps the production ledger and fails ONLY
// IngestProviderRefund while flagged — the payreview failing-store pattern
// for the "provider refund executed, ledger recording failed" recovery path.
type failingRefundLedger struct {
	payLedgerStore
	mu         sync.Mutex
	failIngest bool
	ingestHits int
}

func (f *failingRefundLedger) IngestProviderRefund(ctx context.Context, refund storage.Refund, audit storage.PaymentIngressAudit) error {
	f.mu.Lock()
	f.ingestHits++
	fail := f.failIngest
	f.mu.Unlock()
	if fail {
		return errors.New("mock ledger ingest failure")
	}
	return f.payLedgerStore.IngestProviderRefund(ctx, refund, audit)
}

func (f *failingRefundLedger) setFailIngest(v bool) {
	f.mu.Lock()
	f.failIngest = v
	f.mu.Unlock()
}

// --- shared assertion helpers ------------------------------------------------

func assertRefundRow(t *testing.T, e *e2eEnv, orderID int64, provider, externalID, paymentExternalID string, payer, amount int64, currency string, scale int) {
	t.Helper()
	var gotExt, gotPayExt, gotCur, gotStatus string
	var gotPayer, gotAmount int64
	var gotScale int
	if err := e.db.Conn().QueryRow(`SELECT external_id, payment_external_id, payer_id, amount_minor, currency, scale, status
		FROM refunds WHERE order_id=? AND provider=?`, orderID, provider).
		Scan(&gotExt, &gotPayExt, &gotPayer, &gotAmount, &gotCur, &gotScale, &gotStatus); err != nil {
		t.Fatalf("refund row: %v", err)
	}
	if gotExt != externalID || gotPayExt != paymentExternalID || gotPayer != payer ||
		gotAmount != amount || gotCur != currency || gotScale != scale || gotStatus != "succeeded" {
		t.Fatalf("refund row = {%s %s %d %d %s %d %s}, want {%s %s %d %d %s %d succeeded}",
			gotExt, gotPayExt, gotPayer, gotAmount, gotCur, gotScale, gotStatus,
			externalID, paymentExternalID, payer, amount, currency, scale)
	}
}

func assertRefundAudit(t *testing.T, e *e2eEnv, orderID int64, want int) {
	t.Helper()
	if got := e.qInt(`SELECT COUNT(*) FROM payment_ingress_audits
		WHERE order_id=? AND event_kind='refunded' AND actor='admin:9001' AND reason='admin /refund'`, orderID); got != int64(want) {
		t.Fatalf("refund audits = %d, want %d", got, want)
	}
}

// tgCountMethod counts recorded calls of one Bot API method.
func tgCountMethod(calls []tgCall, method string) int {
	n := 0
	for _, c := range calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

// --- non-admin / usage / parse legs ------------------------------------------

func TestAdminRefundNonAdminInert(t *testing.T) {
	e := newE2EEnv(t)
	// Command: fully inert (no ack, no message).
	if calls := e.cmd(1234, "/refund 1", "en"); len(calls) != 0 {
		t.Fatalf("non-admin /refund produced calls=%+v", calls)
	}
	// Callback: acked at the router level, never acted upon.
	if calls := e.cb(1234, refundCBData(1, 100), "en"); tgSends(calls) {
		t.Fatalf("non-admin refund callback acted: %+v", calls)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM refunds`); got != 0 {
		t.Fatalf("non-admin flow wrote %d refunds", got)
	}
}

func TestAdminRefundUsageAndMalformedArgs(t *testing.T) {
	e := newE2EEnv(t)
	want := e.bot.t("en", "admin_refund_usage")
	for _, args := range []string{"", "abc", "0", "42 xyz", "42 -5.00", "42 0", "42 1.0 extra"} {
		calls := e.cmd(e2eAdminID, "/refund "+args, "en")
		if got := tgText(calls); got != want {
			t.Fatalf("/refund %q text = %q, want usage %q", args, got, want)
		}
	}
	// Unknown order: the house not-found message.
	calls := e.cmd(e2eAdminID, "/refund 424242", "en")
	if got, want := tgText(calls), fmt.Sprintf(e.bot.t("en", "admin_order_not_found"), int64(424242)); got != want {
		t.Fatalf("unknown order text = %q, want %q", got, want)
	}
}

func TestAdminRefundRejectsNonRefundableStates(t *testing.T) {
	e := newE2EEnv(t)
	// Pending order: never refundable.
	_, pendingOrder := seedPayReviewOrder(t, e, refundBuyer, 500)
	calls := e.cmd(e2eAdminID, fmt.Sprintf("/refund %d", pendingOrder), "en")
	want := e.bot.i18n.Tf("en", "admin_refund_not_paid", pendingOrder, "pending/pending")
	if got := tgText(calls); got != want {
		t.Fatalf("pending text = %q, want %q", got, want)
	}
	// Fully refunded order: the settled gate is closed.
	orderID := seedRefundOrder(t, e, storage.PaymentMethodBalance, "")
	e.cmd(refundBuyer, "/start", "en")
	e.cb(e2eAdminID, refundCBData(orderID, refundFullUSD), "en")
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id=?`, orderID); got != storage.PaymentStateRefunded {
		t.Fatalf("state after first refund = %s", got)
	}
	calls = e.cmd(e2eAdminID, fmt.Sprintf("/refund %d", orderID), "en")
	want = e.bot.i18n.Tf("en", "admin_refund_not_paid", orderID, "paid/refunded")
	if got := tgText(calls); got != want {
		t.Fatalf("refunded text = %q, want %q", got, want)
	}
}

// --- preview cards ------------------------------------------------------------

func TestAdminRefundPreviewCards(t *testing.T) {
	t.Run("stripe full and partial", func(t *testing.T) {
		e := newE2EEnvWithConfig(t, enableStripe)
		mock := newStripeRefundMock(t)
		e.bot.stripe.SetBaseURL(mock.srv.URL + "/v1")
		orderID := seedRefundOrder(t, e, storage.PaymentMethodStripe, refundStripeSession)

		calls := e.cmd(e2eAdminID, fmt.Sprintf("/refund %d", orderID), "en")
		text := tgText(calls)
		want := e.bot.i18n.Tf("en", "admin_refund_card", orderID, "stripe", "12.50 USD")
		if text != want {
			t.Fatalf("card text = %q, want %q", text, want)
		}
		if !strings.Contains(calls[0].markup(), refundCBData(orderID, refundFullUSD)) {
			t.Fatalf("card markup = %s, want confirm button", calls[0].markup())
		}
		// The card resolved the payment intent but NEVER touched the refund endpoint,
		// and it wrote nothing.
		if s, r := mock.stats(); r != 0 {
			t.Fatalf("card executed %d refunds (sessions=%d)", r, s)
		}
		if got := e.qInt(`SELECT COUNT(*) FROM refunds`); got != 0 {
			t.Fatalf("card wrote %d refunds", got)
		}

		// Partial decimal amount → minor-unit button.
		calls = e.cmd(e2eAdminID, fmt.Sprintf("/refund %d 5.25", orderID), "en")
		if !strings.Contains(calls[0].markup(), refundCBData(orderID, 525)) {
			t.Fatalf("partial card markup = %s", calls[0].markup())
		}
		if got, want := tgText(calls), e.bot.i18n.Tf("en", "admin_refund_card", orderID, "stripe", "5.25 USD"); got != want {
			t.Fatalf("partial card text = %q, want %q", got, want)
		}
		// Amount above the frozen total → conflict, no card.
		calls = e.cmd(e2eAdminID, fmt.Sprintf("/refund %d 99.00", orderID), "en")
		if got, want := tgText(calls), e.bot.t("en", "admin_refund_conflict"); got != want {
			t.Fatalf("over-amount text = %q, want %q", got, want)
		}
	})

	t.Run("stripe delivered order is refundable", func(t *testing.T) {
		e := newE2EEnvWithConfig(t, enableStripe)
		mock := newStripeRefundMock(t)
		e.bot.stripe.SetBaseURL(mock.srv.URL + "/v1")
		orderID := seedRefundOrder(t, e, storage.PaymentMethodStripe, refundStripeSession)
		store := storage.NewSQLOrderStore(e.db)
		if err := store.UpdateOrderStatus(context.Background(), orderID,
			storage.OrderStatusPaid, storage.OrderStatusDelivered, "", ""); err != nil {
			t.Fatal(err)
		}
		calls := e.cmd(e2eAdminID, fmt.Sprintf("/refund %d", orderID), "en")
		if !strings.Contains(calls[0].markup(), refundCBData(orderID, refundFullUSD)) {
			t.Fatalf("delivered card markup = %s", calls[0].markup())
		}
	})

	t.Run("yookassa", func(t *testing.T) {
		e := newE2EEnvWithConfig(t, enableYooKassa)
		mock := newYookassaRefundMock(t)
		e.bot.yookassa.SetBaseURL(mock.srv.URL + "/v3")
		orderID := seedRefundOrder(t, e, storage.PaymentMethodYooKassa, refundYooPayment)

		calls := e.cmd(e2eAdminID, fmt.Sprintf("/refund %d", orderID), "en")
		want := e.bot.i18n.Tf("en", "admin_refund_card", orderID, "yookassa", "1156.25 RUB")
		if got := tgText(calls); got != want {
			t.Fatalf("card text = %q, want %q", got, want)
		}
		if !strings.Contains(calls[0].markup(), refundCBData(orderID, refundFullRUB)) {
			t.Fatalf("card markup = %s", calls[0].markup())
		}
		if mock.stats() != 0 {
			t.Fatal("card executed a refund")
		}
	})

	t.Run("stars full-only", func(t *testing.T) {
		e := newE2EEnv(t)
		orderID := seedRefundOrder(t, e, storage.PaymentMethodStars, refundStarsCharge)

		// Explicit full amount is accepted...
		calls := e.cmd(e2eAdminID, fmt.Sprintf("/refund %d 500", orderID), "en")
		if !strings.Contains(calls[0].markup(), refundCBData(orderID, refundFullStars)) {
			t.Fatalf("stars card markup = %s", calls[0].markup())
		}
		want := e.bot.i18n.Tf("en", "admin_refund_card", orderID, "stars", "500 XTR")
		if got := tgText(calls); got != want {
			t.Fatalf("stars card text = %q, want %q", got, want)
		}
		// ...anything else is the partial-stars rejection.
		calls = e.cmd(e2eAdminID, fmt.Sprintf("/refund %d 100", orderID), "en")
		want = e.bot.i18n.Tf("en", "admin_refund_partial_stars", orderID, refundFullStars)
		if got := tgText(calls); got != want {
			t.Fatalf("partial stars text = %q, want %q", got, want)
		}
	})

	t.Run("balance", func(t *testing.T) {
		e := newE2EEnv(t)
		orderID := seedRefundOrder(t, e, storage.PaymentMethodBalance, "")
		calls := e.cmd(e2eAdminID, fmt.Sprintf("/refund %d", orderID), "en")
		want := e.bot.i18n.Tf("en", "admin_refund_card", orderID, "balance", "12.50 USD")
		if got := tgText(calls); got != want {
			t.Fatalf("card text = %q, want %q", got, want)
		}
		if !strings.Contains(calls[0].markup(), refundCBData(orderID, refundFullUSD)) {
			t.Fatalf("card markup = %s", calls[0].markup())
		}
	})

	t.Run("manual rails are informational", func(t *testing.T) {
		for _, tc := range []struct {
			provider  string
			paymentID string
		}{
			{storage.PaymentMethodCrypto, "crypto_invoice_1"},
			{storage.PaymentMethodTON, "ton_tx_1"},
			{storage.PaymentMethodNowpayments, "np_invoice_1"},
		} {
			e := newE2EEnv(t)
			orderID := seedRefundOrder(t, e, tc.provider, tc.paymentID)
			calls := e.cmd(e2eAdminID, fmt.Sprintf("/refund %d", orderID), "en")
			text := tgText(calls)
			// The card tells the TRUTH: it never quotes the stars-only
			// ingest CLI (which cannot record this rail's refund, ever) and
			// names the dashboard execution plus the known in-ledger
			// recording follow-up.
			want := e.bot.i18n.Tf("en", "admin_refund_manual", tc.provider)
			if text != want {
				t.Fatalf("%s card text = %q, want %q", tc.provider, text, want)
			}
			if strings.Contains(text, "ingest-stars") {
				t.Fatalf("%s card quotes the stars-only CLI: %q", tc.provider, text)
			}
			if !strings.Contains(text, "known follow-up") || !strings.Contains(text, "Telegram Stars only") {
				t.Fatalf("%s card misses the truthful follow-up wording: %q", tc.provider, text)
			}
			for _, c := range calls {
				if strings.Contains(c.markup(), "admin:refund:") {
					t.Fatalf("%s card offers a confirm button: %s", tc.provider, c.markup())
				}
			}
			if got := e.qInt(`SELECT COUNT(*) FROM refunds`); got != 0 {
				t.Fatalf("%s card wrote %d refunds", tc.provider, got)
			}
			// A crafted confirm callback for a manual rail stays inert: the
			// same truthful informational card re-renders, no ledger write.
			calls = e.cb(e2eAdminID, refundCBData(orderID, refundFullUSD), "en")
			if got := tgText(calls); got != want || strings.Contains(got, "ingest-stars") {
				t.Fatalf("%s confirm text = %q, want %q", tc.provider, got, want)
			}
			if got := e.qInt(`SELECT COUNT(*) FROM refunds`); got != 0 {
				t.Fatalf("%s confirm wrote %d refunds", tc.provider, got)
			}
		}
	})
}

// --- confirm e2e per executable rail -------------------------------------------

func TestAdminRefundConfirmStripeE2E(t *testing.T) {
	e := newE2EEnvWithConfig(t, enableStripe)
	mock := newStripeRefundMock(t)
	e.bot.stripe.SetBaseURL(mock.srv.URL + "/v1")
	orderID := seedRefundOrder(t, e, storage.PaymentMethodStripe, refundStripeSession)

	calls := e.cb(e2eAdminID, refundCBData(orderID, refundFullUSD), "en")

	// Provider called exactly once with the exact form and the DETERMINISTIC
	// idempotency key refund:<orderID>:<amountMinor>:<paymentID>.
	if _, r := mock.stats(); r != 1 {
		t.Fatalf("refund calls = %d, want 1", r)
	}
	wantKey := fmt.Sprintf("refund:%d:%d:%s", orderID, refundFullUSD, refundStripeSession)
	if keys := mock.keys(); len(keys) != 1 || keys[0] != wantKey {
		t.Fatalf("idempotency keys = %v, want [%s]", keys, wantKey)
	}
	form := mock.forms()[0]
	if form.Get("payment_intent") != refundStripeIntent || form.Get("amount") != "1250" ||
		form.Get("reason") != "requested_by_customer" {
		t.Fatalf("refund form = %v", form)
	}
	// Ledger: refund row (provider refund id, session as parent, order user as
	// payer, USD scale 2), audit attributed to the acting admin, order flipped.
	assertRefundRow(t, e, orderID, "stripe", refundStripeRefundID, refundStripeSession,
		refundBuyer, refundFullUSD, "USD", 2)
	assertRefundAudit(t, e, orderID, 1)
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id=?`, orderID); got != storage.PaymentStateRefunded {
		t.Fatalf("state = %s, want refunded", got)
	}
	want := e.bot.i18n.Tf("en", "admin_refund_done", refundStripeRefundID, orderID, storage.PaymentStateRefunded)
	if got := tgText(calls); got != want {
		t.Fatalf("done text = %q, want %q", got, want)
	}

	// Double-confirm replay: the recorded refund is found BEFORE any provider
	// call — the count stays pinned at 1 and no second row appears.
	calls = e.cb(e2eAdminID, refundCBData(orderID, refundFullUSD), "en")
	if _, r := mock.stats(); r != 1 {
		t.Fatalf("replay executed another refund: calls = %d", r)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM refunds WHERE order_id=?`, orderID); got != 1 {
		t.Fatalf("replay wrote %d refund rows", got)
	}
	if got := tgText(calls); got != want {
		t.Fatalf("replay text = %q, want done %q", got, want)
	}
}

func TestAdminRefundConfirmStripePartial(t *testing.T) {
	e := newE2EEnvWithConfig(t, enableStripe)
	mock := newStripeRefundMock(t)
	e.bot.stripe.SetBaseURL(mock.srv.URL + "/v1")
	orderID := seedRefundOrder(t, e, storage.PaymentMethodStripe, refundStripeSession)

	calls := e.cb(e2eAdminID, refundCBData(orderID, 525), "en")
	if _, r := mock.stats(); r != 1 {
		t.Fatalf("refund calls = %d, want 1", r)
	}
	if form := mock.forms()[0]; form.Get("amount") != "525" {
		t.Fatalf("partial refund form = %v", form)
	}
	wantKey := fmt.Sprintf("refund:%d:525:%s", orderID, refundStripeSession)
	if keys := mock.keys(); keys[0] != wantKey {
		t.Fatalf("partial idempotency key = %v, want %s", keys, wantKey)
	}
	assertRefundRow(t, e, orderID, "stripe", refundStripeRefundID, refundStripeSession,
		refundBuyer, 525, "USD", 2)
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id=?`, orderID); got != storage.PaymentStatePartiallyRefunded {
		t.Fatalf("state = %s, want partially_refunded", got)
	}
	want := e.bot.i18n.Tf("en", "admin_refund_done", refundStripeRefundID, orderID, storage.PaymentStatePartiallyRefunded)
	if got := tgText(calls); got != want {
		t.Fatalf("done text = %q, want %q", got, want)
	}
}

func TestAdminRefundConfirmYooKassaE2E(t *testing.T) {
	e := newE2EEnvWithConfig(t, enableYooKassa)
	mock := newYookassaRefundMock(t)
	e.bot.yookassa.SetBaseURL(mock.srv.URL + "/v3")
	orderID := seedRefundOrder(t, e, storage.PaymentMethodYooKassa, refundYooPayment)

	calls := e.cb(e2eAdminID, refundCBData(orderID, refundFullRUB), "en")
	if mock.stats() != 1 {
		t.Fatalf("refund calls = %d, want 1", mock.stats())
	}
	wantKey := fmt.Sprintf("refund:%d:%d:%s", orderID, refundFullRUB, refundYooPayment)
	if keys := mock.keys(); len(keys) != 1 || keys[0] != wantKey {
		t.Fatalf("idempotence keys = %v, want [%s]", keys, wantKey)
	}
	body := mock.bodies()[0]
	amount, _ := body["amount"].(map[string]any)
	if amount["value"] != "1156.25" || amount["currency"] != "rub" {
		t.Fatalf("refund amount = %v", body["amount"])
	}
	if body["payment_id"] != refundYooPayment {
		t.Fatalf("payment_id = %v", body["payment_id"])
	}
	if body["description"] != fmt.Sprintf("Order #%d refund", orderID) {
		t.Fatalf("description = %v", body["description"])
	}
	assertRefundRow(t, e, orderID, "yookassa", refundYooRefundID, refundYooPayment,
		refundBuyer, refundFullRUB, "RUB", 2)
	assertRefundAudit(t, e, orderID, 1)
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id=?`, orderID); got != storage.PaymentStateRefunded {
		t.Fatalf("state = %s, want refunded", got)
	}
	want := e.bot.i18n.Tf("en", "admin_refund_done", refundYooRefundID, orderID, storage.PaymentStateRefunded)
	if got := tgText(calls); got != want {
		t.Fatalf("done text = %q, want %q", got, want)
	}

	// Double-confirm replay: no second provider call, no second row.
	calls = e.cb(e2eAdminID, refundCBData(orderID, refundFullRUB), "en")
	if mock.stats() != 1 {
		t.Fatalf("replay executed another refund: calls = %d", mock.stats())
	}
	if got := e.qInt(`SELECT COUNT(*) FROM refunds WHERE order_id=?`, orderID); got != 1 {
		t.Fatalf("replay wrote %d refund rows", got)
	}
	if got := tgText(calls); got != want {
		t.Fatalf("replay text = %q, want done %q", got, want)
	}
}

func TestAdminRefundConfirmStarsE2E(t *testing.T) {
	e := newE2EEnv(t)
	orderID := seedRefundOrder(t, e, storage.PaymentMethodStars, refundStarsCharge)

	calls := e.cb(e2eAdminID, refundCBData(orderID, refundFullStars), "en")
	refunds := make([]tgCall, 0, 1)
	for _, c := range calls {
		if c.Method == "refundStarPayment" {
			refunds = append(refunds, c)
		}
	}
	if len(refunds) != 1 {
		t.Fatalf("refundStarPayment calls = %d, want 1 (%+v)", len(refunds), calls)
	}
	if got := refunds[0].Params.Get("user_id"); got != fmt.Sprintf("%d", refundBuyer) {
		t.Fatalf("user_id = %q", got)
	}
	if got := refunds[0].Params.Get("telegram_payment_charge_id"); got != refundStarsCharge {
		t.Fatalf("charge id = %q", got)
	}
	// Telegram returns no refund identity: the ledger's stars rule
	// (ExternalID == PaymentExternalID) makes the charge id the refund row id.
	assertRefundRow(t, e, orderID, "stars", refundStarsCharge, refundStarsCharge,
		refundBuyer, refundFullStars, "XTR", 0)
	assertRefundAudit(t, e, orderID, 1)
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id=?`, orderID); got != storage.PaymentStateRefunded {
		t.Fatalf("state = %s, want refunded", got)
	}
	want := e.bot.i18n.Tf("en", "admin_refund_done", refundStarsCharge, orderID, storage.PaymentStateRefunded)
	if got := tgText(calls); got != want {
		t.Fatalf("done text = %q, want %q", got, want)
	}

	// Double-confirm replay: Telegram is NOT called again.
	calls = e.cb(e2eAdminID, refundCBData(orderID, refundFullStars), "en")
	if tgCountMethod(calls, "refundStarPayment") != 0 {
		t.Fatalf("replay called refundStarPayment again: %+v", calls)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM refunds WHERE order_id=?`, orderID); got != 1 {
		t.Fatalf("replay wrote %d refund rows", got)
	}
}

func TestAdminRefundConfirmStarsRejectsPartialCallback(t *testing.T) {
	e := newE2EEnv(t)
	orderID := seedRefundOrder(t, e, storage.PaymentMethodStars, refundStarsCharge)
	// A crafted callback with a non-full amount is rejected at the confirm
	// gate: Telegram has no partial star refund.
	calls := e.cb(e2eAdminID, refundCBData(orderID, 100), "en")
	want := e.bot.i18n.Tf("en", "admin_refund_partial_stars", orderID, refundFullStars)
	if got := tgText(calls); got != want {
		t.Fatalf("partial confirm text = %q, want %q", got, want)
	}
	if tgCountMethod(calls, "refundStarPayment") != 0 {
		t.Fatal("partial confirm called refundStarPayment")
	}
	if got := e.qInt(`SELECT COUNT(*) FROM refunds`); got != 0 {
		t.Fatalf("partial confirm wrote %d refunds", got)
	}
}

func TestAdminRefundConfirmBalanceE2E(t *testing.T) {
	e := newE2EEnv(t)
	e.cmd(refundBuyer, "/start", "en") // the credit needs the users row
	orderID := seedRefundOrder(t, e, storage.PaymentMethodBalance, "")
	balances := storage.NewSQLBalanceStore(e.db.Conn())
	// Mirror the original settlement: a grant the buyer paid from, then the
	// order_payment debit — so the refund credit lands on a realistic balance.
	if _, err := balances.AdjustBalance(context.Background(), refundBuyer, 12.50, "grant", e2eAdminID); err != nil {
		t.Fatal(err)
	}
	if _, err := balances.AdjustBalance(context.Background(), refundBuyer, -12.50,
		fmt.Sprintf("order_payment:%d", orderID), 0); err != nil {
		t.Fatal(err)
	}

	calls := e.cb(e2eAdminID, refundCBData(orderID, refundFullUSD), "en")
	// Balance credited back through the real store, with the deterministic
	// order_refund audit row naming the acting admin.
	if got := e.qStr(`SELECT printf('%.2f', balance_usd) FROM users WHERE telegram_id=?`, refundBuyer); got != "12.50" {
		t.Fatalf("balance = %s, want 12.50", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM balance_txs WHERE type=? AND ref_id='9001'
		AND printf('%.2f', amount_usd)='12.50'`, fmt.Sprintf("order_refund:%d", orderID)); got != 1 {
		t.Fatalf("order_refund balance_txs = %d, want 1", got)
	}
	assertRefundRow(t, e, orderID, "balance", fmt.Sprintf("balance-refund:%d", orderID),
		fmt.Sprintf("balance:%d", orderID), refundBuyer, refundFullUSD, "USD", 2)
	assertRefundAudit(t, e, orderID, 1)
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id=?`, orderID); got != storage.PaymentStateRefunded {
		t.Fatalf("state = %s, want refunded", got)
	}
	want := e.bot.i18n.Tf("en", "admin_refund_done",
		fmt.Sprintf("balance-refund:%d", orderID), orderID, storage.PaymentStateRefunded)
	if got := tgText(calls); got != want {
		t.Fatalf("done text = %q, want %q", got, want)
	}

	// Double-confirm replay: the balance is NOT credited a second time.
	calls = e.cb(e2eAdminID, refundCBData(orderID, refundFullUSD), "en")
	if got := e.qStr(`SELECT printf('%.2f', balance_usd) FROM users WHERE telegram_id=?`, refundBuyer); got != "12.50" {
		t.Fatalf("balance after replay = %s, want 12.50", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM balance_txs WHERE type=?`, fmt.Sprintf("order_refund:%d", orderID)); got != 1 {
		t.Fatalf("replay wrote %d order_refund rows", got)
	}
	if got := tgText(calls); got != want {
		t.Fatalf("replay text = %q, want %q", got, want)
	}
}

// --- failure and recovery legs ---------------------------------------------------

func TestAdminRefundProviderFailureWritesNoLedger(t *testing.T) {
	e := newE2EEnvWithConfig(t, enableStripe)
	mock := newStripeRefundMock(t)
	e.bot.stripe.SetBaseURL(mock.srv.URL + "/v1")
	mock.setFailRefund(true)
	orderID := seedRefundOrder(t, e, storage.PaymentMethodStripe, refundStripeSession)

	calls := e.cb(e2eAdminID, refundCBData(orderID, refundFullUSD), "en")
	// Ordering ruling: provider failure → NO ledger write, order unchanged.
	want := e.bot.i18n.Tf("en", "admin_refund_provider_failed", orderID,
		"stripe: HTTP status 400: mock stripe refund failure")
	if got := tgText(calls); got != want {
		t.Fatalf("failure text = %q, want %q", got, want)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM refunds`); got != 0 {
		t.Fatalf("provider failure wrote %d refunds", got)
	}
	assertRefundAudit(t, e, orderID, 0)
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id=?`, orderID); got != storage.PaymentStateSettled {
		t.Fatalf("state = %s, want settled (unchanged)", got)
	}

	// A "failed" refund status is not an HTTP error but must not record either.
	mock.setFailRefund(false)
	mock.mu.Lock()
	mock.refundStatus = "failed"
	mock.mu.Unlock()
	calls = e.cb(e2eAdminID, refundCBData(orderID, refundFullUSD), "en")
	if got := e.qInt(`SELECT COUNT(*) FROM refunds`); got != 0 {
		t.Fatalf("failed-status refund wrote %d rows", got)
	}
	if text := tgText(calls); !strings.Contains(text, "failed") {
		t.Fatalf("failed-status text = %q", text)
	}
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id=?`, orderID); got != storage.PaymentStateSettled {
		t.Fatalf("state after failed status = %s", got)
	}
}

func TestAdminRefundLedgerFailureAfterProviderSuccessStripe(t *testing.T) {
	e := newE2EEnvWithConfig(t, enableStripe)
	mock := newStripeRefundMock(t)
	e.bot.stripe.SetBaseURL(mock.srv.URL + "/v1")
	orderID := seedRefundOrder(t, e, storage.PaymentMethodStripe, refundStripeSession)
	ledger := &failingRefundLedger{payLedgerStore: e.bot.payLedger, failIngest: true}
	e.bot.payLedger = ledger

	calls := e.cb(e2eAdminID, refundCBData(orderID, refundFullUSD), "en")
	// The refund EXECUTED but the ledger recording failed: loud re-run message,
	// no refund row, order unchanged.
	want := e.bot.i18n.Tf("en", "admin_refund_ledger_failed_rerun",
		refundStripeRefundID, orderID, "mock ledger ingest failure", orderID, "12.50")
	if got := tgText(calls); got != want {
		t.Fatalf("loud text = %q, want %q", got, want)
	}
	// The message carries the EXACT copy-pasteable re-run command, amount
	// included — an amount-less re-run of a partial refund would default to
	// full and desync the books.
	if text := tgText(calls); !strings.Contains(text, fmt.Sprintf("/refund %d 12.50", orderID)) {
		t.Fatalf("loud text misses the exact re-run command: %q", text)
	}
	if _, r := mock.stats(); r != 1 {
		t.Fatalf("provider calls = %d, want exactly 1", r)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM refunds`); got != 0 {
		t.Fatalf("failed ingest wrote %d refunds", got)
	}

	// Recovery: re-run the SAME confirm with the ledger healed. The provider is
	// called again with the SAME deterministic key — in production Stripe
	// dedupes it into the original refund (a safe no-op) — and the ledger
	// record completes.
	ledger.setFailIngest(false)
	calls = e.cb(e2eAdminID, refundCBData(orderID, refundFullUSD), "en")
	wantKey := fmt.Sprintf("refund:%d:%d:%s", orderID, refundFullUSD, refundStripeSession)
	keys := mock.keys()
	if len(keys) != 2 || keys[0] != wantKey || keys[1] != wantKey {
		t.Fatalf("idempotency keys = %v, want %s twice", keys, wantKey)
	}
	assertRefundRow(t, e, orderID, "stripe", refundStripeRefundID, refundStripeSession,
		refundBuyer, refundFullUSD, "USD", 2)
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id=?`, orderID); got != storage.PaymentStateRefunded {
		t.Fatalf("state after recovery = %s", got)
	}
	want = e.bot.i18n.Tf("en", "admin_refund_done", refundStripeRefundID, orderID, storage.PaymentStateRefunded)
	if got := tgText(calls); got != want {
		t.Fatalf("recovery text = %q, want %q", got, want)
	}
}

// TestAdminRefundLedgerFailureAfterProviderSuccessStars pins the rail-aware
// recovery guidance (review Minor 6): a /refund re-run can NEVER record a
// stars refund — Telegram rejects the repeat refundStarPayment and the flow
// would falsely report "no refund, order unchanged" while the money is out —
// so the message must instead quote the ingest-stars --kind refund CLI, which
// records the refund with Telegram's authoritative transaction.
func TestAdminRefundLedgerFailureAfterProviderSuccessStars(t *testing.T) {
	e := newE2EEnv(t)
	orderID := seedRefundOrder(t, e, storage.PaymentMethodStars, refundStarsCharge)
	ledger := &failingRefundLedger{payLedgerStore: e.bot.payLedger, failIngest: true}
	e.bot.payLedger = ledger

	calls := e.cb(e2eAdminID, refundCBData(orderID, refundFullStars), "en")
	// The money LEFT at Telegram exactly once...
	if got := tgCountMethod(calls, "refundStarPayment"); got != 1 {
		t.Fatalf("refundStarPayment calls = %d, want 1", got)
	}
	// ...and the message tells the truth: the stars-specific recovery with
	// the ingest-stars CLI line, NOT the impossible re-run promise.
	want := e.bot.i18n.Tf("en", "admin_refund_ledger_failed_stars",
		refundStarsCharge, orderID, "mock ledger ingest failure", refundCLIRecordLine)
	if got := tgText(calls); got != want {
		t.Fatalf("loud text = %q, want %q", got, want)
	}
	if !strings.Contains(tgText(calls), "ingest-stars --kind refund") {
		t.Fatalf("stars ledger-failure text misses the CLI recovery line: %q", tgText(calls))
	}
	if got := e.qInt(`SELECT COUNT(*) FROM refunds`); got != 0 {
		t.Fatalf("failed ingest wrote %d refunds", got)
	}
	assertRefundAudit(t, e, orderID, 0)
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id=?`, orderID); got != storage.PaymentStateSettled {
		t.Fatalf("state = %s, want settled (unchanged — money out, unrecorded)", got)
	}
}

// TestAdminRefundLedgerFailureAfterProviderSuccessBalance pins the amendment's
// recovery guarantee on the rail with NO provider-side dedup: the re-run after
// a ledger failure must find the deterministic order_refund audit row and skip
// the credit — money is minted exactly once.
func TestAdminRefundLedgerFailureAfterProviderSuccessBalance(t *testing.T) {
	e := newE2EEnv(t)
	e.cmd(refundBuyer, "/start", "en")
	orderID := seedRefundOrder(t, e, storage.PaymentMethodBalance, "")
	balances := storage.NewSQLBalanceStore(e.db.Conn())
	if _, err := balances.AdjustBalance(context.Background(), refundBuyer, 12.50, "grant", e2eAdminID); err != nil {
		t.Fatal(err)
	}
	if _, err := balances.AdjustBalance(context.Background(), refundBuyer, -12.50,
		fmt.Sprintf("order_payment:%d", orderID), 0); err != nil {
		t.Fatal(err)
	}
	ledger := &failingRefundLedger{payLedgerStore: e.bot.payLedger, failIngest: true}
	e.bot.payLedger = ledger

	calls := e.cb(e2eAdminID, refundCBData(orderID, refundFullUSD), "en")
	want := e.bot.i18n.Tf("en", "admin_refund_ledger_failed_rerun",
		fmt.Sprintf("balance-refund:%d", orderID), orderID, "mock ledger ingest failure", orderID, "12.50")
	if got := tgText(calls); got != want {
		t.Fatalf("loud text = %q, want %q", got, want)
	}
	if text := tgText(calls); !strings.Contains(text, fmt.Sprintf("/refund %d 12.50", orderID)) {
		t.Fatalf("loud text misses the exact re-run command: %q", text)
	}
	if got := e.qStr(`SELECT printf('%.2f', balance_usd) FROM users WHERE telegram_id=?`, refundBuyer); got != "12.50" {
		t.Fatalf("balance after credit = %s, want 12.50", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM refunds`); got != 0 {
		t.Fatalf("failed ingest wrote %d refunds", got)
	}

	// Re-run with the ledger healed: NO second credit (the audit row is the
	// balance rail's idempotency identity), and the ledger record completes.
	ledger.setFailIngest(false)
	calls = e.cb(e2eAdminID, refundCBData(orderID, refundFullUSD), "en")
	if got := e.qInt(`SELECT COUNT(*) FROM balance_txs WHERE type=?`, fmt.Sprintf("order_refund:%d", orderID)); got != 1 {
		t.Fatalf("re-run minted %d order_refund credits, want exactly 1", got)
	}
	if got := e.qStr(`SELECT printf('%.2f', balance_usd) FROM users WHERE telegram_id=?`, refundBuyer); got != "12.50" {
		t.Fatalf("balance after re-run = %s, want 12.50 (single credit)", got)
	}
	assertRefundRow(t, e, orderID, "balance", fmt.Sprintf("balance-refund:%d", orderID),
		fmt.Sprintf("balance:%d", orderID), refundBuyer, refundFullUSD, "USD", 2)
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id=?`, orderID); got != storage.PaymentStateRefunded {
		t.Fatalf("state after recovery = %s", got)
	}
	want = e.bot.i18n.Tf("en", "admin_refund_done",
		fmt.Sprintf("balance-refund:%d", orderID), orderID, storage.PaymentStateRefunded)
	if got := tgText(calls); got != want {
		t.Fatalf("recovery text = %q, want %q", got, want)
	}
}

// TestAdminRefundLedgerFailurePartialCarriesPartialAmount pins the money-books
// fix on the rail with NO provider-side dedup: after a PARTIAL balance refund
// ($5.25 of $12.50) whose ledger record failed, the recovery message must
// carry the re-run command WITH the partial amount. An amount-less re-run
// defaults to FULL, and the balance rail's per-order order_refund identity
// would skip the credit while the ledger recorded a full refund — books claim
// $12.50 out while $5.25 moved. Recovery then follows the message LITERALLY:
// the shown command re-parses to the SAME amountMinor (the decimal rendering
// is the parser's exact inverse), the credit stays skipped and the ledger
// completes with the partial amount — books and money agree at 5.25.
func TestAdminRefundLedgerFailurePartialCarriesPartialAmount(t *testing.T) {
	e := newE2EEnv(t)
	e.cmd(refundBuyer, "/start", "en") // the credit needs the users row
	orderID := seedRefundOrder(t, e, storage.PaymentMethodBalance, "")
	balances := storage.NewSQLBalanceStore(e.db.Conn())
	if _, err := balances.AdjustBalance(context.Background(), refundBuyer, 12.50, "grant", e2eAdminID); err != nil {
		t.Fatal(err)
	}
	if _, err := balances.AdjustBalance(context.Background(), refundBuyer, -12.50,
		fmt.Sprintf("order_payment:%d", orderID), 0); err != nil {
		t.Fatal(err)
	}
	ledger := &failingRefundLedger{payLedgerStore: e.bot.payLedger, failIngest: true}
	e.bot.payLedger = ledger

	// Partial refund: the credit executes (balance rail = money moved), the
	// ledger record fails.
	calls := e.cb(e2eAdminID, refundCBData(orderID, 525), "en")
	if got := e.qStr(`SELECT printf('%.2f', balance_usd) FROM users WHERE telegram_id=?`, refundBuyer); got != "5.25" {
		t.Fatalf("balance after partial credit = %s, want 5.25", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM refunds`); got != 0 {
		t.Fatalf("failed ingest wrote %d refunds", got)
	}
	want := e.bot.i18n.Tf("en", "admin_refund_ledger_failed_rerun",
		fmt.Sprintf("balance-refund:%d", orderID), orderID, "mock ledger ingest failure", orderID, "5.25")
	if got := tgText(calls); got != want {
		t.Fatalf("loud text = %q, want %q", got, want)
	}
	// The re-run command shows the PARTIAL amount — never the full total and
	// never amount-less.
	text := tgText(calls)
	if !strings.Contains(text, fmt.Sprintf("/refund %d 5.25", orderID)) {
		t.Fatalf("loud text misses the partial re-run command: %q", text)
	}
	if strings.Contains(text, fmt.Sprintf("/refund %d 12.50", orderID)) {
		t.Fatalf("loud text offers a FULL-amount re-run after a partial refund: %q", text)
	}

	// Recovery BY THE MESSAGE: the admin copy-pastes the shown command. Its
	// card must carry the SAME amountMinor (525)...
	ledger.setFailIngest(false)
	calls = e.cmd(e2eAdminID, fmt.Sprintf("/refund %d 5.25", orderID), "en")
	if !strings.Contains(calls[0].markup(), refundCBData(orderID, 525)) {
		t.Fatalf("re-run card markup = %s, want the same amountMinor 525", calls[0].markup())
	}
	// ...and the confirm skips the credit (the order_refund row exists) while
	// completing the ledger with the PARTIAL amount.
	calls = e.cb(e2eAdminID, refundCBData(orderID, 525), "en")
	if got := e.qInt(`SELECT COUNT(*) FROM balance_txs WHERE type=?`, fmt.Sprintf("order_refund:%d", orderID)); got != 1 {
		t.Fatalf("recovery minted %d order_refund credits, want exactly 1", got)
	}
	if got := e.qStr(`SELECT printf('%.2f', balance_usd) FROM users WHERE telegram_id=?`, refundBuyer); got != "5.25" {
		t.Fatalf("balance after recovery = %s, want 5.25 (no second credit)", got)
	}
	assertRefundRow(t, e, orderID, "balance", fmt.Sprintf("balance-refund:%d", orderID),
		fmt.Sprintf("balance:%d", orderID), refundBuyer, 525, "USD", 2)
	assertRefundAudit(t, e, orderID, 1)
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id=?`, orderID); got != storage.PaymentStatePartiallyRefunded {
		t.Fatalf("state after recovery = %s, want partially_refunded (partial recorded)", got)
	}
	want = e.bot.i18n.Tf("en", "admin_refund_done",
		fmt.Sprintf("balance-refund:%d", orderID), orderID, storage.PaymentStatePartiallyRefunded)
	if got := tgText(calls); got != want {
		t.Fatalf("recovery text = %q, want %q", got, want)
	}
}

// TestAdminRefundBalanceDivergentRerunFailsClosed pins the HANDOFF §6.7
// guard: after a PARTIAL balance refund ($5.25 of $12.50) whose ledger
// record failed, a re-run with a DIFFERENT amount must fail closed — the
// per-order order_refund identity already spent its credit, so recording a
// divergent amount would make books claim money that never moved. Before
// the guard this re-run skipped the credit AND recorded a full refund.
func TestAdminRefundBalanceDivergentRerunFailsClosed(t *testing.T) {
	e := newE2EEnv(t)
	e.cmd(refundBuyer, "/start", "en") // the credit needs the users row
	orderID := seedRefundOrder(t, e, storage.PaymentMethodBalance, "")
	balances := storage.NewSQLBalanceStore(e.db.Conn())
	if _, err := balances.AdjustBalance(context.Background(), refundBuyer, 12.50, "grant", e2eAdminID); err != nil {
		t.Fatal(err)
	}
	if _, err := balances.AdjustBalance(context.Background(), refundBuyer, -12.50,
		fmt.Sprintf("order_payment:%d", orderID), 0); err != nil {
		t.Fatal(err)
	}
	ledger := &failingRefundLedger{payLedgerStore: e.bot.payLedger, failIngest: true}
	e.bot.payLedger = ledger

	// Partial refund: the credit executes ($5.25), the ledger record fails.
	e.cb(e2eAdminID, refundCBData(orderID, 525), "en")
	if got := e.qStr(`SELECT printf('%.2f', balance_usd) FROM users WHERE telegram_id=?`, refundBuyer); got != "5.25" {
		t.Fatalf("balance after partial credit = %s, want 5.25", got)
	}

	// Divergent re-run (FULL amount) with the ledger healed: fail closed.
	ledger.setFailIngest(false)
	calls := e.cb(e2eAdminID, refundCBData(orderID, refundFullUSD), "en")
	if got := e.qInt(`SELECT COUNT(*) FROM refunds`); got != 0 {
		t.Fatalf("divergent re-run wrote %d refund rows, want 0", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM balance_txs WHERE type=?`, fmt.Sprintf("order_refund:%d", orderID)); got != 1 {
		t.Fatalf("divergent re-run left %d order_refund rows, want the prior 1", got)
	}
	if got := e.qStr(`SELECT printf('%.2f', balance_usd) FROM users WHERE telegram_id=?`, refundBuyer); got != "5.25" {
		t.Fatalf("divergent re-run moved the balance to %s, want 5.25 untouched", got)
	}
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id=?`, orderID); got != storage.PaymentStateSettled {
		t.Fatalf("divergent re-run left state %s, want settled", got)
	}
	if text := tgText(calls); !strings.Contains(text, "prior credit") {
		t.Fatalf("divergent re-run text does not name the prior credit: %q", text)
	}

	// The EXACT-amount re-run still completes the record (credit skipped).
	calls = e.cb(e2eAdminID, refundCBData(orderID, 525), "en")
	if got := e.qInt(`SELECT COUNT(*) FROM balance_txs WHERE type=?`, fmt.Sprintf("order_refund:%d", orderID)); got != 1 {
		t.Fatalf("recovery minted %d credits, want exactly 1", got)
	}
	assertRefundRow(t, e, orderID, "balance", fmt.Sprintf("balance-refund:%d", orderID),
		fmt.Sprintf("balance:%d", orderID), refundBuyer, 525, "USD", 2)
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id=?`, orderID); got != storage.PaymentStatePartiallyRefunded {
		t.Fatalf("state after recovery = %s, want partially_refunded", got)
	}
	want := e.bot.i18n.Tf("en", "admin_refund_done",
		fmt.Sprintf("balance-refund:%d", orderID), orderID, storage.PaymentStatePartiallyRefunded)
	if got := tgText(calls); got != want {
		t.Fatalf("recovery text = %q, want %q", got, want)
	}
}

// TestAdminRefundConfirmBalanceConcurrentDoubleTap exercises the refundMu
// serialization under real contention on the rail with the WEAKEST dedup:
// balance has no provider API — the deterministic order_refund balance_txs
// row (a check-then-act, protected ONLY by the mutex) is its sole double-mint
// protection. Two confirms for the same card fire simultaneously; exactly one
// credit, one audit row and one ledger refund row may exist afterwards.
func TestAdminRefundConfirmBalanceConcurrentDoubleTap(t *testing.T) {
	e := newE2EEnv(t)
	e.cmd(refundBuyer, "/start", "en") // the credit needs the users row
	orderID := seedRefundOrder(t, e, storage.PaymentMethodBalance, "")
	balances := storage.NewSQLBalanceStore(e.db.Conn())
	if _, err := balances.AdjustBalance(context.Background(), refundBuyer, 12.50, "grant", e2eAdminID); err != nil {
		t.Fatal(err)
	}
	if _, err := balances.AdjustBalance(context.Background(), refundBuyer, -12.50,
		fmt.Sprintf("order_payment:%d", orderID), 0); err != nil {
		t.Fatal(err)
	}

	// The same confirm callback delivered as two concurrent updates (the
	// double-tap). Updates are built by hand — the e.cb helper's shared
	// updSeq counter is not goroutine-safe.
	update := func(id int) tgbotapi.Update {
		return tgbotapi.Update{
			UpdateID: id,
			CallbackQuery: &tgbotapi.CallbackQuery{
				ID:   fmt.Sprintf("cb-%d", id),
				Data: refundCBData(orderID, refundFullUSD),
				From: &tgbotapi.User{ID: e2eAdminID, FirstName: "A", LanguageCode: "en"},
				Message: &tgbotapi.Message{
					MessageID: 10_000,
					Chat:      &tgbotapi.Chat{ID: e2eAdminID, Type: "private"},
				},
			},
		}
	}
	before := e.tg.count()
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(2)
	for i := range 2 {
		go func(i int) {
			defer wg.Done()
			<-start // release both goroutines at the same instant
			e.handle(update(900_000 + i))
		}(i)
	}
	close(start)
	wg.Wait()

	// The mutex makes the outcome deterministic: whoever wins executes the
	// credit + ledger record; the loser reloads, finds the recorded refund
	// (replay) and never reaches the balance store.
	txType := fmt.Sprintf("order_refund:%d", orderID)
	if got := e.qInt(`SELECT COUNT(*) FROM balance_txs WHERE type=?`, txType); got != 1 {
		t.Fatalf("concurrent confirms minted %d order_refund credits, want exactly 1", got)
	}
	if got := e.qStr(`SELECT printf('%.2f', balance_usd) FROM users WHERE telegram_id=?`, refundBuyer); got != "12.50" {
		t.Fatalf("balance = %s, want 12.50 (single credit)", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM refunds WHERE order_id=?`, orderID); got != 1 {
		t.Fatalf("concurrent confirms wrote %d ledger refund rows, want exactly 1", got)
	}
	assertRefundRow(t, e, orderID, "balance", fmt.Sprintf("balance-refund:%d", orderID),
		fmt.Sprintf("balance:%d", orderID), refundBuyer, refundFullUSD, "USD", 2)
	assertRefundAudit(t, e, orderID, 1)
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id=?`, orderID); got != storage.PaymentStateRefunded {
		t.Fatalf("state = %s, want refunded (exactly once)", got)
	}
	// Both taps answer with the SAME truthful done message: the executor's
	// and the replay's renderings are identical.
	done := e.bot.i18n.Tf("en", "admin_refund_done",
		fmt.Sprintf("balance-refund:%d", orderID), orderID, storage.PaymentStateRefunded)
	renders := 0
	for _, c := range e.tg.since(before) {
		if c.Method == "sendMessage" || c.Method == "editMessageText" {
			renders++
			if got := c.Params.Get("text"); got != done {
				t.Fatalf("concurrent render %d text = %q, want done %q", renders, got, done)
			}
		}
	}
	if renders != 2 {
		t.Fatalf("concurrent confirms rendered %d messages, want 2 (executor + replay)", renders)
	}
}

// TestAdminRefundConfirmConflictOnStaleAmount drives a crafted confirm whose
// amount exceeds the captured total: the ledger preview quarantines and the
// flow stops with the reload message — no provider call, no write.
func TestAdminRefundConfirmConflictOnStaleAmount(t *testing.T) {
	e := newE2EEnvWithConfig(t, enableStripe)
	mock := newStripeRefundMock(t)
	e.bot.stripe.SetBaseURL(mock.srv.URL + "/v1")
	orderID := seedRefundOrder(t, e, storage.PaymentMethodStripe, refundStripeSession)

	calls := e.cb(e2eAdminID, refundCBData(orderID, 999_999), "en")
	if got, want := tgText(calls), e.bot.t("en", "admin_refund_conflict"); got != want {
		t.Fatalf("conflict text = %q, want %q", got, want)
	}
	if _, r := mock.stats(); r != 0 {
		t.Fatalf("conflict executed %d refunds", r)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM refunds`); got != 0 {
		t.Fatalf("conflict wrote %d refunds", got)
	}
}

// TestParseRefundCallback pins the callback grammar: exactly two positive
// integers after the prefix, within Telegram's 64-byte callback limit.
func TestParseRefundCallback(t *testing.T) {
	for _, tc := range []struct {
		data   string
		order  int64
		amount int64
		ok     bool
	}{
		{"admin:refund:42:1250", 42, 1250, true},
		{"admin:refund:1:1", 1, 1, true},
		{"admin:refund:0:5", 0, 0, false},
		{"admin:refund:42:0", 0, 0, false},
		{"admin:refund:42:-5", 0, 0, false},
		{"admin:refund:42", 0, 0, false},
		{"admin:refund:42:5:stripe", 0, 0, false},
		{"admin:refund:abc:5", 0, 0, false},
		{"admin:payrev:42:5", 0, 0, false},
		{"admin:refund:", 0, 0, false},
	} {
		order, amount, ok := parseRefundCallback(tc.data)
		if ok != tc.ok || (ok && (order != tc.order || amount != tc.amount)) {
			t.Fatalf("parseRefundCallback(%q) = (%d, %d, %t), want (%d, %d, %t)",
				tc.data, order, amount, ok, tc.order, tc.amount, tc.ok)
		}
	}
	if got := len(refundCallbackData(9_223_372_036_854_775_807, 9_223_372_036_854_775_807)); got > 64 {
		t.Fatalf("callback data length %d exceeds Telegram's 64-byte limit", got)
	}
}
