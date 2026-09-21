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
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

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

func TestYooKassaGetPaymentRejectsInvalidID(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	defer srv.Close()

	client := newYookassaTestClient(srv)

	for _, tc := range []struct {
		name      string
		paymentID string
	}{
		{name: "empty", paymentID: ""},
		{name: "path separator", paymentID: "pay/1"},
		{name: "space", paymentID: "pay 1"},
		{name: "over 64 chars", paymentID: strings.Repeat("a", 65)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := client.GetPayment(context.Background(), tc.paymentID); !errors.Is(err, ErrInvalidYooKassaReceipt) {
				t.Fatalf("expected ErrInvalidYooKassaReceipt, got %v", err)
			}
		})
	}
	if called {
		t.Fatal("GetPayment made an HTTP call for an invalid id")
	}
}

func TestYooKassaGetPaymentAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"resource_not_found","description":"Payment not found"}`))
	}))
	defer srv.Close()

	client := newYookassaTestClient(srv)

	_, err := client.GetPayment(context.Background(), "pay_404")
	if err == nil || !strings.Contains(err.Error(), "resource_not_found") {
		t.Fatalf("expected API error mentioning resource_not_found, got %v", err)
	}
}

func TestYooKassaGetPaymentDecodeFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":`))
	}))
	defer srv.Close()

	client := newYookassaTestClient(srv)

	_, err := client.GetPayment(context.Background(), "pay_1")
	if err == nil || !strings.Contains(err.Error(), "parse payment response") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

// TestYooKassaGetPaymentToPaymentBranches covers the toPayment normalization
// legs through GetPayment: an invalid id in a 200 body fails closed, while
// metadata/timestamp/currency quirks normalize exactly (never an error, never
// a wrong receipt).
func TestYooKassaGetPaymentToPaymentBranches(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		want    *Payment // nil ⇒ wantErr must be set
		wantErr error
	}{
		{
			name:    "invalid id in body fails closed",
			body:    `{"id":"pay/1","status":"succeeded","paid":true,"amount":{"value":"1999.00","currency":"RUB"},"metadata":{"order_id":"42"},"created_at":"2026-09-19T10:00:00Z"}`,
			wantErr: ErrInvalidYooKassaReceipt,
		},
		{
			name: "missing metadata order_id stays zero",
			body: `{"id":"pay_1","status":"succeeded","paid":true,"amount":{"value":"1999.00","currency":"RUB"},"created_at":"2026-09-19T10:00:00Z"}`,
			want: &Payment{ID: "pay_1", Status: "succeeded", Paid: true, Amount: "1999.00", Currency: "RUB",
				OrderID: 0, OccurredAt: time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)},
		},
		{
			name: "unparsable metadata order_id stays zero",
			body: `{"id":"pay_1","status":"succeeded","paid":true,"amount":{"value":"1999.00","currency":"RUB"},"metadata":{"order_id":"abc"},"created_at":"2026-09-19T10:00:00Z"}`,
			want: &Payment{ID: "pay_1", Status: "succeeded", Paid: true, Amount: "1999.00", Currency: "RUB",
				OrderID: 0, OccurredAt: time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)},
		},
		{
			name: "malformed captured_at falls back to created_at",
			body: `{"id":"pay_1","status":"succeeded","paid":true,"amount":{"value":"1999.00","currency":"RUB"},"metadata":{"order_id":"42"},"created_at":"2026-09-19T10:00:00Z","captured_at":"not-a-time"}`,
			want: &Payment{ID: "pay_1", Status: "succeeded", Paid: true, Amount: "1999.00", Currency: "RUB",
				OrderID: 42, OccurredAt: time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)},
		},
		{
			name: "both timestamps absent leave the zero time",
			body: `{"id":"pay_1","status":"pending","paid":false,"amount":{"value":"1999.00","currency":"RUB"},"metadata":{"order_id":"42"}}`,
			want: &Payment{ID: "pay_1", Status: "pending", Paid: false, Amount: "1999.00", Currency: "RUB",
				OrderID: 42, OccurredAt: time.Time{}},
		},
		{
			name: "lowercase currency normalizes to upper",
			body: `{"id":"pay_1","status":"succeeded","paid":true,"amount":{"value":"1999.00","currency":"rub"},"metadata":{"order_id":"42"},"captured_at":"2026-09-19T10:01:00Z"}`,
			want: &Payment{ID: "pay_1", Status: "succeeded", Paid: true, Amount: "1999.00", Currency: "RUB",
				OrderID: 42, OccurredAt: time.Date(2026, 9, 19, 10, 1, 0, 0, time.UTC)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			client := newYookassaTestClient(srv)

			got, err := client.GetPayment(context.Background(), "pay_1")
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetPayment: %v", err)
			}
			if *got != *tc.want {
				t.Fatalf("payment = %+v, want %+v", *got, *tc.want)
			}
		})
	}
}

