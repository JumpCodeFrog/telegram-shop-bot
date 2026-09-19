package payment

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shop_bot/internal/storage"
)

const (
	yookassaTestShopID    = "12345"
	yookassaTestSecretKey = "test-secret"
	yookassaTestReturnURL = "https://shop.example/orders/return"
)

// newYookassaTestClient points a fully configured adapter at the test server,
// mirroring the production base URL https://api.yookassa.ru/v3.
func newYookassaTestClient(srv *httptest.Server) *YooKassaPayment {
	client := NewYooKassaPayment(yookassaTestShopID, yookassaTestSecretKey, yookassaTestReturnURL)
	client.baseURL = srv.URL + "/v3"
	return client
}

// requireYooKassaBasicAuth asserts the request carries Basic auth for the
// test credentials.
func requireYooKassaBasicAuth(t *testing.T, r *http.Request) {
	t.Helper()
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Basic ") {
		t.Errorf("expected Basic authorization header, got %q", auth)
		return
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, "Basic "))
	if err != nil {
		t.Errorf("authorization header is not valid base64: %v", err)
		return
	}
	if want := yookassaTestShopID + ":" + yookassaTestSecretKey; string(decoded) != want {
		t.Errorf("basic auth decoded to %q, want %q", decoded, want)
	}
}

func TestYooKassaCreatePaymentSendsRedirectConfirmation(t *testing.T) {
	var idempotenceKeys []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v3/payments" {
			t.Errorf("expected path /v3/payments, got %s", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type application/json, got %q", r.Header.Get("Content-Type"))
		}
		requireYooKassaBasicAuth(t, r)

		key := r.Header.Get("Idempotence-Key")
		if key == "" {
			t.Error("expected non-empty Idempotence-Key header")
		}
		idempotenceKeys = append(idempotenceKeys, key)

		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("failed to read request body: %v", err)
			return
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("failed to parse request body %q: %v", raw, err)
			return
		}
		if body["capture"] != true {
			t.Errorf("capture = %v, want true", body["capture"])
		}
		amount, _ := body["amount"].(map[string]any)
		if amount["value"] != "1999.00" {
			t.Errorf("amount.value = %v, want %q", amount["value"], "1999.00")
		}
		if amount["currency"] != "RUB" {
			t.Errorf("amount.currency = %v, want %q", amount["currency"], "RUB")
		}
		confirmation, _ := body["confirmation"].(map[string]any)
		if confirmation["type"] != "redirect" {
			t.Errorf("confirmation.type = %v, want %q", confirmation["type"], "redirect")
		}
		if confirmation["return_url"] != yookassaTestReturnURL {
			t.Errorf("confirmation.return_url = %v, want %q", confirmation["return_url"], yookassaTestReturnURL)
		}
		metadata, _ := body["metadata"].(map[string]any)
		if metadata["order_id"] != "42" {
			t.Errorf("metadata.order_id = %v, want %q", metadata["order_id"], "42")
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"pay_1","status":"pending","confirmation":{"confirmation_url":"https://yoomoney/redirect/pay_1"}}`))
	}))
	defer srv.Close()

	client := newYookassaTestClient(srv)

	invoice, err := client.CreatePayment(context.Background(), 42, 199900, "Order 42")
	if err != nil {
		t.Fatalf("CreatePayment returned error: %v", err)
	}
	if invoice.InvoiceID != "pay_1" {
		t.Errorf("InvoiceID = %q, want %q", invoice.InvoiceID, "pay_1")
	}
	if invoice.PayURL != "https://yoomoney/redirect/pay_1" {
		t.Errorf("PayURL = %q, want %q", invoice.PayURL, "https://yoomoney/redirect/pay_1")
	}

	// A retried creation must not reuse the idempotence key: the ledger
	// quarantines a second charge for the same order.
	if _, err := client.CreatePayment(context.Background(), 42, 199900, "Order 42"); err != nil {
		t.Fatalf("second CreatePayment returned error: %v", err)
	}
	if len(idempotenceKeys) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(idempotenceKeys))
	}
	if idempotenceKeys[0] == "" || idempotenceKeys[1] == "" {
		t.Fatalf("expected both requests to carry an Idempotence-Key, got %v", idempotenceKeys)
	}
	if idempotenceKeys[0] == idempotenceKeys[1] {
		t.Fatalf("second request reused Idempotence-Key %q", idempotenceKeys[0])
	}
}

func TestYooKassaCreatePaymentAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"invalid_parameter","description":"bad"}`))
	}))
	defer srv.Close()

	client := newYookassaTestClient(srv)

	_, err := client.CreatePayment(context.Background(), 42, 199900, "Order 42")
	if err == nil {
		t.Fatal("expected error from CreatePayment on API error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid_parameter") {
		t.Fatalf("expected error to mention invalid_parameter, got %q", err.Error())
	}
}

