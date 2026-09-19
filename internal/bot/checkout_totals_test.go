package bot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/config"
)

func TestOrderConfirmDisplaysStoredTotals(t *testing.T) {
	for _, tc := range []struct {
		name      string
		priceUSD  float64
		promo     string
		wantUSD   string
		wantStars int
	}{
		{name: "regular", priceUSD: 10, wantUSD: "10.00", wantStars: 500},
		{name: "promo", priceUSD: 10, promo: "SAVE10", wantUSD: "9.00", wantStars: 450},
		{name: "promo rounds Stars down", priceUSD: 1.03, promo: "SAVE10", wantUSD: "0.93", wantStars: 45},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newE2EEnv(t)
			const buyer = int64(1001)
			if _, err := e.db.Conn().Exec(`UPDATE products SET price_usd = ? WHERE id = ?`, tc.priceUSD, e.prodReg); err != nil {
				t.Fatal(err)
			}
			e.cmd(buyer, "/start", "en")
			e.cb(buyer, fmt.Sprintf("cart:add:%d", e.prodReg), "en")
			confirm := "order:confirm"
			if tc.promo != "" {
				confirm += ":promo:" + tc.promo
			}
			calls := e.cb(buyer, confirm, "en")
			orderID := e.qInt(`SELECT MAX(id) FROM orders WHERE user_id = ?`, buyer)
			if got := e.qInt(`SELECT total_stars FROM orders WHERE id = ?`, orderID); got != int64(tc.wantStars) {
				t.Fatalf("stored Stars = %d, want %d", got, tc.wantStars)
			}
			if got := e.qStr(`SELECT printf('%.2f', total_usd) FROM orders WHERE id = ?`, orderID); got != tc.wantUSD {
				t.Fatalf("stored USD = %s, want %s", got, tc.wantUSD)
			}

			payment := requireRender(t, calls, fmt.Sprintf("pay:stars:%d", orderID))
			wantTotal := fmt.Sprintf("$%s / %d ⭐", tc.wantUSD, tc.wantStars)
			if !strings.Contains(payment.Params.Get("text"), "To pay: "+wantTotal) {
				t.Errorf("payment summary = %q, want total %s", payment.Params.Get("text"), wantTotal)
			}
			var markup tgbotapi.InlineKeyboardMarkup
			if err := json.Unmarshal([]byte(payment.markup()), &markup); err != nil {
				t.Fatal(err)
			}
			for _, btn := range []struct {
				callback string
				amount   string
			}{
				{callback: fmt.Sprintf("pay:stars:%d", orderID), amount: fmt.Sprintf("(%d ⭐)", tc.wantStars)},
				{callback: fmt.Sprintf("pay:crypto:%d", orderID), amount: "($" + tc.wantUSD + ")"},
			} {
				found := false
				for _, row := range markup.InlineKeyboard {
					for _, button := range row {
						if button.CallbackData != nil && *button.CallbackData == btn.callback {
							found = true
							if !strings.Contains(button.Text, btn.amount) {
								t.Errorf("button %s = %q, want amount %s", btn.callback, button.Text, btn.amount)
							}
						}
					}
				}
				if !found {
					t.Errorf("payment button %s missing", btn.callback)
				}
			}
			admin := requireCall(t, calls, "sendMessage", "New order #")
			if admin.Params.Get("chat_id") != strconv.FormatInt(e2eAdminID, 10) || !strings.Contains(admin.Params.Get("text"), wantTotal) {
				t.Errorf("admin notification = %v, want admin total %s", admin.Params, wantTotal)
			}
			invoice := requireCall(t, e.cb(buyer, fmt.Sprintf("pay:stars:%d", orderID), "en"), "sendInvoice", "")
			var prices []tgbotapi.LabeledPrice
			if err := json.Unmarshal([]byte(invoice.Params.Get("prices")), &prices); err != nil {
				t.Fatal(err)
			}
			if len(prices) != 1 || prices[0].Amount != tc.wantStars {
				t.Errorf("invoice prices = %+v, want %d Stars", prices, tc.wantStars)
			}
		})
	}
}

// --- YooKassa RUB card checkout ---

// yookassaReq captures what the fake YooKassa API received.
type yookassaReq struct {
	Path string
	Auth string
	Body map[string]any
}

