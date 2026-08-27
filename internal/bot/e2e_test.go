package bot

// End-to-end buyer journeys: a real Bot wired to a temporary SQLite database
// (storage.New on t.TempDir()) and a fake in-process Telegram Bot API server
// that records every outgoing request — the same approach as
// cmd/telegram-smoke and cmd/usability-smoke, embedded in the package.
//
// Assertions target database state (SQL) and recorded Bot API calls
// (methods, callback data, invoice params), never localized message texts.
// All updates are dispatched synchronously through the production router.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/prometheus/client_golang/prometheus"

	"shop_bot/internal/bot/middleware"
	"shop_bot/internal/config"
	"shop_bot/internal/service"
	"shop_bot/internal/storage"
)

const (
	e2eBotToken    = "e2e-token"
	e2eCryptoToken = "e2e-crypto-token"
	e2eAdminID     = int64(9001)
)

// --- fake Telegram Bot API ---

type tgCall struct {
	Method string
	Params url.Values
}

// markup returns the raw reply_markup JSON of the call ("" when absent).
func (c tgCall) markup() string { return c.Params.Get("reply_markup") }

type fakeTelegram struct {
	mu        sync.Mutex
	calls     []tgCall
	nextMsgID int
}

func (f *fakeTelegram) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		_ = r.ParseForm()
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	method := parts[len(parts)-1]

	params := make(url.Values, len(r.Form))
	for k, vs := range r.Form {
		params[k] = append([]string(nil), vs...)
	}

	writeJSON := func(payload any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}

	switch method {
	case "getMe":
		writeJSON(map[string]any{
			"ok": true,
			"result": map[string]any{
				"id": 424242, "is_bot": true,
				"first_name": "E2E Bot", "username": "e2e_bot",
			},
		})
		return
	case "answerCallbackQuery", "deleteMessage", "answerPreCheckoutQuery":
		f.record(tgCall{Method: method, Params: params})
		writeJSON(map[string]any{"ok": true, "result": true})
		return
	default:
		f.record(tgCall{Method: method, Params: params})
		f.mu.Lock()
		f.nextMsgID++
		msgID := f.nextMsgID
		f.mu.Unlock()
		chatID, _ := strconv.ParseInt(params.Get("chat_id"), 10, 64)
		writeJSON(map[string]any{
			"ok": true,
			"result": map[string]any{
				"message_id": msgID,
				"date":       time.Now().Unix(),
				"chat":       map[string]any{"id": chatID, "type": "private"},
				"text":       params.Get("text"),
			},
		})
	}
}

func (f *fakeTelegram) record(call tgCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeTelegram) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeTelegram) since(from int) []tgCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]tgCall, len(f.calls[from:]))
	copy(out, f.calls[from:])
	return out
}

// --- test environment ---

type e2eEnv struct {
	t       *testing.T
	db      *storage.DB
	bot     *Bot
	tg      *fakeTelegram
	handle  func(tgbotapi.Update)
	updSeq  int
	catID   int64
	prodReg int64 // regular product: $10 / 500⭐, stock 5
	prodSub int64 // subscription product (30 days): $2 / 100⭐, stock 100
}

