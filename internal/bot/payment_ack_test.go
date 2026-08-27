package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/config"
	"shop_bot/internal/storage"
)

const testTelegramWebhookSecret = "0123456789abcdef0123456789abcdef"

func TestTelegramWebhookWithoutStrongSecretFailsClosed(t *testing.T) {
	e := newE2EEnv(t)
	const buyer = int64(6101)
	e.cmd(buyer, "/start", "en")
	orderID := e.placeOrder(buyer, e.prodReg, "")
	body := telegramSuccessfulPaymentBody(1, buyer, fmt.Sprint(orderID), "stars-forged", 500)

	recorder := httptest.NewRecorder()
	e.bot.TelegramWebhookHandler()(recorder,
		httptest.NewRequest(http.MethodPost, "/telegram-webhook", strings.NewReader(body)))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	if got := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); got != storage.OrderStatusPending {
		t.Fatalf("forged webhook changed order status to %q", got)
	}
}

func TestTelegramWebhookStarsParseFailureIsDurablyAcknowledged(t *testing.T) {
	e := newE2EEnv(t)
	e.bot.cfg.TelegramWebhookSecret = testTelegramWebhookSecret
	body := telegramSuccessfulPaymentBody(11, 6201, "not-an-order", "stars-malformed-1", 500)

	post := func(raw string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/telegram-webhook", strings.NewReader(raw))
		request.Header.Set("X-Telegram-Bot-Api-Secret-Token", testTelegramWebhookSecret)
		e.bot.TelegramWebhookHandler()(recorder, request)
		return recorder
	}
	if recorder := post(body); recorder.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200 (%s)", recorder.Code, recorder.Body.String())
	}
	if recorder := post(body); recorder.Code != http.StatusOK {
		t.Fatalf("replay status = %d, want 200 (%s)", recorder.Code, recorder.Body.String())
	}

	var count int
	var reason, rawPayload string
	if err := e.db.Conn().QueryRow(`SELECT COUNT(*), reason, raw_payload
		FROM payment_anomalies WHERE provider = 'stars' AND external_id = 'stars-malformed-1'`).Scan(
		&count, &reason, &rawPayload); err != nil {
		t.Fatal(err)
	}
	if count != 1 || reason != "stars_invalid_order_payload" {
		t.Fatalf("count=%d reason=%q", count, reason)
	}
	if strings.Contains(rawPayload, "not-an-order") || !strings.HasPrefix(rawPayload, "invoice_payload_sha256:") {
		t.Fatalf("raw payload was not safely digested: %q", rawPayload)
	}
}