const yookassaConfirmationURL = "https://checkout.example/pay/abc123"

// yookassaMock is a fake YooKassa API: it records every request and answers
// payment creation with a pending redirect payment. fail() makes the next
// request fail with an HTTP 500 so the error path can be exercised.
type yookassaMock struct {
	mu       sync.Mutex
	srv      *httptest.Server
	requests []yookassaReq
	failNext bool
}

func newYookassaMock(t *testing.T) *yookassaMock {
	t.Helper()
	m := &yookassaMock{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		m.requests = append(m.requests, yookassaReq{Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Body: body})
		fail := m.failNext
		m.failNext = false
		m.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type": "error", "id": "err-1", "code": "internal_error", "description": "boom",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":           "2b2f9c4a-000f-5000-8000-1f7d4b5e6a7d",
			"status":       "pending",
			"paid":         false,
			"amount":       body["amount"],
			"confirmation": map[string]any{"type": "redirect", "confirmation_url": yookassaConfirmationURL},
			"metadata":     body["metadata"],
			"created_at":   "2026-09-19T10:00:00Z",
		})
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *yookassaMock) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func (m *yookassaMock) last() yookassaReq {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.requests) == 0 {
		return yookassaReq{}
	}
	return m.requests[len(m.requests)-1]
}

func (m *yookassaMock) fail() {
	m.mu.Lock()
	m.failNext = true
	m.mu.Unlock()
}

// enableYooKassa configures credentials and a 92.5 RUB/USD rate: $19.99
// converts to 1849.075 → 1849.08 RUB (half-kopeck rounds up).
func enableYooKassa(c *config.Config) {
	c.YooKassaShopID = "123456"
	c.YooKassaSecretKey = "live_key"
	c.YooKassaReturnURL = "https://shop.example.com/return"
	c.USDToRUBRate = 92.5
}

// confirmOrder drives /start → add-to-cart → order:confirm (English locale)
// and returns the created order ID with the rendered payment-method keyboard.
func confirmOrder(e *e2eEnv, userID, productID int64) (int64, [][]tgbotapi.InlineKeyboardButton) {
	e.t.Helper()
	e.cmd(userID, "/start", "en")
	e.cb(userID, fmt.Sprintf("cart:add:%d", productID), "en")
	calls := e.cb(userID, "order:confirm", "en")
	orderID := e.qInt(`SELECT MAX(id) FROM orders WHERE user_id = ?`, userID)
	payment := requireRender(e.t, calls, fmt.Sprintf("pay:stars:%d", orderID))
	var markup tgbotapi.InlineKeyboardMarkup
	if err := json.Unmarshal([]byte(payment.markup()), &markup); err != nil {
		e.t.Fatalf("parse payment keyboard: %v", err)
	}
	return orderID, markup.InlineKeyboard
}

// buttonCallbacks flattens a rendered keyboard into its callback data strings
// in display order.
func buttonCallbacks(rows [][]tgbotapi.InlineKeyboardButton) []string {
	var callbacks []string
	for _, row := range rows {
		for _, button := range row {
			if button.CallbackData != nil {
				callbacks = append(callbacks, *button.CallbackData)
			}
		}
	}
	return callbacks
}