func newE2EEnv(t *testing.T) *e2eEnv {
	t.Helper()

	tg := &fakeTelegram{nextMsgID: 100}
	srv := httptest.NewServer(http.HandlerFunc(tg.serveHTTP))
	t.Cleanup(srv.Close)

	api, err := tgbotapi.NewBotAPIWithAPIEndpoint(e2eBotToken, srv.URL+"/bot%s/%s")
	if err != nil {
		t.Fatalf("fake bot api: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "shop.db")
	db, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cfg := &config.Config{
		BotToken:       e2eBotToken,
		CryptoBotToken: e2eCryptoToken,
		AdminIDs:       []int64{e2eAdminID},
		DBPath:         dbPath,
		USDToStarsRate: 50,
		LocalesDir:     filepath.Join("..", "..", "locales"),
	}

	logWriter := io.Writer(io.Discard)
	if testing.Verbose() {
		logWriter = os.Stderr
	}
	logger := slog.New(slog.NewTextHandler(logWriter, nil))
	b, err := NewWithAPI(cfg, api, db, service.NewMetricsServiceWith(prometheus.NewRegistry()),
		storage.NewMemoryFSMStore(), nil, logger)
	if err != nil {
		t.Fatalf("NewWithAPI: %v", err)
	}

	env := &e2eEnv{
		t:  t,
		db: db, bot: b, tg: tg,
		// The production chain minus rate limiting (its per-user token bucket
		// would silently drop mid-journey updates) and logging. Auth stays:
		// it upserts users exactly like production.
		handle: middleware.Auth(b.users)(b.route),
	}
	env.seedCatalog()
	return env
}

func (e *e2eEnv) seedCatalog() {
	e.t.Helper()
	conn := e.db.Conn()

	res, err := conn.Exec(`INSERT INTO categories (name, emoji, sort_order, is_active) VALUES ('E2E', '🧪', 1, 1)`)
	if err != nil {
		e.t.Fatalf("seed category: %v", err)
	}
	e.catID, _ = res.LastInsertId()

	res, err = conn.Exec(
		`INSERT INTO products (category_id, name, description, price_usd, price_stars, stock, is_active)
		 VALUES (?, 'Tee', 'cotton', 10.0, 500, 5, 1)`, e.catID)
	if err != nil {
		e.t.Fatalf("seed regular product: %v", err)
	}
	e.prodReg, _ = res.LastInsertId()

	res, err = conn.Exec(
		`INSERT INTO products (category_id, name, description, price_usd, price_stars, stock, is_active, sub_period_days)
		 VALUES (?, 'Club', 'monthly club', 2.0, 100, 100, 1, 30)`, e.catID)
	if err != nil {
		e.t.Fatalf("seed subscription product: %v", err)
	}
	e.prodSub, _ = res.LastInsertId()

	if _, err := conn.Exec(
		`INSERT INTO promo_codes (code, discount, max_uses, is_active) VALUES ('SAVE10', 10, 0, 1)`); err != nil {
		e.t.Fatalf("seed promo: %v", err)
	}
}

// --- update dispatch ---

func (e *e2eEnv) do(upd tgbotapi.Update) []tgCall {
	e.t.Helper()
	before := e.tg.count()
	e.handle(upd)
	return e.tg.since(before)
}

func (e *e2eEnv) cmd(userID int64, text, lang string) []tgCall {
	e.updSeq++
	entityLength := len(strings.SplitN(text, " ", 2)[0])
	return e.do(tgbotapi.Update{
		UpdateID: e.updSeq,
		Message: &tgbotapi.Message{
			MessageID: e.updSeq,
			Chat:      &tgbotapi.Chat{ID: userID, Type: "private"},
			From:      &tgbotapi.User{ID: userID, FirstName: "U", UserName: fmt.Sprintf("u%d", userID), LanguageCode: lang},
			Text:      text,
			Entities:  []tgbotapi.MessageEntity{{Offset: 0, Length: entityLength, Type: "bot_command"}},
		},
	})
}

func (e *e2eEnv) text(userID int64, text, lang string) []tgCall {
	e.updSeq++
	return e.do(tgbotapi.Update{
		UpdateID: e.updSeq,
		Message: &tgbotapi.Message{
			MessageID: e.updSeq,
			Chat:      &tgbotapi.Chat{ID: userID, Type: "private"},
			From:      &tgbotapi.User{ID: userID, FirstName: "U", UserName: fmt.Sprintf("u%d", userID), LanguageCode: lang},
			Text:      text,
		},
	})
}

func (e *e2eEnv) cb(userID int64, data, lang string) []tgCall {
	e.updSeq++
	return e.do(tgbotapi.Update{
		UpdateID: e.updSeq,
		CallbackQuery: &tgbotapi.CallbackQuery{
			ID:   fmt.Sprintf("cb-%d", e.updSeq),
			Data: data,
			From: &tgbotapi.User{ID: userID, FirstName: "U", UserName: fmt.Sprintf("u%d", userID), LanguageCode: lang},
			Message: &tgbotapi.Message{
				MessageID: 10_000 + e.updSeq,
				Chat:      &tgbotapi.Chat{ID: userID, Type: "private"},
			},
		},
	})
}

func (e *e2eEnv) preCheckout(userID int64, queryID, payload string, totalStars int) []tgCall {
	e.updSeq++
	return e.do(tgbotapi.Update{
		UpdateID: e.updSeq,
		PreCheckoutQuery: &tgbotapi.PreCheckoutQuery{
			ID:             queryID,
			From:           &tgbotapi.User{ID: userID, LanguageCode: "ru"},
			Currency:       "XTR",
			TotalAmount:    totalStars,
			InvoicePayload: payload,
		},
	})
}

func (e *e2eEnv) successfulPayment(userID int64, payload string, totalStars int, chargeID string) []tgCall {
	e.updSeq++
	update := tgbotapi.Update{
		UpdateID: e.updSeq,
		Message: &tgbotapi.Message{
			MessageID: e.updSeq,
			Date:      int(time.Now().Unix()),
			Chat:      &tgbotapi.Chat{ID: userID, Type: "private"},
			From:      &tgbotapi.User{ID: userID, LanguageCode: "ru"},
			SuccessfulPayment: &tgbotapi.SuccessfulPayment{
				Currency:                "XTR",
				TotalAmount:             totalStars,
				InvoicePayload:          payload,
				TelegramPaymentChargeID: chargeID,
			},
		},
	}
	// Drive the same raw-update boundary as production so subscription-only
	// fields omitted by tgbotapi v5 are present during settlement.
	expiresAt := time.Now().Add(30 * 24 * time.Hour).Unix()
	raw, err := json.Marshal(map[string]any{
		"update_id": update.UpdateID,
		"message": map[string]any{
			"message_id": update.Message.MessageID,
			"date":       update.Message.Date,
			"chat":       update.Message.Chat,
			"from":       update.Message.From,
			"successful_payment": map[string]any{
				"currency": "XTR", "total_amount": totalStars, "invoice_payload": payload,
				"telegram_payment_charge_id": chargeID, "subscription_expiration_date": expiresAt,
			},
		},
	})
	if err != nil {
		e.t.Fatal(err)
	}
	decoded, cleanup, err := e.bot.decodeTelegramUpdate(raw)
	if err != nil {
		e.t.Fatal(err)
	}
	defer cleanup()
	return e.do(decoded)
}

// --- journey building blocks ---

// placeOrder drives add-to-cart → checkout → confirm (optionally with a promo
// code baked into the callback) and returns the freshly created order ID.
func (e *e2eEnv) placeOrder(userID, productID int64, promoCode string) int64 {
	e.t.Helper()
	e.cb(userID, fmt.Sprintf("cart:add:%d", productID), "ru")
	e.cb(userID, "cart:checkout", "ru")
	confirm := "order:confirm"
	if promoCode != "" {
		confirm = "order:confirm:promo:" + promoCode
	}
	e.cb(userID, confirm, "ru")
	return e.qInt(`SELECT COALESCE(MAX(id), 0) FROM orders WHERE user_id = ?`, userID)
}

// payWithStars runs the full Stars payment leg: invoice request, pre-checkout
// approval and the successful_payment update.
func (e *e2eEnv) payWithStars(userID, orderID int64, chargeID string) {
	e.t.Helper()
	total := int(e.qInt(`SELECT total_stars FROM orders WHERE id = ?`, orderID))
	payload := strconv.FormatInt(orderID, 10)

	calls := e.cb(userID, "pay:stars:"+payload, "ru")
	inv := requireCall(e.t, calls, "sendInvoice", "")
	if got := inv.Params.Get("payload"); got != payload {
		e.t.Fatalf("invoice payload = %q, want %q", got, payload)
	}

	calls = e.preCheckout(userID, "pcq-"+chargeID, payload, total)
	answer := requireCall(e.t, calls, "answerPreCheckoutQuery", "")
	if answer.Params.Get("ok") != "true" {
		e.t.Fatalf("pre-checkout rejected: %v", answer.Params)
	}

	e.successfulPayment(userID, payload, total, chargeID)
}

// --- SQL assertion helpers ---

func (e *e2eEnv) qInt(query string, args ...any) int64 {
	e.t.Helper()
	var v int64
	if err := e.db.Conn().QueryRow(query, args...).Scan(&v); err != nil {
		e.t.Fatalf("query %q: %v", query, err)
	}
	return v
}

func (e *e2eEnv) qStr(query string, args ...any) string {
	e.t.Helper()
	var v string
	if err := e.db.Conn().QueryRow(query, args...).Scan(&v); err != nil {
		e.t.Fatalf("query %q: %v", query, err)
	}
	return v
}

func (e *e2eEnv) userDBID(telegramID int64) int64 {
	e.t.Helper()
	return e.qInt(`SELECT id FROM users WHERE telegram_id = ?`, telegramID)
}

// --- call assertion helpers ---

func callMatches(c tgCall, substr string) bool {
	return substr == "" || strings.Contains(c.Params.Encode(), substr) ||
		strings.Contains(c.markup(), substr) || strings.Contains(c.Params.Get("text"), substr)
}

// requireCall returns the last recorded call with the given method whose
// serialized params contain substr ("" matches any call of the method).
func requireCall(t *testing.T, calls []tgCall, method, substr string) tgCall {
	t.Helper()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].Method == method && callMatches(calls[i], substr) {
			return calls[i]
		}
	}
	t.Fatalf("no %s call matching %q among:\n%s", method, substr, dumpCalls(calls))
	return tgCall{}
}