func TestTelegramWebhookValidStarsPaymentSettlesBeforeAcknowledgement(t *testing.T) {
	e := newE2EEnv(t)
	e.bot.cfg.TelegramWebhookSecret = testTelegramWebhookSecret
	const buyer = int64(6200)
	e.cmd(buyer, "/start", "en")
	orderID := e.placeOrder(buyer, e.prodReg, "")
	body := telegramSuccessfulPaymentBody(10, buyer, fmt.Sprint(orderID), "stars-valid-webhook", 500)
	request := httptest.NewRequest(http.MethodPost, "/telegram-webhook", strings.NewReader(body))
	request.Header.Set("X-Telegram-Bot-Api-Secret-Token", testTelegramWebhookSecret)
	recorder := httptest.NewRecorder()
	e.bot.TelegramWebhookHandler()(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, recorder.Body.String())
	}
	if status := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); status != storage.OrderStatusPaid {
		t.Fatalf("order status = %s, want paid", status)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_attempts
		WHERE provider='stars' AND external_id='stars-valid-webhook' AND status='succeeded'`); got != 1 {
		t.Fatalf("settled payment attempt count = %d, want 1", got)
	}
}

func TestTelegramWebhookUndecodableStarsFieldsAreDurablyAcknowledged(t *testing.T) {
	e := newE2EEnv(t)
	e.bot.cfg.TelegramWebhookSecret = testTelegramWebhookSecret
	body := `{"update_id":12,"message":{"message_id":1,"date":1700000000,` +
		`"chat":{"id":6202,"type":"private"},"from":{"id":6202,"is_bot":false,"first_name":"U"},` +
		`"successful_payment":{"currency":"XTR","total_amount":"not-an-integer",` +
		`"invoice_payload":"opaque-value","telegram_payment_charge_id":"stars-decode-1"}}}`
	request := httptest.NewRequest(http.MethodPost, "/telegram-webhook", strings.NewReader(body))
	request.Header.Set("X-Telegram-Bot-Api-Secret-Token", testTelegramWebhookSecret)
	recorder := httptest.NewRecorder()
	e.bot.TelegramWebhookHandler()(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, recorder.Body.String())
	}
	var reason, rawPayload string
	if err := e.db.Conn().QueryRow(`SELECT reason, raw_payload FROM payment_anomalies
		WHERE provider = 'stars' ORDER BY id DESC LIMIT 1`).Scan(&reason, &rawPayload); err != nil {
		t.Fatal(err)
	}
	if reason != "stars_update_decode_failure" || strings.Contains(rawPayload, "opaque-value") ||
		!strings.HasPrefix(rawPayload, "telegram_update_sha256:") {
		t.Fatalf("reason=%q raw_payload=%q", reason, rawPayload)
	}
}

func TestTelegramWebhookStarsStorageFailureWithholdsAcknowledgement(t *testing.T) {
	e := newE2EEnv(t)
	e.bot.cfg.TelegramWebhookSecret = testTelegramWebhookSecret
	if err := e.db.Close(); err != nil {
		t.Fatal(err)
	}
	body := telegramSuccessfulPaymentBody(13, 6203, "not-an-order", "stars-storage-failure", 500)
	request := httptest.NewRequest(http.MethodPost, "/telegram-webhook", strings.NewReader(body))
	request.Header.Set("X-Telegram-Bot-Api-Secret-Token", testTelegramWebhookSecret)
	recorder := httptest.NewRecorder()
	e.bot.TelegramWebhookHandler()(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
}

func TestTelegramPollingDoesNotAdvancePastUndurableStarsPayment(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	provider := &pollingStarsServer{cancel: cancel}
	server := httptest.NewServer(provider)
	defer server.Close()

	api, err := tgbotapi.NewBotAPIWithAPIEndpoint("123456789:test-token", server.URL+"/bot%s/%s")
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.New(filepath.Join(t.TempDir(), "polling.db"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewWithAPI(&config.Config{
		BotToken: "123456789:test-token", LocalesDir: "../../locales", USDToStarsRate: 50,
	}, api, db, nil, storage.NewMemoryFSMStore(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	err = b.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context cancellation", err)
	}
	provider.mu.Lock()
	offsets := append([]string(nil), provider.offsets...)
	provider.mu.Unlock()
	if len(offsets) < 2 || offsets[0] != "0" || offsets[1] != "0" {
		t.Fatalf("getUpdates offsets = %v, want retry at offset 0", offsets)
	}
}

func TestCryptoWebhookResolvedMalformedReplayIsAcknowledged(t *testing.T) {
	e := newE2EEnv(t)
	body := `{"update_type":"invoice_paid","payload":{"invoice_id":777,"status":"paid",` +
		`"asset":"TON","amount":"1.00","paid_at":"2026-08-27T10:00:00Z","payload":"not-an-order"}}`
	post := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/cryptobot-webhook", strings.NewReader(body))
		request.Header.Set("crypto-pay-api-signature", cryptoSign(body))
		e.bot.CryptoBotWebhookHandler()(recorder, request)
		return recorder
	}
	if recorder := post(); recorder.Code != http.StatusOK {
		t.Fatalf("initial status = %d, want 200", recorder.Code)
	}
	var anomalyID int64
	if err := e.db.Conn().QueryRow(`SELECT id FROM payment_anomalies
		WHERE provider = 'crypto' AND external_id = '777'`).Scan(&anomalyID); err != nil {
		t.Fatal(err)
	}
	ledger := storage.NewSQLPaymentLedgerStore(e.db)
	if err := ledger.ResolvePaymentReview(context.Background(), storage.PaymentReviewResolution{
		OrderID: 0, Provider: storage.PaymentMethodCrypto, AnomalyIDs: []int64{anomalyID},
		Decision: "compensated", Actor: "operator:test", Reason: "provider compensation verified",
	}); err != nil {
		t.Fatalf("resolve anomaly: %v", err)
	}
	if recorder := post(); recorder.Code != http.StatusOK {
		t.Fatalf("resolved replay status = %d, want 200 (%s)", recorder.Code, recorder.Body.String())
	}
	var count int
	if err := e.db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies
		WHERE provider = 'crypto' AND external_id = '777'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("anomaly count = %d, want 1", count)
	}
}

func TestCryptoWebhookMissingPaidAtIsQuarantinedNotSettled(t *testing.T) {
	e := newE2EEnv(t)
	const buyer = int64(6401)
	e.cmd(buyer, "/start", "en")
	orderID := e.placeOrder(buyer, e.prodReg, "")
	body := fmt.Sprintf(`{"update_type":"invoice_paid","payload":{"invoice_id":778,"status":"paid",`+
		`"asset":"USDT","amount":"10.00","payload":"%d"}}`, orderID)
	request := httptest.NewRequest(http.MethodPost, "/cryptobot-webhook", strings.NewReader(body))
	request.Header.Set("crypto-pay-api-signature", cryptoSign(body))
	recorder := httptest.NewRecorder()
	e.bot.CryptoBotWebhookHandler()(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if status := e.qStr(`SELECT status FROM orders WHERE id = ?`, orderID); status != storage.OrderStatusPending {
		t.Fatalf("missing paid_at settled order: %s", status)
	}
	if state := e.qStr(`SELECT payment_state FROM orders WHERE id = ?`, orderID); state != storage.PaymentStateNeedsReview {
		t.Fatalf("payment_state = %s, want needs_review", state)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_anomalies
		WHERE provider='crypto' AND external_id='778' AND reason='webhook_invalid_paid_invoice'`); got != 1 {
		t.Fatalf("anomaly count = %d, want 1", got)
	}
}