func TestYooKassaCreatePaymentRejectsInvalidInput(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	defer srv.Close()

	client := newYookassaTestClient(srv)

	for _, tc := range []struct {
		name           string
		orderID        int64
		amountRUBMinor int64
	}{
		{name: "order id zero", orderID: 0, amountRUBMinor: 199900},
		{name: "order id negative", orderID: -1, amountRUBMinor: 199900},
		{name: "amount zero", orderID: 42, amountRUBMinor: 0},
		{name: "amount negative", orderID: 42, amountRUBMinor: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.CreatePayment(context.Background(), tc.orderID, tc.amountRUBMinor, "Order 42")
			if !errors.Is(err, ErrInvalidYooKassaReceipt) {
				t.Fatalf("expected ErrInvalidYooKassaReceipt, got %v", err)
			}
		})
	}
	if called {
		t.Fatal("CreatePayment made an HTTP call for invalid input")
	}
}

func TestYooKassaGetPaymentMapsSucceededPayment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/v3/payments/pay_1" {
			t.Errorf("expected path /v3/payments/pay_1, got %s", r.URL.Path)
		}
		requireYooKassaBasicAuth(t, r)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"pay_1","status":"succeeded","paid":true,` +
			`"amount":{"value":"1999.00","currency":"RUB"},` +
			`"metadata":{"order_id":"42"},` +
			`"created_at":"2026-09-19T10:00:00Z","captured_at":"2026-09-19T10:01:00Z"}`))
	}))
	defer srv.Close()

	client := newYookassaTestClient(srv)

	payment, err := client.GetPayment(context.Background(), "pay_1")
	if err != nil {
		t.Fatalf("GetPayment returned error: %v", err)
	}
	want := &Payment{
		ID:         "pay_1",
		Status:     "succeeded",
		Paid:       true,
		Amount:     "1999.00",
		Currency:   "RUB",
		OrderID:    42,
		OccurredAt: time.Date(2026, 9, 19, 10, 1, 0, 0, time.UTC),
	}
	if *payment != *want {
		t.Fatalf("payment = %+v, want %+v", *payment, *want)
	}
}

func TestYooKassaGetPaymentFallsBackToCreatedAt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"pay_1","status":"pending","paid":false,` +
			`"amount":{"value":"1999.00","currency":"RUB"},` +
			`"metadata":{"order_id":"42"},` +
			`"created_at":"2026-09-19T10:00:00Z"}`))
	}))
	defer srv.Close()

	client := newYookassaTestClient(srv)

	payment, err := client.GetPayment(context.Background(), "pay_1")
	if err != nil {
		t.Fatalf("GetPayment returned error: %v", err)
	}
	if !payment.OccurredAt.Equal(time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("OccurredAt = %v, want created_at fallback", payment.OccurredAt)
	}
}