// requireRender returns the last screen render — sendOrEditStyled emits
// either sendMessage or editMessageText — whose params contain substr.
func requireRender(t *testing.T, calls []tgCall, substr string) tgCall {
	t.Helper()
	for i := len(calls) - 1; i >= 0; i-- {
		m := calls[i].Method
		if (m == "sendMessage" || m == "editMessageText") && callMatches(calls[i], substr) {
			return calls[i]
		}
	}
	t.Fatalf("no render matching %q among:\n%s", substr, dumpCalls(calls))
	return tgCall{}
}

func hasRender(calls []tgCall, substr string) bool {
	for _, c := range calls {
		if (c.Method == "sendMessage" || c.Method == "editMessageText") && callMatches(c, substr) {
			return true
		}
	}
	return false
}

func hasCall(calls []tgCall, method, substr string) bool {
	for _, c := range calls {
		if c.Method == method && callMatches(c, substr) {
			return true
		}
	}
	return false
}

func dumpCalls(calls []tgCall) string {
	var sb strings.Builder
	for _, c := range calls {
		fmt.Fprintf(&sb, "  %s chat_id=%s text=%.60q markup=%.200s\n",
			c.Method, c.Params.Get("chat_id"), c.Params.Get("text"), c.markup())
	}
	return sb.String()
}