func telegramSuccessfulPaymentBody(updateID int, userID int64, payload, chargeID string, amount int) string {
	body, _ := json.Marshal(map[string]any{
		"update_id": updateID,
		"message": map[string]any{
			"message_id": updateID,
			"date":       1700000000,
			"chat":       map[string]any{"id": userID, "type": "private"},
			"from":       map[string]any{"id": userID, "is_bot": false, "first_name": "U"},
			"successful_payment": map[string]any{
				"currency": "XTR", "total_amount": amount, "invoice_payload": payload,
				"telegram_payment_charge_id": chargeID,
			},
		},
	})
	return string(body)
}

type pollingStarsServer struct {
	mu      sync.Mutex
	offsets []string
	cancel  context.CancelFunc
}

func (s *pollingStarsServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]
	w.Header().Set("Content-Type", "application/json")
	switch method {
	case "getMe":
		_, _ = io.WriteString(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"Bot","username":"bot"}}`)
	case "getUpdates":
		_ = r.ParseForm()
		s.mu.Lock()
		s.offsets = append(s.offsets, r.Form.Get("offset"))
		count := len(s.offsets)
		s.mu.Unlock()
		if count >= 2 {
			s.cancel()
		}
		body := telegramSuccessfulPaymentBody(41, 6301, "not-an-order", "polling-undurable", 500)
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":[%s]}`, body)
	default:
		_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
	}
}