func TestPaymentKeyboardShowsRUBOnlyWhenEnabled(t *testing.T) {
	const buyer = int64(1011)

	// The pre-YooKassa row set: Stars, crypto, terms/support, cancel/orders, menu.
	baseKeyboard := func(orderID int64) []string {
		return []string{
			fmt.Sprintf("pay:stars:%d", orderID),
			fmt.Sprintf("pay:crypto:%d", orderID),
			"terms", "paysupport",
			fmt.Sprintf("order:cancel:%d", orderID), "back:orders",
			"back:menu",
		}
	}

	t.Run("unconfigured adapter keeps the row set unchanged", func(t *testing.T) {
		e := newE2EEnv(t) // no YooKassa credentials, USDToRUBRate 0
		orderID, rows := confirmOrder(e, buyer, e.prodReg)
		if got := buttonCallbacks(rows); !slices.Equal(got, baseKeyboard(orderID)) {
			t.Fatalf("callbacks = %v, want %v", got, baseKeyboard(orderID))
		}
	})

	t.Run("zero RUB rate keeps the row set unchanged", func(t *testing.T) {
		e := newE2EEnvWithConfig(t, func(c *config.Config) {
			c.YooKassaShopID = "123456"
			c.YooKassaSecretKey = "live_key"
			c.YooKassaReturnURL = "https://shop.example.com/return"
			// USDToRUBRate deliberately stays 0.
		})
		orderID, rows := confirmOrder(e, buyer, e.prodReg)
		if got := buttonCallbacks(rows); !slices.Equal(got, baseKeyboard(orderID)) {
			t.Fatalf("callbacks = %v, want %v", got, baseKeyboard(orderID))
		}
	})

	t.Run("configured adds exactly one RUB row", func(t *testing.T) {
		e := newE2EEnvWithConfig(t, enableYooKassa)
		if _, err := e.db.Conn().Exec(`UPDATE products SET price_usd = ? WHERE id = ?`, 19.99, e.prodReg); err != nil {
			t.Fatal(err)
		}
		orderID, rows := confirmOrder(e, buyer, e.prodReg)
		want := append([]string{
			fmt.Sprintf("pay:stars:%d", orderID),
			fmt.Sprintf("pay:crypto:%d", orderID),
			fmt.Sprintf("pay:yookassa:%d", orderID),
		}, baseKeyboard(orderID)[2:]...)
		if got := buttonCallbacks(rows); !slices.Equal(got, want) {
			t.Fatalf("callbacks = %v, want %v", got, want)
		}
		found := false
		for _, row := range rows {
			for _, button := range row {
				if button.CallbackData != nil && *button.CallbackData == fmt.Sprintf("pay:yookassa:%d", orderID) {
					found = true
					if !strings.Contains(button.Text, "1849.08") {
						t.Errorf("RUB button label = %q, want amount 1849.08", button.Text)
					}
					if !strings.Contains(button.Text, "Pay by card") {
						t.Errorf("RUB button label = %q, want localized btn_pay_rub", button.Text)
					}
				}
			}
		}
		if !found {
			t.Fatalf("pay:yookassa:%d button missing", orderID)
		}
	})

	t.Run("subscription cart hides the RUB row", func(t *testing.T) {
		e := newE2EEnvWithConfig(t, enableYooKassa)
		orderID, rows := confirmOrder(e, buyer, e.prodSub)
		want := []string{
			fmt.Sprintf("pay:stars:%d", orderID),
			"terms", "paysupport",
			fmt.Sprintf("order:cancel:%d", orderID), "back:orders",
			"back:menu",
		}
		if got := buttonCallbacks(rows); !slices.Equal(got, want) {
			t.Fatalf("callbacks = %v, want %v", got, want)
		}
	})
}