func TestYooKassaCreateRefundSendsJSONRequest(t *testing.T) {
	var idempotenceKeys []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v3/refunds" {
			t.Errorf("expected path /v3/refunds, got %s", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type application/json, got %q", r.Header.Get("Content-Type"))
		}
		requireYooKassaBasicAuth(t, r)

		key := r.Header.Get("Idempotence-Key")
		if key == "" {
			t.Error("expected non-empty Idempotence-Key header")
		} else if _, err := uuid.Parse(key); err != nil {
			t.Errorf("Idempotence-Key %q is not a valid uuid: %v", key, err)
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
		if len(body) != 3 {
			t.Errorf("body = %v, want exactly amount, description, payment_id", body)
		}
		amount, _ := body["amount"].(map[string]any)
		if len(amount) != 2 {
			t.Errorf("amount = %v, want exactly value and currency", amount)
		}
		// Minor units become an exact two-decimal string: 184908 → "1849.08".
		if amount["value"] != "1849.08" {
			t.Errorf("amount.value = %v, want %q", amount["value"], "1849.08")
		}
		if amount["currency"] != "rub" {
			t.Errorf("amount.currency = %v, want %q", amount["currency"], "rub")
		}
		if body["description"] != "Refund for order 42" {
			t.Errorf("description = %v, want %q", body["description"], "Refund for order 42")
		}
		if body["payment_id"] != "pay_1" {
			t.Errorf("payment_id = %v, want %q", body["payment_id"], "pay_1")
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"rf_1","status":"succeeded"}`))
	}))
	defer srv.Close()

	client := newYookassaTestClient(srv)

	refund, err := client.CreateRefund(context.Background(), "pay_1", 184908, "Refund for order 42", "")
	if err != nil {
		t.Fatalf("CreateRefund returned error: %v", err)
	}
	if refund.ID != "rf_1" || refund.Status != "succeeded" {
		t.Fatalf("refund = %+v, want {ID:rf_1 Status:succeeded}", refund)
	}

	// A second refund call must carry a fresh idempotence key: two adapter
	// calls are two distinct money-out operations and the provider must never
	// collapse the second into a replay of the first.
	if _, err := client.CreateRefund(context.Background(), "pay_1", 184908, "Refund for order 42", ""); err != nil {
		t.Fatalf("second CreateRefund returned error: %v", err)
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

// TestYooKassaCreateRefundIdempotenceKeyPassthrough pins the caller-supplied
// key contract: a non-empty idempotencyKey is sent verbatim as the
// Idempotence-Key header (YooKassa's spelling; the bot's deterministic refund
// key), and two calls with the SAME key repeat it — that is exactly the
// provider-side dedup the refund flow relies on after a ledger-recording
// failure.
func TestYooKassaCreateRefundIdempotenceKeyPassthrough(t *testing.T) {
	var idempotenceKeys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idempotenceKeys = append(idempotenceKeys, r.Header.Get("Idempotence-Key"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"rf_9","status":"succeeded"}`))
	}))
	defer srv.Close()

	client := newYookassaTestClient(srv)
	const key = "refund:42:184908:pay_1"
	for i := 0; i < 2; i++ {
		if _, err := client.CreateRefund(context.Background(), "pay_1", 184908, "Refund for order 42", key); err != nil {
			t.Fatalf("CreateRefund returned error: %v", err)
		}
	}
	if len(idempotenceKeys) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(idempotenceKeys))
	}
	for _, got := range idempotenceKeys {
		if got != key {
			t.Fatalf("Idempotence-Key = %q, want the caller-supplied %q", got, key)
		}
	}
}

