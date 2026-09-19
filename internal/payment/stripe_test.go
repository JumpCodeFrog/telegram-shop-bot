package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"shop_bot/internal/storage"
)

const (
	stripeTestSecretKey     = "sk_test_secret"
	stripeTestWebhookSecret = "whsec_test_secret"
	stripeTestReturnURL     = "https://shop.example/orders/return"
)

// newStripeTestClient points a fully configured adapter at the test server,
// mirroring the production base URL https://api.stripe.com/v1.
func newStripeTestClient(srv *httptest.Server) *StripePayment {
	client := NewStripePayment(stripeTestSecretKey, stripeTestWebhookSecret, stripeTestReturnURL)
	client.SetBaseURL(srv.URL + "/v1")
	return client
}

// stripeSignatureHeader builds a Stripe-Signature header for the given
// timestamp and body, signed with the given secret.
func stripeSignatureHeader(t *testing.T, secret string, timestamp int64, body []byte) string {
	t.Helper()
	ts := strconv.FormatInt(timestamp, 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func requireStripeBearerAuth(t *testing.T, r *http.Request) {
	t.Helper()
	if want := "Bearer " + stripeTestSecretKey; r.Header.Get("Authorization") != want {
		t.Errorf("Authorization = %q, want %q", r.Header.Get("Authorization"), want)
	}
}

func TestStripeCreateCheckoutSessionSendsFormEncodedRequest(t *testing.T) {
	var idempotencyKeys []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/checkout/sessions" {
			t.Errorf("expected path /v1/checkout/sessions, got %s", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", ct)
		}
		requireStripeBearerAuth(t, r)

		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			t.Error("expected non-empty Idempotency-Key header")
		} else if _, err := uuid.Parse(key); err != nil {
			t.Errorf("Idempotency-Key %q is not a valid uuid: %v", key, err)
		}
		idempotencyKeys = append(idempotencyKeys, key)

		if err := r.ParseForm(); err != nil {
			t.Errorf("failed to parse form body: %v", err)
			return
		}
		want := url.Values{
			"mode":                                   {"payment"},
			"success_url":                            {stripeTestReturnURL},
			"cancel_url":                             {stripeTestReturnURL},
			"client_reference_id":                    {"42"},
			"metadata[order_id]":                     {"42"},
			"line_items[0][quantity]":                {"1"},
			"line_items[0][price_data][currency]":    {"usd"},
			"line_items[0][price_data][unit_amount]": {"199900"},
			"line_items[0][price_data][product_data][name]": {"Order 42"},
		}
		if !reflect.DeepEqual(r.PostForm, want) {
			t.Errorf("form = %v, want exactly %v", r.PostForm, want)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cs_test_1","url":"https://checkout.stripe.com/pay/cs_test_1"}`))
	}))
	defer srv.Close()

	client := newStripeTestClient(srv)

	invoice, err := client.CreateCheckoutSession(context.Background(), 42, 199900, "Order 42")
	if err != nil {
		t.Fatalf("CreateCheckoutSession returned error: %v", err)
	}
	if invoice.InvoiceID != "cs_test_1" {
		t.Errorf("InvoiceID = %q, want %q", invoice.InvoiceID, "cs_test_1")
	}
	if invoice.PayURL != "https://checkout.stripe.com/pay/cs_test_1" {
		t.Errorf("PayURL = %q, want %q", invoice.PayURL, "https://checkout.stripe.com/pay/cs_test_1")
	}

	// A retried creation must not reuse the idempotency key: the ledger
	// quarantines a second charge for the same order.
	if _, err := client.CreateCheckoutSession(context.Background(), 42, 199900, "Order 42"); err != nil {
		t.Fatalf("second CreateCheckoutSession returned error: %v", err)
	}
	if len(idempotencyKeys) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(idempotencyKeys))
	}
	if idempotencyKeys[0] == "" || idempotencyKeys[1] == "" {
		t.Fatalf("expected both requests to carry an Idempotency-Key, got %v", idempotencyKeys)
	}
	if idempotencyKeys[0] == idempotencyKeys[1] {
		t.Fatalf("second request reused Idempotency-Key %q", idempotencyKeys[0])
	}
}

func TestStripeCreateCheckoutSessionAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"amount_too_small","message":"Amount must be at least 50 cents"}}`))
	}))
	defer srv.Close()

	client := newStripeTestClient(srv)

	_, err := client.CreateCheckoutSession(context.Background(), 42, 199900, "Order 42")
	if err == nil {
		t.Fatal("expected error from CreateCheckoutSession on API error, got nil")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("expected error to contain HTTP status 400, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "Amount must be at least 50 cents") {
		t.Errorf("expected error to contain the Stripe message, got %q", err.Error())
	}
	if strings.Contains(err.Error(), stripeTestSecretKey) {
		t.Errorf("error leaks the secret key: %q", err.Error())
	}
}

func TestStripeCreateCheckoutSessionRejectsInvalidInput(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	defer srv.Close()

	client := newStripeTestClient(srv)

	for _, tc := range []struct {
		name        string
		orderID     int64
		amountCents int64
	}{
		{name: "order id zero", orderID: 0, amountCents: 199900},
		{name: "order id negative", orderID: -1, amountCents: 199900},
		{name: "amount zero", orderID: 42, amountCents: 0},
		{name: "amount negative", orderID: 42, amountCents: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.CreateCheckoutSession(context.Background(), tc.orderID, tc.amountCents, "Order 42")
			if !errors.Is(err, ErrInvalidStripeReceipt) {
				t.Fatalf("expected ErrInvalidStripeReceipt, got %v", err)
			}
		})
	}
	if called {
		t.Fatal("CreateCheckoutSession made an HTTP call for invalid input")
	}
}

func TestStripeGetCheckoutSessionParsesSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/v1/checkout/sessions/cs_test_1" {
			t.Errorf("expected path /v1/checkout/sessions/cs_test_1, got %s", r.URL.Path)
		}
		requireStripeBearerAuth(t, r)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cs_test_1","status":"complete","payment_status":"paid",` +
			`"amount_total":199900,"currency":"usd",` +
			`"metadata":{"order_id":"42"},"client_reference_id":"42"}`))
	}))
	defer srv.Close()

	client := newStripeTestClient(srv)

	session, err := client.GetCheckoutSession(context.Background(), "cs_test_1")
	if err != nil {
		t.Fatalf("GetCheckoutSession returned error: %v", err)
	}
	want := &StripeSession{
		ID:            "cs_test_1",
		Status:        "complete",
		PaymentStatus: "paid",
		AmountTotal:   199900,
		Currency:      "USD", // API sends lowercase "usd"; normalized on read
		OrderID:       42,
	}
	if *session != *want {
		t.Fatalf("session = %+v, want %+v", *session, *want)
	}
}

func TestStripeGetCheckoutSessionFallsBackToClientReferenceID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cs_test_2","status":"open","payment_status":"unpaid",` +
			`"amount_total":500,"currency":"usd","client_reference_id":"43"}`))
	}))
	defer srv.Close()

	client := newStripeTestClient(srv)

	session, err := client.GetCheckoutSession(context.Background(), "cs_test_2")
	if err != nil {
		t.Fatalf("GetCheckoutSession returned error: %v", err)
	}
	if session.OrderID != 43 {
		t.Fatalf("OrderID = %d, want client_reference_id fallback 43", session.OrderID)
	}
}

func TestStripeGetCheckoutSessionAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"resource_missing","message":"No such checkout session"}}`))
	}))
	defer srv.Close()

	client := newStripeTestClient(srv)

	_, err := client.GetCheckoutSession(context.Background(), "cs_missing")
	if err == nil {
		t.Fatal("expected error from GetCheckoutSession on API error, got nil")
	}
	if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "No such checkout session") {
		t.Fatalf("expected status and message in error, got %q", err.Error())
	}
}

func TestStripeVerifyWebhookSignature(t *testing.T) {
	fixedNow := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	now := fixedNow.Unix()
	body := []byte(`{"id":"evt_1","type":"checkout.session.completed"}`)

	client := NewStripePayment(stripeTestSecretKey, stripeTestWebhookSecret, stripeTestReturnURL)
	// Clock seam: tests pin the clock through the unexported nowFunc field.
	client.nowFunc = func() time.Time { return fixedNow }

	t.Run("valid signature", func(t *testing.T) {
		if err := client.VerifyWebhookSignature(stripeSignatureHeader(t, stripeTestWebhookSecret, now, body), body); err != nil {
			t.Fatalf("expected valid signature, got %v", err)
		}
	})

	t.Run("unknown keys ignored", func(t *testing.T) {
		header := stripeSignatureHeader(t, stripeTestWebhookSecret, now, body) + ",v0=deadbeef"
		if err := client.VerifyWebhookSignature(header, body); err != nil {
			t.Fatalf("expected unknown keys to be ignored, got %v", err)
		}
	})

	t.Run("multiple v1 one matches", func(t *testing.T) {
		valid := stripeSignatureHeader(t, stripeTestWebhookSecret, now, body)
		header := valid + ",v1=zz,v1=" + strings.Repeat("0", 64)
		if err := client.VerifyWebhookSignature(header, body); err != nil {
			t.Fatalf("expected any matching v1 to accept, got %v", err)
		}
	})

	t.Run("wrong secret", func(t *testing.T) {
		header := stripeSignatureHeader(t, "whsec_other", now, body)
		if err := client.VerifyWebhookSignature(header, body); !errors.Is(err, ErrStripeSignatureMismatch) {
			t.Fatalf("expected ErrStripeSignatureMismatch, got %v", err)
		}
	})

	t.Run("tampered body", func(t *testing.T) {
		header := stripeSignatureHeader(t, stripeTestWebhookSecret, now, body)
		if err := client.VerifyWebhookSignature(header, []byte(`{"id":"evt_2"}`)); !errors.Is(err, ErrStripeSignatureMismatch) {
			t.Fatalf("expected ErrStripeSignatureMismatch, got %v", err)
		}
	})

	t.Run("timestamp at tolerance boundary", func(t *testing.T) {
		header := stripeSignatureHeader(t, stripeTestWebhookSecret, now-300, body)
		if err := client.VerifyWebhookSignature(header, body); err != nil {
			t.Fatalf("expected timestamp at -300s to be accepted, got %v", err)
		}
	})

	t.Run("stale timestamp", func(t *testing.T) {
		header := stripeSignatureHeader(t, stripeTestWebhookSecret, now-301, body)
		if err := client.VerifyWebhookSignature(header, body); !errors.Is(err, ErrStripeSignatureStale) {
			t.Fatalf("expected ErrStripeSignatureStale, got %v", err)
		}
	})

	t.Run("future timestamp beyond tolerance", func(t *testing.T) {
		header := stripeSignatureHeader(t, stripeTestWebhookSecret, now+301, body)
		if err := client.VerifyWebhookSignature(header, body); !errors.Is(err, ErrStripeSignatureStale) {
			t.Fatalf("expected ErrStripeSignatureStale, got %v", err)
		}
	})

	t.Run("garbage header", func(t *testing.T) {
		if err := client.VerifyWebhookSignature("not a signature header", body); !errors.Is(err, ErrStripeSignatureMalformed) {
			t.Fatalf("expected ErrStripeSignatureMalformed, got %v", err)
		}
	})

	t.Run("missing timestamp", func(t *testing.T) {
		if err := client.VerifyWebhookSignature("v1="+strings.Repeat("0", 64), body); !errors.Is(err, ErrStripeSignatureMalformed) {
			t.Fatalf("expected ErrStripeSignatureMalformed, got %v", err)
		}
	})

	t.Run("missing v1", func(t *testing.T) {
		if err := client.VerifyWebhookSignature("t="+strconv.FormatInt(now, 10), body); !errors.Is(err, ErrStripeSignatureMalformed) {
			t.Fatalf("expected ErrStripeSignatureMalformed, got %v", err)
		}
	})

	t.Run("non-integer timestamp", func(t *testing.T) {
		if err := client.VerifyWebhookSignature("t=soon,v1="+strings.Repeat("0", 64), body); !errors.Is(err, ErrStripeSignatureMalformed) {
			t.Fatalf("expected ErrStripeSignatureMalformed, got %v", err)
		}
	})
}

func TestStripeSessionPaymentReceipt(t *testing.T) {
	valid := func() *StripeSession {
		return &StripeSession{
			ID:            "cs_test_1",
			Status:        "complete",
			PaymentStatus: "paid",
			AmountTotal:   199900,
			Currency:      "USD",
			OrderID:       42,
		}
	}

	receipt, err := valid().PaymentReceipt()
	if err != nil {
		t.Fatalf("valid session receipt error: %v", err)
	}
	if receipt.OrderID != 42 || receipt.Provider != storage.PaymentMethodStripe ||
		receipt.ExternalID != "cs_test_1" || receipt.Currency != "USD" ||
		receipt.AmountMinor != 199900 || receipt.Scale != 2 {
		t.Fatalf("receipt = %+v", receipt)
	}
	// Stripe has no Telegram payer identity; PayerID stays zero.
	if receipt.PayerID != 0 {
		t.Fatalf("PayerID = %d, want 0", receipt.PayerID)
	}

	mutations := []struct {
		name   string
		mutate func(*StripeSession)
	}{
		{name: "open and unpaid", mutate: func(s *StripeSession) { s.Status, s.PaymentStatus = "open", "unpaid" }},
		{name: "complete but unpaid", mutate: func(s *StripeSession) { s.PaymentStatus = "unpaid" }},
		{name: "expired", mutate: func(s *StripeSession) { s.Status, s.PaymentStatus = "expired", "unpaid" }},
		{name: "currency not usd", mutate: func(s *StripeSession) { s.Currency = "EUR" }},
		{name: "currency not normalized", mutate: func(s *StripeSession) { s.Currency = "usd" }},
		{name: "amount zero", mutate: func(s *StripeSession) { s.AmountTotal = 0 }},
		{name: "amount negative", mutate: func(s *StripeSession) { s.AmountTotal = -5 }},
		{name: "order id missing", mutate: func(s *StripeSession) { s.OrderID = 0 }},
		{name: "order id negative", mutate: func(s *StripeSession) { s.OrderID = -42 }},
		{name: "empty session id", mutate: func(s *StripeSession) { s.ID = "" }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			s := valid()
			tc.mutate(s)
			if _, err := s.PaymentReceipt(); !errors.Is(err, ErrInvalidStripeReceipt) {
				t.Fatalf("expected ErrInvalidStripeReceipt, got %v", err)
			}
		})
	}
}

func TestStripeSessionPaymentAnomalyPreservesFacts(t *testing.T) {
	unpaid := &StripeSession{
		ID:            "cs_test_1",
		Status:        "expired",
		PaymentStatus: "unpaid",
		AmountTotal:   199900,
		Currency:      "USD",
		OrderID:       42,
	}
	anomaly, err := unpaid.PaymentAnomaly("webhook_session_expired")
	if err != nil {
		t.Fatalf("PaymentAnomaly returned error: %v", err)
	}
	if anomaly.Provider != storage.PaymentMethodStripe ||
		anomaly.ExternalID != "cs_test_1" ||
		anomaly.ProposedOrderID != 42 ||
		anomaly.AmountMinor != 199900 || anomaly.Scale != 2 ||
		anomaly.Currency != "USD" ||
		anomaly.RawAmount != "199900" ||
		anomaly.RawPayload != "session_id:cs_test_1" ||
		anomaly.Reason != "webhook_session_expired" {
		t.Fatalf("anomaly did not preserve the session facts: %+v", anomaly)
	}

	mismatched := &StripeSession{
		ID:            "cs_test_2",
		Status:        "complete",
		PaymentStatus: "paid",
		AmountTotal:   0,
		Currency:      "USD",
		OrderID:       0,
	}
	mismatchAnomaly, err := mismatched.PaymentAnomaly("amount_not_representable")
	if err != nil {
		t.Fatalf("PaymentAnomaly returned error: %v", err)
	}
	if mismatchAnomaly.AmountMinor != 0 || mismatchAnomaly.Scale != 0 || mismatchAnomaly.RawAmount != "0" {
		t.Fatalf("mismatched anomaly did not preserve the raw amount: %+v", mismatchAnomaly)
	}

	for _, reason := range []string{"", "   "} {
		if _, err := unpaid.PaymentAnomaly(reason); !errors.Is(err, ErrInvalidStripeReceipt) {
			t.Fatalf("reason %q: expected ErrInvalidStripeReceipt, got %v", reason, err)
		}
	}
}

func TestStripeNotConfiguredFailsClosed(t *testing.T) {
	client := NewStripePayment("", "", "")

	if _, err := client.CreateCheckoutSession(context.Background(), 1, 100, "Order 1"); !errors.Is(err, ErrStripeNotConfigured) {
		t.Fatalf("CreateCheckoutSession: expected ErrStripeNotConfigured, got %v", err)
	}
	if _, err := client.GetCheckoutSession(context.Background(), "cs_test_1"); !errors.Is(err, ErrStripeNotConfigured) {
		t.Fatalf("GetCheckoutSession: expected ErrStripeNotConfigured, got %v", err)
	}
	// An empty webhook secret would make every forged signature valid, so
	// verification must fail closed before any signature is compared.
	if err := client.VerifyWebhookSignature("t=1,v1=abc", []byte(`{}`)); !errors.Is(err, ErrStripeNotConfigured) {
		t.Fatalf("VerifyWebhookSignature: expected ErrStripeNotConfigured, got %v", err)
	}

	for _, tc := range []struct {
		secretKey, webhookSecret, returnURL string
		configured                          bool
	}{
		{secretKey: "", webhookSecret: "", returnURL: "", configured: false},
		{secretKey: stripeTestSecretKey, webhookSecret: "", returnURL: stripeTestReturnURL, configured: false},
		{secretKey: stripeTestSecretKey, webhookSecret: stripeTestWebhookSecret, returnURL: "", configured: false},
		{secretKey: "", webhookSecret: stripeTestWebhookSecret, returnURL: stripeTestReturnURL, configured: false},
		{secretKey: "  ", webhookSecret: stripeTestWebhookSecret, returnURL: stripeTestReturnURL, configured: false},
		{secretKey: stripeTestSecretKey, webhookSecret: stripeTestWebhookSecret, returnURL: stripeTestReturnURL, configured: true},
	} {
		if got := NewStripePayment(tc.secretKey, tc.webhookSecret, tc.returnURL).Configured(); got != tc.configured {
			t.Errorf("Configured(%q, %q, %q) = %v, want %v", tc.secretKey, tc.webhookSecret, tc.returnURL, got, tc.configured)
		}
	}
}