func TestOnPayYooKassaCreatesRedirectPayment(t *testing.T) {
	const buyer = int64(1012)
	const stranger = int64(1013)

	// newConfiguredEnv builds an env with YooKassa enabled and the bot's
	// adapter pointed at a fake YooKassa API via the SetBaseURL test seam.
	newConfiguredEnv := func(t *testing.T) (*e2eEnv, *yookassaMock) {
		e := newE2EEnvWithConfig(t, enableYooKassa)
		mock := newYookassaMock(t)
		e.bot.yookassa.SetBaseURL(mock.srv.URL)
		if _, err := e.db.Conn().Exec(`UPDATE products SET price_usd = ? WHERE id = ?`, 19.99, e.prodReg); err != nil {
			t.Fatal(err)
		}
		return e, mock
	}

	t.Run("pending order redirects to the confirmation URL", func(t *testing.T) {
		e, mock := newConfiguredEnv(t)
		orderID := e.placeOrder(buyer, e.prodReg, "")
		calls := e.cb(buyer, fmt.Sprintf("pay:yookassa:%d", orderID), "en")

		render := requireRender(t, calls, fmt.Sprintf("Pay order <code>#%d</code>", orderID))
		if !strings.Contains(render.Params.Get("text"), "1849.08") {
			t.Fatalf("payment message = %q, want amount 1849.08", render.Params.Get("text"))
		}
		var markup tgbotapi.InlineKeyboardMarkup
		if err := json.Unmarshal([]byte(render.markup()), &markup); err != nil {
			t.Fatal(err)
		}
		if len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 1 {
			t.Fatalf("payment keyboard = %s, want a single URL button", render.markup())
		}
		button := markup.InlineKeyboard[0][0]
		if button.URL == nil || *button.URL != yookassaConfirmationURL {
			t.Fatalf("URL button = %+v, want confirmation URL %s", button, yookassaConfirmationURL)
		}
		if button.Text != "💳 Pay by card" {
			t.Fatalf("URL button label = %q, want localized btn_pay_rub", button.Text)
		}
		// Skeleton state and callback ack precede the redirect message.
		if !hasCall(calls, "editMessageReplyMarkup", "Generating invoice") {
			t.Errorf("skeleton invoice edit missing")
		}
		if !hasCall(calls, "answerCallbackQuery", "") {
			t.Errorf("callback ack missing")
		}

		if mock.count() != 1 {
			t.Fatalf("YooKassa API calls = %d, want 1", mock.count())
		}
		req := mock.last()
		if req.Path != "/payments" {
			t.Errorf("API path = %q, want /payments", req.Path)
		}
		amount, _ := req.Body["amount"].(map[string]any)
		if amount["value"] != "1849.08" || amount["currency"] != "RUB" {
			t.Errorf("API amount = %v, want 1849.08 RUB", req.Body["amount"])
		}
		metadata, _ := req.Body["metadata"].(map[string]any)
		if metadata["order_id"] != strconv.FormatInt(orderID, 10) {
			t.Errorf("API metadata = %v, want order_id %d", req.Body["metadata"], orderID)
		}
		confirmation, _ := req.Body["confirmation"].(map[string]any)
		if confirmation["type"] != "redirect" || confirmation["return_url"] != "https://shop.example.com/return" {
			t.Errorf("API confirmation = %v, want redirect to the configured return URL", req.Body["confirmation"])
		}
	})

	t.Run("foreign buyer is rejected without an API call", func(t *testing.T) {
		e, mock := newConfiguredEnv(t)
		orderID := e.placeOrder(buyer, e.prodReg, "")
		calls := e.cb(stranger, fmt.Sprintf("pay:yookassa:%d", orderID), "en")
		requireCall(t, calls, "answerCallbackQuery", "Order not found")
		if mock.count() != 0 {
			t.Fatalf("YooKassa API calls = %d, want 0", mock.count())
		}
		if hasRender(calls, "Pay order") {
			t.Fatal("unexpected payment message for a foreign buyer")
		}
	})

	t.Run("non-pending order is rejected without an API call", func(t *testing.T) {
		e, mock := newConfiguredEnv(t)
		orderID := e.placeOrder(buyer, e.prodReg, "")
		if _, err := e.db.Conn().Exec(`UPDATE orders SET status = 'paid' WHERE id = ?`, orderID); err != nil {
			t.Fatal(err)
		}
		calls := e.cb(buyer, fmt.Sprintf("pay:yookassa:%d", orderID), "en")
		requireCall(t, calls, "answerCallbackQuery", "no longer be paid")
		if mock.count() != 0 {
			t.Fatalf("YooKassa API calls = %d, want 0", mock.count())
		}
	})

	t.Run("unconfigured adapter alerts yookassa_unavailable", func(t *testing.T) {
		e := newE2EEnv(t) // no credentials, no RUB rate
		orderID := e.placeOrder(buyer, e.prodReg, "")
		calls := e.cb(buyer, fmt.Sprintf("pay:yookassa:%d", orderID), "en")
		requireCall(t, calls, "answerCallbackQuery", "Card payment is not available")
	})

	t.Run("subscription order alerts sub_stars_only", func(t *testing.T) {
		e, mock := newConfiguredEnv(t)
		orderID := e.placeOrder(buyer, e.prodSub, "")
		calls := e.cb(buyer, fmt.Sprintf("pay:yookassa:%d", orderID), "en")
		requireCall(t, calls, "answerCallbackQuery", "only be paid with Telegram Stars")
		if mock.count() != 0 {
			t.Fatalf("YooKassa API calls = %d, want 0", mock.count())
		}
	})

	t.Run("API failure informs the buyer", func(t *testing.T) {
		e, mock := newConfiguredEnv(t)
		mock.fail()
		orderID := e.placeOrder(buyer, e.prodReg, "")
		calls := e.cb(buyer, fmt.Sprintf("pay:yookassa:%d", orderID), "en")
		requireCall(t, calls, "sendMessage", "Error creating payment")
		if mock.count() != 1 {
			t.Fatalf("YooKassa API calls = %d, want 1", mock.count())
		}
	})
}