// --- scenarios ---

// TestE2E_BuyerJourney covers the full happy path: /start → catalog →
// product card → cart → checkout → promo code → confirmation → Stars
// pre-checkout → successful payment (order paid, stock decremented, loyalty
// points awarded) → admin /setdelivered → review invitation → 5-star rating
// with text → review row persisted and rating shown on the product card.
func TestE2E_BuyerJourney(t *testing.T) {
	e := newE2EEnv(t)
	const buyer = int64(1001)

	// /start registers the user and renders the main menu.
	calls := e.cmd(buyer, "/start", "ru")
	requireRender(t, calls, "back:catalog")
	if got := e.qInt(`SELECT COUNT(*) FROM users WHERE telegram_id = ?`, buyer); got != 1 {
		t.Fatalf("users rows for buyer = %d, want 1", got)
	}

	// Catalog → category → product card.
	calls = e.cb(buyer, "back:catalog", "ru")
	requireRender(t, calls, fmt.Sprintf("category:%d", e.catID))
	calls = e.cb(buyer, fmt.Sprintf("category:%d", e.catID), "ru")
	requireRender(t, calls, fmt.Sprintf("product:%d", e.prodReg))
	calls = e.cb(buyer, fmt.Sprintf("product:%d", e.prodReg), "ru")
	requireRender(t, calls, fmt.Sprintf("cart:add:%d", e.prodReg))

	// Add to cart.
	e.cb(buyer, fmt.Sprintf("cart:add:%d", e.prodReg), "ru")
	if got := e.qInt(`SELECT quantity FROM cart_items WHERE user_id = ? AND product_id = ?`, buyer, e.prodReg); got != 1 {
		t.Fatalf("cart quantity = %d, want 1", got)
	}

	// Checkout screen offers promo entry and confirmation.
	calls = e.cb(buyer, "cart:checkout", "ru")
	requireRender(t, calls, "promo:enter")

	// Promo entry: FSM prompt, then the code as a plain text message.
	e.cb(buyer, "promo:enter", "ru")
	calls = e.text(buyer, "SAVE10", "ru")
	requireRender(t, calls, "order:confirm:promo:SAVE10")

	// Confirm with the promo: order created with a 10% discount.
	calls = e.cb(buyer, "order:confirm:promo:SAVE10", "ru")
	orderID := e.qInt(`SELECT MAX(id) FROM orders WHERE user_id = ?`, buyer)
	if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPending {
		t.Fatalf("order status = %q, want pending", got)
	}
	if got := e.qInt(`SELECT discount_pct FROM orders WHERE id = ?`, orderID); got != 10 {
		t.Fatalf("order discount_pct = %d, want 10", got)
	}
	if got := e.qStr(`SELECT promo_code FROM orders WHERE id = ?`, orderID); got != "SAVE10" {
		t.Fatalf("order promo_code = %q, want SAVE10", got)
	}
	if got := e.qInt(`SELECT total_stars FROM orders WHERE id = ?`, orderID); got != 450 {
		t.Fatalf("order total_stars = %d, want 450 (500 - 10%%)", got)
	}
	requireRender(t, calls, fmt.Sprintf("pay:stars:%d", orderID))

	// Stars invoice for the discounted total, no subscription period.
	payload := strconv.FormatInt(orderID, 10)
	calls = e.cb(buyer, "pay:stars:"+payload, "ru")
	inv := requireCall(t, calls, "sendInvoice", "")
	if inv.Params.Get("payload") != payload {
		t.Fatalf("invoice payload = %q, want %q", inv.Params.Get("payload"), payload)
	}
	if inv.Params.Get("currency") != "XTR" {
		t.Fatalf("invoice currency = %q, want XTR", inv.Params.Get("currency"))
	}
	if !strings.Contains(inv.Params.Get("prices"), "450") {
		t.Fatalf("invoice prices %q missing discounted amount 450", inv.Params.Get("prices"))
	}
	if inv.Params.Get("subscription_period") != "" {
		t.Fatalf("regular order must not carry subscription_period, got %q", inv.Params.Get("subscription_period"))
	}

	// Pre-checkout with a wrong amount is rejected...
	calls = e.preCheckout(buyer, "pcq-bad", payload, 999)
	bad := requireCall(t, calls, "answerPreCheckoutQuery", "")
	// tgbotapi encodes ok=false by omitting the param (Params.AddBool).
	if bad.Params.Get("ok") == "true" {
		t.Fatalf("mismatched pre-checkout not rejected: %v", bad.Params)
	}
	if bad.Params.Get("error_message") == "" {
		t.Fatal("rejected pre-checkout carries no error_message")
	}

	// ...the genuine one is approved.
	calls = e.preCheckout(buyer, "pcq-ok", payload, 450)
	ok := requireCall(t, calls, "answerPreCheckoutQuery", "")
	if ok.Params.Get("ok") != "true" {
		t.Fatalf("valid pre-checkout rejected: %v", ok.Params)
	}

	// successful_payment: paid + stock decrement + cashback + promo usage.
	e.successfulPayment(buyer, payload, 450, "ch-journey-1")
	if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPaid {
		t.Fatalf("order status after payment = %q, want paid", got)
	}
	if got := e.qStr(`SELECT payment_method FROM orders WHERE id = ?`, orderID); got != storage.PaymentMethodStars {
		t.Fatalf("payment_method = %q, want stars", got)
	}
	if got := e.qStr(`SELECT payment_id FROM orders WHERE id = ?`, orderID); got != "ch-journey-1" {
		t.Fatalf("payment_id = %q, want ch-journey-1", got)
	}
	if got := e.qInt(`SELECT stock FROM products WHERE id = ?`, e.prodReg); got != 4 {
		t.Fatalf("stock after payment = %d, want 4", got)
	}
	// $9.00 at 1% bronze cashback → 9 points.
	if got := e.qInt(`SELECT loyalty_pts FROM users WHERE telegram_id = ?`, buyer); got != 9 {
		t.Fatalf("loyalty_pts = %d, want 9", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM loyalty_txs WHERE user_id = ? AND reason = 'purchase'`, e.userDBID(buyer)); got != 1 {
		t.Fatalf("purchase loyalty_txs = %d, want 1", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM promo_usages WHERE user_id = ? AND order_id = ?`, buyer, orderID); got != 1 {
		t.Fatalf("promo_usages rows = %d, want 1", got)
	}

	// Admin marks the order delivered → buyer gets the rating invitation.
	calls = e.cmd(e2eAdminID, fmt.Sprintf("/setdelivered %d", orderID), "ru")
	if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusDelivered {
		t.Fatalf("order status after setdelivered = %q, want delivered", got)
	}
	invite := requireRender(t, calls, fmt.Sprintf("review:%d:5", orderID))
	if invite.Params.Get("chat_id") != strconv.FormatInt(buyer, 10) {
		t.Fatalf("review invite sent to chat %s, want %d", invite.Params.Get("chat_id"), buyer)
	}

	// 5-star rating, then the free-form review text.
	e.cb(buyer, fmt.Sprintf("review:%d:5", orderID), "ru")
	if got := e.qInt(`SELECT rating FROM reviews WHERE product_id = ? AND user_id = ?`, e.prodReg, buyer); got != 5 {
		t.Fatalf("review rating = %d, want 5", got)
	}
	e.text(buyer, "Отличная футболка!", "ru")
	if got := e.qStr(`SELECT COALESCE(text, '') FROM reviews WHERE product_id = ? AND user_id = ?`, e.prodReg, buyer); got != "Отличная футболка!" {
		t.Fatalf("review text = %q", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM reviews`); got != 1 {
		t.Fatalf("reviews rows = %d, want 1", got)
	}

	// The product card now renders the aggregated rating and a reviews button.
	calls = e.cb(buyer, fmt.Sprintf("product:%d", e.prodReg), "ru")
	card := requireRender(t, calls, fmt.Sprintf("review:list:%d", e.prodReg))
	if text := card.Params.Get("text") + card.Params.Get("caption"); !strings.Contains(text, "5.0") || !strings.Contains(text, "(1)") {
		t.Fatalf("product card misses the 5.0 (1) rating, text: %q", text)
	}
}

// TestE2E_ReferralFirstPurchaseAward: user B joins through A's deep link;
// B's FIRST paid order awards A 100 points and issues B a personal REF promo
// usable only by B; B's second purchase yields no further referral bonuses.
func TestE2E_ReferralFirstPurchaseAward(t *testing.T) {
	e := newE2EEnv(t)
	const userA, userB = int64(2001), int64(2002)

	// A registers and opens the referral screen (generates the code lazily).
	e.cmd(userA, "/start", "ru")
	e.cmd(userA, "/referral", "ru")
	code := e.qStr(`SELECT COALESCE(referral_code, '') FROM users WHERE telegram_id = ?`, userA)
	if code == "" {
		t.Fatal("referral code was not generated for A")
	}

	// B joins through the deep link.
	e.cmd(userB, "/start ref_"+code, "ru")
	if got := e.qInt(`SELECT COALESCE(referred_by, 0) FROM users WHERE telegram_id = ?`, userB); got != e.userDBID(userA) {
		t.Fatalf("B.referred_by = %d, want A's internal id %d", got, e.userDBID(userA))
	}

	// B's first purchase.
	order1 := e.placeOrder(userB, e.prodReg, "")
	if order1 == 0 {
		t.Fatal("first order was not created")
	}
	before := e.tg.count()
	e.payWithStars(userB, order1, "ch-ref-1")
	paymentCalls := e.tg.since(before)

	// A got exactly the 100-point referrer bonus.
	if got := e.qInt(`SELECT loyalty_pts FROM users WHERE telegram_id = ?`, userA); got != 100 {
		t.Fatalf("A loyalty_pts = %d, want 100", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM loyalty_txs WHERE user_id = ? AND reason = 'referral'`, e.userDBID(userA)); got != 1 {
		t.Fatalf("referral loyalty_txs for A = %d, want 1", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM referral_awards`); got != 1 {
		t.Fatalf("referral_awards rows = %d, want 1", got)
	}
	if got := e.qInt(`SELECT referred_user_id FROM referral_awards`); got != e.userDBID(userB) {
		t.Fatalf("referral_awards.referred_user_id = %d, want B's internal id %d", got, e.userDBID(userB))
	}
	// A was notified (a message went to A's chat during the payment step).
	if !hasCall(paymentCalls, "sendMessage", "chat_id="+strconv.FormatInt(userA, 10)) {
		t.Fatalf("no referrer notification sent to %d:\n%s", userA, dumpCalls(paymentCalls))
	}

	// B received a personal REF- promo bound to their Telegram ID.
	refCode := e.qStr(`SELECT code FROM promo_codes WHERE bound_user_id IS NOT NULL`)
	if !strings.HasPrefix(refCode, "REF-") {
		t.Fatalf("personal promo code = %q, want REF- prefix", refCode)
	}
	if got := e.qInt(`SELECT bound_user_id FROM promo_codes WHERE code = ?`, refCode); got != userB {
		t.Fatalf("promo bound_user_id = %d, want %d", got, userB)
	}
	if got := e.qInt(`SELECT discount FROM promo_codes WHERE code = ?`, refCode); got != 10 {
		t.Fatalf("promo discount = %d, want 10", got)
	}

	// The personal promo is rejected for anyone but B: A tries to use it.
	e.cb(userA, fmt.Sprintf("cart:add:%d", e.prodReg), "ru")
	e.cb(userA, "order:confirm:promo:"+refCode, "ru")
	if got := e.qInt(`SELECT COUNT(*) FROM orders WHERE user_id = ?`, userA); got != 0 {
		t.Fatalf("A must not be able to order with B's personal promo, got %d orders", got)
	}

	// B's second purchase (with the REF promo): discount applies, no new bonuses.
	order2 := e.placeOrder(userB, e.prodReg, refCode)
	if order2 == order1 || order2 == 0 {
		t.Fatalf("second order not created (order1=%d order2=%d)", order1, order2)
	}
	if got := e.qInt(`SELECT discount_pct FROM orders WHERE id = ?`, order2); got != 10 {
		t.Fatalf("second order discount_pct = %d, want 10", got)
	}
	e.payWithStars(userB, order2, "ch-ref-2")

	if got := e.qInt(`SELECT loyalty_pts FROM users WHERE telegram_id = ?`, userA); got != 100 {
		t.Fatalf("A loyalty_pts after B's 2nd purchase = %d, want still 100", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM loyalty_txs WHERE user_id = ? AND reason = 'referral'`, e.userDBID(userA)); got != 1 {
		t.Fatalf("referral loyalty_txs after 2nd purchase = %d, want still 1", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM referral_awards`); got != 1 {
		t.Fatalf("referral_awards after 2nd purchase = %d, want still 1", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM promo_codes WHERE bound_user_id IS NOT NULL`); got != 1 {
		t.Fatalf("personal promos after 2nd purchase = %d, want still 1", got)
	}
}

// TestE2E_SubscriptionLifecycle: a SubPeriodDays=30 product produces a
// recurring Stars invoice (subscription_period=2592000); the successful
// payment records an active subscription expiring ≈ +30 days; /mysubs lists
// it and sub:cancel cancels it on both the Telegram and DB sides.
func TestE2E_SubscriptionLifecycle(t *testing.T) {
	e := newE2EEnv(t)
	const buyer = int64(3001)

	e.cmd(buyer, "/start", "ru")

	// Order the subscription product; crypto must be hidden for such carts.
	e.cb(buyer, fmt.Sprintf("cart:add:%d", e.prodSub), "ru")
	e.cb(buyer, "cart:checkout", "ru")
	calls := e.cb(buyer, "order:confirm", "ru")
	orderID := e.qInt(`SELECT MAX(id) FROM orders WHERE user_id = ?`, buyer)
	payScreen := requireRender(t, calls, fmt.Sprintf("pay:stars:%d", orderID))
	if strings.Contains(payScreen.markup(), "pay:crypto:") {
		t.Fatalf("subscription order offers crypto payment: %s", payScreen.markup())
	}

	// The invoice is recurring: subscription_period = 30 days in seconds.
	payload := strconv.FormatInt(orderID, 10)
	calls = e.cb(buyer, "pay:stars:"+payload, "ru")
	inv := requireCall(t, calls, "sendInvoice", "")
	if got := inv.Params.Get("subscription_period"); got != "2592000" {
		t.Fatalf("subscription_period = %q, want 2592000", got)
	}
	if inv.Params.Get("payload") != payload {
		t.Fatalf("invoice payload = %q, want %q", inv.Params.Get("payload"), payload)
	}

	// Pay.
	beforePay := time.Now()
	calls = e.preCheckout(buyer, "pcq-sub", payload, 100)
	if requireCall(t, calls, "answerPreCheckoutQuery", "").Params.Get("ok") != "true" {
		t.Fatal("subscription pre-checkout rejected")
	}
	e.successfulPayment(buyer, payload, 100, "ch-sub-1")

	// Subscription row: active, right charge, expires ≈ +30 days.
	if got := e.qInt(`SELECT COUNT(*) FROM subscriptions WHERE user_id = ? AND product_id = ?`, buyer, e.prodSub); got != 1 {
		t.Fatalf("subscriptions rows = %d, want 1", got)
	}
	if got := e.qStr(`SELECT status FROM subscriptions WHERE user_id = ?`, buyer); got != storage.SubStatusActive {
		t.Fatalf("subscription status = %q, want active", got)
	}
	if got := e.qStr(`SELECT telegram_charge_id FROM subscriptions WHERE user_id = ?`, buyer); got != "ch-sub-1" {
		t.Fatalf("subscription charge = %q, want ch-sub-1", got)
	}
	if got := e.qInt(`SELECT order_id FROM subscriptions WHERE user_id = ?`, buyer); got != orderID {
		t.Fatalf("subscription order_id = %d, want %d", got, orderID)
	}
	subs, err := e.bot.subs.ListActiveByUser(t.Context(), buyer)
	if err != nil || len(subs) != 1 {
		t.Fatalf("ListActiveByUser: %v, %d rows", err, len(subs))
	}
	lo, hi := beforePay.Add(29*24*time.Hour), beforePay.Add(31*24*time.Hour)
	if subs[0].ExpiresAt.Before(lo) || subs[0].ExpiresAt.After(hi) {
		t.Fatalf("expires_at = %v, want within [%v, %v]", subs[0].ExpiresAt, lo, hi)
	}

	// /mysubs shows the subscription with a cancel button.
	calls = e.cmd(buyer, "/mysubs", "ru")
	cancelData := fmt.Sprintf("sub:cancel:%d", subs[0].ID)
	requireRender(t, calls, cancelData)

	// Cancel: raw editUserStarSubscription + local status flip.
	calls = e.cb(buyer, cancelData, "ru")
	cancelReq := requireCall(t, calls, "editUserStarSubscription", "")
	if got := cancelReq.Params.Get("telegram_payment_charge_id"); got != "ch-sub-1" {
		t.Fatalf("cancel charge id = %q, want ch-sub-1", got)
	}
	if got := cancelReq.Params.Get("is_canceled"); got != "true" {
		t.Fatalf("cancel is_canceled = %q, want true", got)
	}
	if got := e.qStr(`SELECT status FROM subscriptions WHERE user_id = ?`, buyer); got != storage.SubStatusCanceled {
		t.Fatalf("subscription status after cancel = %q, want canceled", got)
	}

	// /mysubs no longer offers cancellation.
	calls = e.cmd(buyer, "/mysubs", "ru")
	if hasRender(calls, cancelData) {
		t.Fatalf("canceled subscription still listed:\n%s", dumpCalls(calls))
	}
}

// TestE2E_CryptoWebhook: a correctly signed CryptoBot webhook confirms the
// order exactly once (idempotent redelivery returns 200 without double side
// effects); a bad signature is rejected and changes nothing.
func TestE2E_CryptoWebhook(t *testing.T) {
	e := newE2EEnv(t)
	const buyer = int64(4001)

	e.cmd(buyer, "/start", "ru")
	orderID := e.placeOrder(buyer, e.prodReg, "")
	if orderID == 0 {
		t.Fatal("order was not created")
	}

	handler := e.bot.CryptoBotWebhookHandler()
	body := fmt.Sprintf(
		`{"update_type":"invoice_paid","payload":{"invoice_id":555,"status":"paid","asset":"USDT","amount":"10.00","paid_at":"2026-08-27T10:00:00Z","payload":"%d"}}`, orderID)

	post := func(payload, signature string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/cryptobot-webhook", strings.NewReader(payload))
		req.Header.Set("crypto-pay-api-signature", signature)
		rec := httptest.NewRecorder()
		handler(rec, req)
		return rec
	}

	// Valid signature → order paid, stock decremented, cashback awarded.
	if rec := post(body, cryptoSign(body)); rec.Code != http.StatusOK {
		t.Fatalf("valid webhook status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPaid {
		t.Fatalf("order status = %q, want paid", got)
	}
	if got := e.qStr(`SELECT payment_method FROM orders WHERE id = ?`, orderID); got != storage.PaymentMethodCrypto {
		t.Fatalf("payment_method = %q, want crypto", got)
	}
	if got := e.qStr(`SELECT payment_id FROM orders WHERE id = ?`, orderID); got != "555" {
		t.Fatalf("payment_id = %q, want 555", got)
	}
	if got := e.qInt(`SELECT stock FROM products WHERE id = ?`, e.prodReg); got != 4 {
		t.Fatalf("stock = %d, want 4", got)
	}
	ptsAfterFirst := e.qInt(`SELECT loyalty_pts FROM users WHERE telegram_id = ?`, buyer)
	if ptsAfterFirst != 10 { // $10 at 1% bronze cashback
		t.Fatalf("loyalty_pts = %d, want 10", ptsAfterFirst)
	}

	// Exact redelivery → 200, but no double stock/points/tx effects.
	if rec := post(body, cryptoSign(body)); rec.Code != http.StatusOK {
		t.Fatalf("redelivered webhook status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	if got := e.qInt(`SELECT stock FROM products WHERE id = ?`, e.prodReg); got != 4 {
		t.Fatalf("stock after redelivery = %d, want still 4", got)
	}
	if got := e.qInt(`SELECT loyalty_pts FROM users WHERE telegram_id = ?`, buyer); got != ptsAfterFirst {
		t.Fatalf("loyalty_pts after redelivery = %d, want still %d", got, ptsAfterFirst)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM loyalty_txs WHERE user_id = ?`, e.userDBID(buyer)); got != 1 {
		t.Fatalf("loyalty_txs after redelivery = %d, want 1", got)
	}

	// Broken signature on a fresh pending order → 403, nothing changes.
	order2 := e.placeOrder(buyer, e.prodReg, "")
	body2 := fmt.Sprintf(
		`{"update_type":"invoice_paid","payload":{"invoice_id":556,"status":"paid","asset":"USDT","amount":"10.00","paid_at":"2026-08-27T10:00:00Z","payload":"%d"}}`, order2)
	if rec := post(body2, cryptoSign(body2+"tampered")); rec.Code != http.StatusForbidden {
		t.Fatalf("tampered webhook status = %d, want 403", rec.Code)
	}
	if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, order2); got != storage.OrderStatusPending {
		t.Fatalf("order status after rejected webhook = %q, want still pending", got)
	}
	if got := e.qInt(`SELECT stock FROM products WHERE id = ?`, e.prodReg); got != 4 {
		t.Fatalf("stock after rejected webhook = %d, want still 4", got)
	}
}

// cryptoSign reproduces the CryptoBot webhook signature:
// HMAC-SHA256 over the body with SHA256(token) as the key.
func cryptoSign(body string) string {
	secret := sha256.Sum256([]byte(e2eCryptoToken))
	mac := hmac.New(sha256.New, secret[:])
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}