func TestYooKassaPaymentReceiptValidatesEverything(t *testing.T) {
	valid := func() *Payment {
		return &Payment{
			ID:         "pay_1",
			Status:     "succeeded",
			Paid:       true,
			Amount:     "1999.00",
			Currency:   "RUB",
			OrderID:    42,
			OccurredAt: time.Date(2026, 9, 19, 10, 1, 0, 0, time.UTC),
		}
	}

	receipt, err := valid().PaymentReceipt()
	if err != nil {
		t.Fatalf("valid payment receipt error: %v", err)
	}
	if receipt.OrderID != 42 || receipt.Provider != storage.PaymentMethodYooKassa ||
		receipt.ExternalID != "pay_1" || receipt.Currency != "RUB" ||
		receipt.AmountMinor != 199900 || receipt.Scale != 2 ||
		!receipt.OccurredAt.Equal(time.Date(2026, 9, 19, 10, 1, 0, 0, time.UTC)) {
		t.Fatalf("receipt = %+v", receipt)
	}

	mutations := []struct {
		name   string
		mutate func(*Payment)
	}{
		{name: "not paid", mutate: func(p *Payment) { p.Paid = false }},
		{name: "status not succeeded", mutate: func(p *Payment) { p.Status = "pending" }},
		{name: "currency not rub", mutate: func(p *Payment) { p.Currency = "USD" }},
		{name: "amount three fraction digits", mutate: func(p *Payment) { p.Amount = "1999.000" }},
		{name: "amount one fraction digit", mutate: func(p *Payment) { p.Amount = "1999.0" }},
		{name: "amount no fraction digits", mutate: func(p *Payment) { p.Amount = "1999" }},
		{name: "negative amount", mutate: func(p *Payment) { p.Amount = "-5.00" }},
		{name: "non numeric amount", mutate: func(p *Payment) { p.Amount = "abc" }},
		// A missing, zero or negative metadata order_id all leave OrderID
		// unparsable as a positive order reference.
		{name: "order id missing or zero", mutate: func(p *Payment) { p.OrderID = 0 }},
		{name: "order id negative", mutate: func(p *Payment) { p.OrderID = -42 }},
		{name: "id with illegal characters", mutate: func(p *Payment) { p.ID = "pay/1" }},
		{name: "zero timestamp", mutate: func(p *Payment) { p.OccurredAt = time.Time{} }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			p := valid()
			tc.mutate(p)
			if _, err := p.PaymentReceipt(); !errors.Is(err, ErrInvalidYooKassaReceipt) {
				t.Fatalf("expected ErrInvalidYooKassaReceipt, got %v", err)
			}
		})
	}
}

func TestYooKassaParseWebhookExtractsEventAndIDOnly(t *testing.T) {
	client := NewYooKassaPayment(yookassaTestShopID, yookassaTestSecretKey, yookassaTestReturnURL)

	notification, err := client.ParseWebhook([]byte(`{"event":"payment.succeeded","object":{"id":"pay_1","status":"succeeded","paid":true}}`))
	if err != nil {
		t.Fatalf("ParseWebhook returned error: %v", err)
	}
	if notification.Event != "payment.succeeded" || notification.PaymentID != "pay_1" {
		t.Fatalf("notification = %+v", notification)
	}

	_, err = client.ParseWebhook([]byte(`{"object":{"id":"pay_1"}}`))
	if err == nil {
		t.Fatal("expected error for missing event, got nil")
	}
	if !strings.Contains(err.Error(), "event") {
		t.Fatalf("expected missing-event error, got %q", err.Error())
	}

	if _, err := client.ParseWebhook([]byte(`{"event":"payment.canceled","object":{"id":"pay/1"}}`)); !errors.Is(err, ErrInvalidYooKassaReceipt) {
		t.Fatalf("expected ErrInvalidYooKassaReceipt for illegal object id, got %v", err)
	}

	if _, err := client.ParseWebhook([]byte(`not json at all`)); err == nil {
		t.Fatal("expected error for garbage JSON, got nil")
	}

	notification, err = client.ParseWebhook([]byte(`{"event":"payment.waiting_for_capture","object":{"status":"pending"}}`))
	if err != nil {
		t.Fatalf("object without id returned error: %v", err)
	}
	if notification.Event != "payment.waiting_for_capture" || notification.PaymentID != "" {
		t.Fatalf("notification = %+v", notification)
	}
}