// --- Stripe USD card checkout ---

// stripeReq captures what the fake Stripe API received. Checkout Sessions are
// created with a form-encoded body, so the form values are kept verbatim.
type stripeReq struct {
	Path string
	Auth string
	Form url.Values
}

const stripeSessionURL = "https://checkout.stripe.example/pay/cs_test_abc"

// stripeMock is a fake Stripe API: it records every request and answers
// session creation with a hosted checkout URL. fail() makes the next request
// fail with an HTTP 500 so the error path can be exercised.
type stripeMock struct {
	mu       sync.Mutex
	srv      *httptest.Server
	requests []stripeReq
	failNext bool
}

func newStripeMock(t *testing.T) *stripeMock {
	t.Helper()
	m := &stripeMock{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		m.mu.Lock()
		m.requests = append(m.requests, stripeReq{Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Form: r.PostForm})
		fail := m.failNext
		m.failNext = false
		m.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"type": "api_error", "message": "boom"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":  "cs_test_abc",
			"url": stripeSessionURL,
		})
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *stripeMock) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func (m *stripeMock) last() stripeReq {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.requests) == 0 {
		return stripeReq{}
	}
	return m.requests[len(m.requests)-1]
}

func (m *stripeMock) fail() {
	m.mu.Lock()
	m.failNext = true
	m.mu.Unlock()
}

// enableStripe configures Stripe credentials. The USD total needs no
// conversion: $19.99 charges as 1999 cents.
func enableStripe(c *config.Config) {
	c.StripeSecretKey = "sk_test_e2e"
	c.StripeWebhookSecret = "whsec_e2e"
	c.StripeReturnURL = "https://shop.example.com/return"
}

func TestPaymentKeyboardShowsStripeOnlyWhenEnabled(t *testing.T) {
	const buyer = int64(1021)

	// The pre-Stripe row set (crypto configured, YooKassa not): Stars, crypto,
	// terms/support, cancel/orders, menu.
	baseKeyboard := func(orderID int64) []string {
		return []string{
			fmt.Sprintf("pay:stars:%d", orderID),
			fmt.Sprintf("pay:crypto:%d", orderID),
			"terms", "paysupport",
			fmt.Sprintf("order:cancel:%d", orderID), "back:orders",
			"back:menu",
		}
	}

	t.Run("unconfigured adapter keeps the row set unchanged", func(t *testing.T) {
		e := newE2EEnv(t) // no Stripe credentials
		orderID, rows := confirmOrder(e, buyer, e.prodReg)
		if got := buttonCallbacks(rows); !slices.Equal(got, baseKeyboard(orderID)) {
			t.Fatalf("callbacks = %v, want %v", got, baseKeyboard(orderID))
		}
	})

	t.Run("configured adds exactly one USD card row", func(t *testing.T) {
		e := newE2EEnvWithConfig(t, enableStripe)
		orderID, rows := confirmOrder(e, buyer, e.prodReg)
		want := append([]string{
			fmt.Sprintf("pay:stars:%d", orderID),
			fmt.Sprintf("pay:crypto:%d", orderID),
			fmt.Sprintf("pay:stripe:%d", orderID),
		}, baseKeyboard(orderID)[2:]...)
		if got := buttonCallbacks(rows); !slices.Equal(got, want) {
			t.Fatalf("callbacks = %v, want %v", got, want)
		}
		found := false
		for _, row := range rows {
			for _, button := range row {
				if button.CallbackData != nil && *button.CallbackData == fmt.Sprintf("pay:stripe:%d", orderID) {
					found = true
					if button.Text != "💳 Card ($)" {
						t.Errorf("USD button label = %q, want localized btn_pay_stripe", button.Text)
					}
				}
			}
		}
		if !found {
			t.Fatalf("pay:stripe:%d button missing", orderID)
		}
	})

	t.Run("stripe row follows the YooKassa row", func(t *testing.T) {
		e := newE2EEnvWithConfig(t, func(c *config.Config) {
			enableYooKassa(c)
			enableStripe(c)
		})
		orderID, rows := confirmOrder(e, buyer, e.prodReg)
		want := []string{
			fmt.Sprintf("pay:stars:%d", orderID),
			fmt.Sprintf("pay:crypto:%d", orderID),
			fmt.Sprintf("pay:yookassa:%d", orderID),
			fmt.Sprintf("pay:stripe:%d", orderID),
			"terms", "paysupport",
			fmt.Sprintf("order:cancel:%d", orderID), "back:orders",
			"back:menu",
		}
		if got := buttonCallbacks(rows); !slices.Equal(got, want) {
			t.Fatalf("callbacks = %v, want %v", got, want)
		}
	})

	t.Run("subscription cart hides the USD card row", func(t *testing.T) {
		e := newE2EEnvWithConfig(t, enableStripe)
		orderID, rows := confirmOrder(e, buyer, e.prodSub)
		want := []string{
			fmt.Sprintf("pay:stars:%d", orderID),
			"terms", "paysupport",
			fmt.Sprintf("order:cancel:%d", orderID), "back:orders",
			"back:menu",
		}
		if got := buttonCallbacks(rows); !slices.Equal(got, want) {
			t.Fatalf("callbacks = %v, want %v", got, want)
		}
	})
}