func TestYooKassaCreateRefundStatusPassthrough(t *testing.T) {
	// Every status — including "canceled" — must come back verbatim: the
	// adapter reports facts, the CALLER decides what a status means for the
	// order and the ledger.
	for _, status := range []string{"succeeded", "pending", "canceled"} {
		t.Run(status, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"rf_2","status":"` + status + `"}`))
			}))
			defer srv.Close()

			refund, err := newYookassaTestClient(srv).CreateRefund(context.Background(), "pay_1", 199900, "Refund for order 42", "")
			if err != nil {
				t.Fatalf("CreateRefund returned error for status %q: %v", status, err)
			}
			if refund.ID != "rf_2" || refund.Status != status {
				t.Fatalf("refund = %+v, want {ID:rf_2 Status:%s}", refund, status)
			}
		})
	}
}

func TestYooKassaCreateRefundRejectsInvalidInput(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	defer srv.Close()

	client := newYookassaTestClient(srv)

	for _, tc := range []struct {
		name        string
		paymentID   string
		amountMinor int64
	}{
		// Partial-or-full is the caller's math; the adapter never guesses an
		// amount, so a non-positive amount is a hard rejection, not a full
		// refund.
		{name: "amount zero", paymentID: "pay_1", amountMinor: 0},
		{name: "amount negative", paymentID: "pay_1", amountMinor: -1},
		{name: "payment id empty", paymentID: "", amountMinor: 199900},
		{name: "payment id with illegal characters", paymentID: "pay/1", amountMinor: 199900},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.CreateRefund(context.Background(), tc.paymentID, tc.amountMinor, "Refund for order 42", "")
			if !errors.Is(err, ErrInvalidYooKassaReceipt) {
				t.Fatalf("expected ErrInvalidYooKassaReceipt, got %v", err)
			}
		})
	}
	if called {
		t.Fatal("CreateRefund made an HTTP call for invalid input")
	}
}

func TestYooKassaCreateRefundAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"invalid_parameter","description":"bad"}`))
	}))
	defer srv.Close()

	client := newYookassaTestClient(srv)

	_, err := client.CreateRefund(context.Background(), "pay_1", 199900, "Refund for order 42", "")
	if err == nil {
		t.Fatal("expected error from CreateRefund on API error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid_parameter") {
		t.Fatalf("expected error to mention invalid_parameter, got %q", err.Error())
	}

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer broken.Close()

	brokenClient := newYookassaTestClient(broken)

	_, err = brokenClient.CreateRefund(context.Background(), "pay_1", 199900, "Refund for order 42", "")
	if err == nil {
		t.Fatal("expected error from CreateRefund on 5xx, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP status 500") {
		t.Fatalf("expected HTTP status error, got %q", err.Error())
	}
}

func TestYooKassaCreateRefundRejectsMissingRefundID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"succeeded"}`))
	}))
	defer srv.Close()

	// Fail closed: a refund response without an id broke the API contract,
	// and recording an anonymous money-out movement is worse than an error.
	if _, err := newYookassaTestClient(srv).CreateRefund(context.Background(), "pay_1", 199900, "Refund for order 42", ""); err == nil {
		t.Fatal("expected error for a refund response without an id, got nil")
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
	if _, _, err := client.ListPayments(context.Background(), "succeeded", time.Time{}, "", 50); !errors.Is(err, ErrYooKassaNotConfigured) {
		t.Fatalf("ListPayments: expected ErrYooKassaNotConfigured, got %v", err)
	}
	if _, err := client.CreateRefund(context.Background(), "pay_1", 100, "Refund", ""); !errors.Is(err, ErrYooKassaNotConfigured) {
		t.Fatalf("CreateRefund: expected ErrYooKassaNotConfigured, got %v", err)
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

func TestYooKassaListPaymentsSendsFiltersAndParsesItems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/v3/payments" {
			t.Errorf("expected path /v3/payments, got %s", r.URL.Path)
		}
		requireYooKassaBasicAuth(t, r)

		q := r.URL.Query()
		if got := q.Get("status"); got != "succeeded" {
			t.Errorf("status = %q, want %q", got, "succeeded")
		}
		if got := q.Get("created_at.gte"); got != "2026-09-19T10:00:00Z" {
			t.Errorf("created_at.gte = %q, want %q", got, "2026-09-19T10:00:00Z")
		}
		if got := q.Get("cursor"); got != "cursor_2" {
			t.Errorf("cursor = %q, want %q", got, "cursor_2")
		}
		if got := q.Get("limit"); got != "50" {
			t.Errorf("limit = %q, want %q", got, "50")
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"list","items":[` +
			`{"id":"pay_1","status":"succeeded","paid":true,` +
			`"amount":{"value":"1999.00","currency":"RUB"},` +
			`"metadata":{"order_id":"42"},` +
			`"created_at":"2026-09-19T10:00:00Z","captured_at":"2026-09-19T10:01:00Z"}` +
			`],"next_cursor":"cursor_3"}`))
	}))
	defer srv.Close()

	client := newYookassaTestClient(srv)
	createdAtGte := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)

	items, nextCursor, err := client.ListPayments(context.Background(), "succeeded", createdAtGte, "cursor_2", 50)
	if err != nil {
		t.Fatalf("ListPayments returned error: %v", err)
	}
	if nextCursor != "cursor_3" {
		t.Errorf("nextCursor = %q, want %q", nextCursor, "cursor_3")
	}
	want := Payment{
		ID:         "pay_1",
		Status:     "succeeded",
		Paid:       true,
		Amount:     "1999.00",
		Currency:   "RUB",
		OrderID:    42,
		OccurredAt: time.Date(2026, 9, 19, 10, 1, 0, 0, time.UTC),
	}
	if len(items) != 1 || items[0] != want {
		t.Fatalf("items = %+v, want [%+v]", items, want)
	}

	// A list item must build the same receipt as a refetched payment: the
	// poller settles orders through the existing receipt path.
	receipt, err := items[0].PaymentReceipt()
	if err != nil {
		t.Fatalf("PaymentReceipt returned error: %v", err)
	}
	if receipt.OrderID != 42 || receipt.Provider != storage.PaymentMethodYooKassa ||
		receipt.ExternalID != "pay_1" || receipt.Currency != "RUB" ||
		receipt.AmountMinor != 199900 || receipt.Scale != 2 ||
		!receipt.OccurredAt.Equal(time.Date(2026, 9, 19, 10, 1, 0, 0, time.UTC)) {
		t.Fatalf("receipt = %+v", receipt)
	}
}

func TestYooKassaListPaymentsEmptyPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"list","items":[]}`))
	}))
	defer srv.Close()

	client := newYookassaTestClient(srv)

	items, nextCursor, err := client.ListPayments(context.Background(), "succeeded", time.Time{}, "", 50)
	if err != nil {
		t.Fatalf("ListPayments returned error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("items = %+v, want empty", items)
	}
	if nextCursor != "" {
		t.Fatalf("nextCursor = %q, want empty at the end of the list", nextCursor)
	}
}

func TestYooKassaListPaymentsOmitsEmptyOptionalFilters(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = w.Write([]byte(`{"type":"list","items":[]}`))
	}))
	defer srv.Close()

	client := newYookassaTestClient(srv)

	// A zero time omits the created_at filter (scanning the full history is
	// the caller's choice); an empty cursor omits the cursor param (first page).
	if _, _, err := client.ListPayments(context.Background(), "succeeded", time.Time{}, "", 10); err != nil {
		t.Fatalf("ListPayments returned error: %v", err)
	}
	if _, ok := got["created_at.gte"]; ok {
		t.Errorf("created_at.gte sent for a zero time: %v", got["created_at.gte"])
	}
	if _, ok := got["cursor"]; ok {
		t.Errorf("cursor sent for an empty cursor: %v", got["cursor"])
	}
	if got.Get("status") != "succeeded" || got.Get("limit") != "10" {
		t.Errorf("expected status and limit to survive, got %v", got)
	}

	// An empty status must not be sent as an empty filter value.
	if _, _, err := client.ListPayments(context.Background(), "", time.Time{}, "", 10); err != nil {
		t.Fatalf("ListPayments with empty status returned error: %v", err)
	}
	if _, ok := got["status"]; ok {
		t.Errorf("status sent for an empty status: %v", got["status"])
	}
}

func TestYooKassaListPaymentsClampsLimit(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = w.Write([]byte(`{"type":"list","items":[]}`))
	}))
	defer srv.Close()

	client := newYookassaTestClient(srv)

	for _, tc := range []struct {
		name  string
		limit int
		want  string
	}{
		{name: "zero clamps to one", limit: 0, want: "1"},
		{name: "oversized clamps to the YooKassa maximum", limit: 150, want: "100"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := client.ListPayments(context.Background(), "succeeded", time.Time{}, "", tc.limit); err != nil {
				t.Fatalf("ListPayments returned error: %v", err)
			}
			if got.Get("limit") != tc.want {
				t.Fatalf("limit = %q, want %q", got.Get("limit"), tc.want)
			}
		})
	}
}

func TestYooKassaListPaymentsRejectsMalformedItem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"type":"list","items":[{"id":"pay/1","status":"succeeded","paid":true}]}`))
	}))
	defer srv.Close()

	client := newYookassaTestClient(srv)

	_, _, err := client.ListPayments(context.Background(), "succeeded", time.Time{}, "", 50)
	if !errors.Is(err, ErrInvalidYooKassaReceipt) {
		t.Fatalf("expected ErrInvalidYooKassaReceipt for a malformed list item, got %v", err)
	}
}

func TestYooKassaListPaymentsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"invalid_parameter","description":"bad"}`))
	}))
	defer srv.Close()

	client := newYookassaTestClient(srv)

	_, _, err := client.ListPayments(context.Background(), "succeeded", time.Time{}, "", 50)
	if err == nil {
		t.Fatal("expected error from ListPayments on API error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid_parameter") {
		t.Fatalf("expected error to mention invalid_parameter, got %q", err.Error())
	}

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer broken.Close()

	brokenClient := newYookassaTestClient(broken)

	_, _, err = brokenClient.ListPayments(context.Background(), "succeeded", time.Time{}, "", 50)
	if err == nil {
		t.Fatal("expected error from ListPayments on 5xx, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP status 500") {
		t.Fatalf("expected HTTP status error, got %q", err.Error())
	}
}