func TestYooKassaPaymentAnomalyPreservesFacts(t *testing.T) {
	unpaid := &Payment{
		ID:         "pay_1",
		Status:     "canceled",
		Paid:       false,
		Amount:     "1999.00",
		Currency:   "RUB",
		OrderID:    42,
		OccurredAt: time.Date(2026, 9, 19, 10, 1, 0, 0, time.UTC),
	}
	anomaly, err := unpaid.PaymentAnomaly("webhook_canceled_event")
	if err != nil {
		t.Fatalf("PaymentAnomaly returned error: %v", err)
	}
	if anomaly.Provider != storage.PaymentMethodYooKassa ||
		anomaly.ExternalID != "pay_1" ||
		anomaly.ProposedOrderID != 42 ||
		anomaly.AmountMinor != 199900 || anomaly.Scale != 2 ||
		anomaly.Currency != "RUB" ||
		anomaly.RawAmount != "1999.00" ||
		anomaly.RawPayload != "payment_id:pay_1" ||
		anomaly.Reason != "webhook_canceled_event" ||
		!anomaly.OccurredAt.Equal(unpaid.OccurredAt) {
		t.Fatalf("anomaly did not preserve the payment facts: %+v", anomaly)
	}

	mismatched := &Payment{
		ID:         "pay_2",
		Status:     "succeeded",
		Paid:       true,
		Amount:     "not-a-number",
		Currency:   "RUB",
		OrderID:    0,
		OccurredAt: time.Date(2026, 9, 19, 10, 2, 0, 0, time.UTC),
	}
	mismatchAnomaly, err := mismatched.PaymentAnomaly("amount_not_representable")
	if err != nil {
		t.Fatalf("PaymentAnomaly returned error: %v", err)
	}
	if mismatchAnomaly.AmountMinor != 0 || mismatchAnomaly.Scale != 0 || mismatchAnomaly.RawAmount != "not-a-number" {
		t.Fatalf("mismatched anomaly did not preserve the raw amount: %+v", mismatchAnomaly)
	}

	for _, reason := range []string{"", "   "} {
		if _, err := unpaid.PaymentAnomaly(reason); !errors.Is(err, ErrInvalidYooKassaReceipt) {
			t.Fatalf("reason %q: expected ErrInvalidYooKassaReceipt, got %v", reason, err)
		}
	}
}

func TestYooKassaNotConfiguredFailsClosed(t *testing.T) {
	client := NewYooKassaPayment("", "", "")

	if _, err := client.CreatePayment(context.Background(), 1, 100, "Order 1"); !errors.Is(err, ErrYooKassaNotConfigured) {
		t.Fatalf("CreatePayment: expected ErrYooKassaNotConfigured, got %v", err)
	}
	if _, err := client.GetPayment(context.Background(), "pay_1"); !errors.Is(err, ErrYooKassaNotConfigured) {
		t.Fatalf("GetPayment: expected ErrYooKassaNotConfigured, got %v", err)
	}

	for _, tc := range []struct {
		shopID, secretKey, returnURL string
		configured                   bool
	}{
		{shopID: "", secretKey: "", returnURL: "", configured: false},
		{shopID: yookassaTestShopID, secretKey: "", returnURL: yookassaTestReturnURL, configured: false},
		{shopID: yookassaTestShopID, secretKey: yookassaTestSecretKey, returnURL: "", configured: false},
		{shopID: "", secretKey: yookassaTestSecretKey, returnURL: yookassaTestReturnURL, configured: false},
		{shopID: "  ", secretKey: yookassaTestSecretKey, returnURL: yookassaTestReturnURL, configured: false},
		{shopID: yookassaTestShopID, secretKey: yookassaTestSecretKey, returnURL: yookassaTestReturnURL, configured: true},
	} {
		if got := NewYooKassaPayment(tc.shopID, tc.secretKey, tc.returnURL).Configured(); got != tc.configured {
			t.Errorf("Configured(%q, %q, %q) = %v, want %v", tc.shopID, tc.secretKey, tc.returnURL, got, tc.configured)
		}
	}
}

func TestYooKassaResponseSizeLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("a"), yookassaResponseLimit+1))
	}))
	defer srv.Close()

	client := newYookassaTestClient(srv)

	_, err := client.GetPayment(context.Background(), "pay_1")
	if err == nil {
		t.Fatal("expected error for oversized response, got nil")
	}
	if !strings.Contains(err.Error(), "response is too large") {
		t.Fatalf("expected response size error, got %q", err.Error())
	}
}