func TestOnPayStripeCreatesCheckoutSession(t *testing.T) {
	const buyer = int64(1022)
	const stranger = int64(1023)

	// newConfiguredEnv builds an env with Stripe enabled and the bot's adapter
	// pointed at a fake Stripe API via the SetBaseURL test seam.
	newConfiguredEnv := func(t *testing.T) (*e2eEnv, *stripeMock) {
		e := newE2EEnvWithConfig(t, enableStripe)
		mock := newStripeMock(t)
		e.bot.stripe.SetBaseURL(mock.srv.URL)
		if _, err := e.db.Conn().Exec(`UPDATE products SET price_usd = ? WHERE id = ?`, 19.99, e.prodReg); err != nil {
			t.Fatal(err)
		}
		return e, mock
	}

	t.Run("pending order redirects to the checkout session URL", func(t *testing.T) {
		e, mock := newConfiguredEnv(t)
		orderID := e.placeOrder(buyer, e.prodReg, "")
		calls := e.cb(buyer, fmt.Sprintf("pay:stripe:%d", orderID), "en")

		render := requireRender(t, calls, fmt.Sprintf("Pay order <code>#%d</code>", orderID))
		if !strings.Contains(render.Params.Get("text"), "$19.99") {
			t.Fatalf("payment message = %q, want amount $19.99", render.Params.Get("text"))
		}
		var markup tgbotapi.InlineKeyboardMarkup
		if err := json.Unmarshal([]byte(render.markup()), &markup); err != nil {
			t.Fatal(err)
		}
		if len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 1 {
			t.Fatalf("payment keyboard = %s, want a single URL button", render.markup())
		}
		button := markup.InlineKeyboard[0][0]
		if button.URL == nil || *button.URL != stripeSessionURL {
			t.Fatalf("URL button = %+v, want session URL %s", button, stripeSessionURL)
		}
		if button.Text != "💳 Card ($)" {
			t.Fatalf("URL button label = %q, want localized btn_pay_stripe", button.Text)
		}
		// Skeleton state and callback ack precede the redirect message.
		if !hasCall(calls, "editMessageReplyMarkup", "Generating invoice") {
			t.Errorf("skeleton invoice edit missing")
		}
		if !hasCall(calls, "answerCallbackQuery", "") {
			t.Errorf("callback ack missing")
		}

		if mock.count() != 1 {
			t.Fatalf("Stripe API calls = %d, want 1", mock.count())
		}
		req := mock.last()
		if req.Path != "/checkout/sessions" {
			t.Errorf("API path = %q, want /checkout/sessions", req.Path)
		}
		if req.Auth != "Bearer sk_test_e2e" {
			t.Errorf("API auth = %q, want the configured secret key", req.Auth)
		}
		if got := req.Form.Get("line_items[0][price_data][unit_amount]"); got != "1999" {
			t.Errorf("unit_amount = %q, want 1999 cents for $19.99", got)
		}
		if got := req.Form.Get("line_items[0][price_data][currency]"); got != "usd" {
			t.Errorf("currency = %q, want usd", got)
		}
		if got := req.Form.Get("metadata[order_id]"); got != strconv.FormatInt(orderID, 10) {
			t.Errorf("metadata order_id = %q, want %d", got, orderID)
		}
		if got := req.Form.Get("success_url"); got != "https://shop.example.com/return" {
			t.Errorf("success_url = %q, want the configured return URL", got)
		}
	})

	t.Run("foreign buyer is rejected without an API call", func(t *testing.T) {
		e, mock := newConfiguredEnv(t)
		orderID := e.placeOrder(buyer, e.prodReg, "")
		calls := e.cb(stranger, fmt.Sprintf("pay:stripe:%d", orderID), "en")
		requireCall(t, calls, "answerCallbackQuery", "Order not found")
		if mock.count() != 0 {
			t.Fatalf("Stripe API calls = %d, want 0", mock.count())
		}
		if hasRender(calls, "Pay order") {
			t.Fatal("unexpected payment message for a foreign buyer")
		}
	})

	t.Run("non-pending order is rejected without an API call", func(t *testing.T) {
		e, mock := newConfiguredEnv(t)
		orderID := e.placeOrder(buyer, e.prodReg, "")
		if _, err := e.db.Conn().Exec(`UPDATE orders SET status = 'paid' WHERE id = ?`, orderID); err != nil {
			t.Fatal(err)
		}
		calls := e.cb(buyer, fmt.Sprintf("pay:stripe:%d", orderID), "en")
		requireCall(t, calls, "answerCallbackQuery", "no longer be paid")
		if mock.count() != 0 {
			t.Fatalf("Stripe API calls = %d, want 0", mock.count())
		}
	})

	t.Run("unconfigured adapter alerts stripe_unavailable", func(t *testing.T) {
		e := newE2EEnv(t) // no Stripe credentials
		orderID := e.placeOrder(buyer, e.prodReg, "")
		calls := e.cb(buyer, fmt.Sprintf("pay:stripe:%d", orderID), "en")
		requireCall(t, calls, "answerCallbackQuery", "not available")
	})

	t.Run("below the $0.50 Stripe minimum is refused without an API call", func(t *testing.T) {
		e, mock := newConfiguredEnv(t)
		if _, err := e.db.Conn().Exec(`UPDATE products SET price_usd = ? WHERE id = ?`, 0.49, e.prodReg); err != nil {
			t.Fatal(err)
		}
		orderID := e.placeOrder(buyer, e.prodReg, "")
		calls := e.cb(buyer, fmt.Sprintf("pay:stripe:%d", orderID), "en")
		requireCall(t, calls, "answerCallbackQuery", "not available")
		if mock.count() != 0 {
			t.Fatalf("Stripe API calls = %d, want 0", mock.count())
		}
		if hasRender(calls, "Pay order") {
			t.Fatal("unexpected payment message below the Stripe minimum")
		}
	})

	t.Run("subscription order alerts sub_stars_only", func(t *testing.T) {
		e, mock := newConfiguredEnv(t)
		orderID := e.placeOrder(buyer, e.prodSub, "")
		calls := e.cb(buyer, fmt.Sprintf("pay:stripe:%d", orderID), "en")
		requireCall(t, calls, "answerCallbackQuery", "only be paid with Telegram Stars")
		if mock.count() != 0 {
			t.Fatalf("Stripe API calls = %d, want 0", mock.count())
		}
	})

	t.Run("API failure informs the buyer", func(t *testing.T) {
		e, mock := newConfiguredEnv(t)
		mock.fail()
		orderID := e.placeOrder(buyer, e.prodReg, "")
		calls := e.cb(buyer, fmt.Sprintf("pay:stripe:%d", orderID), "en")
		requireCall(t, calls, "sendMessage", "Error creating payment")
		if mock.count() != 1 {
			t.Fatalf("Stripe API calls = %d, want 1", mock.count())
		}
	})
}
