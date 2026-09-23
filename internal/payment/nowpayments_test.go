package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"shop_bot/internal/storage"
)

const (
	nowpaymentsTestAPIKey     = "np_test_api_key"
	nowpaymentsTestIPNSecret  = "np_test_ipn_secret"
	nowpaymentsTestReturnURL  = "https://shop.example/orders/return"
	nowpaymentsTestWebhookURL = "https://shop.example/webhooks/nowpayments"
)

// newNowpaymentsTestClient points a fully configured adapter at the test
// server, mirroring the production base URL https://api.nowpayments.io/v1.
func newNowpaymentsTestClient(srv *httptest.Server) *NowpaymentsPayment {
	client := NewNowpaymentsPayment(nowpaymentsTestAPIKey, nowpaymentsTestIPNSecret,
		nowpaymentsTestReturnURL, nowpaymentsTestWebhookURL)
	client.SetBaseURL(srv.URL + "/v1")
	return client
}

// nowpaymentsSignatureHeader signs the body the way NOWPayments documents:
// canonicalize, then HMAC-SHA512 with the IPN secret, hex-encoded.
func nowpaymentsSignatureHeader(t *testing.T, secret string, body []byte) string {
	t.Helper()
	canonical, err := canonicalizeNowpaymentsIPN(body)
	if err != nil {
		t.Fatalf("canonicalizeNowpaymentsIPN: %v", err)
	}
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write(canonical)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestNowpaymentsCreateInvoiceSendsJSONRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/invoice" {
			t.Errorf("expected path /v1/invoice, got %s", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		if key := r.Header.Get("x-api-key"); key != nowpaymentsTestAPIKey {
			t.Errorf("x-api-key = %q, want %q", key, nowpaymentsTestAPIKey)
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("failed to decode request body: %v", err)
			return
		}
		want := map[string]any{
			"price_amount":      float64(1999) / 100,
			"price_currency":    "usd",
			"order_id":          "42",
			"order_description": "Order 42",
			"ipn_callback_url":  nowpaymentsTestWebhookURL,
			"success_url":       nowpaymentsTestReturnURL,
			"cancel_url":        nowpaymentsTestReturnURL,
		}
		if len(body) != len(want) {
			t.Errorf("request body keys = %v, want exactly %v", body, want)
		}
		for key, wantValue := range want {
			if body[key] != wantValue {
				t.Errorf("body[%q] = %v, want %v", key, body[key], wantValue)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"5077125051","invoice_url":"https://nowpayments.io/payment/?iid=5077125051"}`))
	}))
	defer srv.Close()

	client := newNowpaymentsTestClient(srv)

	invoice, err := client.CreateInvoice(context.Background(), 42, 1999, "Order 42")
	if err != nil {
		t.Fatalf("CreateInvoice returned error: %v", err)
	}
	if invoice.InvoiceID != "5077125051" {
		t.Errorf("InvoiceID = %q, want %q", invoice.InvoiceID, "5077125051")
	}
	if invoice.PayURL != "https://nowpayments.io/payment/?iid=5077125051" {
		t.Errorf("PayURL = %q, want %q", invoice.PayURL, "https://nowpayments.io/payment/?iid=5077125051")
	}
}

func TestNowpaymentsCreateInvoiceAPIError(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "status envelope", body: `{"status":false,"statusCode":400,"message":"price_amount must be a number"}`},
		{name: "bare message", body: `{"message":"price_amount must be a number"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			client := newNowpaymentsTestClient(srv)

			_, err := client.CreateInvoice(context.Background(), 42, 1999, "Order 42")
			if err == nil {
				t.Fatal("expected error from CreateInvoice on API error, got nil")
			}
			if !strings.Contains(err.Error(), "400") {
				t.Errorf("expected error to contain HTTP status 400, got %q", err.Error())
			}
			if !strings.Contains(err.Error(), "price_amount must be a number") {
				t.Errorf("expected error to contain the provider message, got %q", err.Error())
			}
			if strings.Contains(err.Error(), nowpaymentsTestAPIKey) {
				t.Errorf("error leaks the API key: %q", err.Error())
			}
		})
	}
}

func TestNowpaymentsCreateInvoiceRejectsInvalidInput(t *testing.T) {
	var called atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called.Store(true)
	}))
	defer srv.Close()

	client := newNowpaymentsTestClient(srv)

	for _, tc := range []struct {
		name           string
		orderID        int64
		amountUSDCents int64
	}{
		{name: "order id zero", orderID: 0, amountUSDCents: 1999},
		{name: "order id negative", orderID: -1, amountUSDCents: 1999},
		{name: "amount zero", orderID: 42, amountUSDCents: 0},
		{name: "amount negative", orderID: 42, amountUSDCents: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.CreateInvoice(context.Background(), tc.orderID, tc.amountUSDCents, "Order 42")
			if !errors.Is(err, ErrInvalidNowpaymentsReceipt) {
				t.Fatalf("expected ErrInvalidNowpaymentsReceipt, got %v", err)
			}
		})
	}
	if called.Load() {
		t.Fatal("CreateInvoice made an HTTP call for invalid input")
	}
}

func TestNowpaymentsVerifyIPNSignature(t *testing.T) {
	body := []byte(`{"payment_id":5077125051,"payment_status":"finished","order_id":"42","price_amount":19.99,"price_currency":"usd"}`)

	client := NewNowpaymentsPayment(nowpaymentsTestAPIKey, nowpaymentsTestIPNSecret,
		nowpaymentsTestReturnURL, nowpaymentsTestWebhookURL)

	t.Run("valid signature", func(t *testing.T) {
		if err := client.VerifyIPNSignature(nowpaymentsSignatureHeader(t, nowpaymentsTestIPNSecret, body), body); err != nil {
			t.Fatalf("expected valid signature, got %v", err)
		}
	})

	t.Run("valid signature against hand-pinned canonical bytes", func(t *testing.T) {
		// The canonical form is pinned by hand here, NOT through
		// canonicalizeNowpaymentsIPN, so a self-agreeing but wrong
		// canonicalizer cannot pass: sorted keys (recursively), compact,
		// no HTML escaping, no trailing newline.
		pinnedBody := []byte(`{"a":1,"b":{"c":2,"a":3}}`)
		const wantCanonical = `{"a":1,"b":{"a":3,"c":2}}`
		mac := hmac.New(sha512.New, []byte(nowpaymentsTestIPNSecret))
		mac.Write([]byte(wantCanonical))
		header := hex.EncodeToString(mac.Sum(nil))
		if err := client.VerifyIPNSignature(header, pinnedBody); err != nil {
			t.Fatalf("expected valid signature over the pinned canonical bytes, got %v", err)
		}
	})

	t.Run("valid signature over hand-pinned canonical bytes with raw HTML characters", func(t *testing.T) {
		// Hand-pinned canonical form (NOT via canonicalizeNowpaymentsIPN),
		// same discipline as the leg above, extended with the HTML-escape
		// property: raw <, > and & stay raw. An escaping regression makes
		// the canonicalizer's HMAC differ from this pin.
		pinnedBody := []byte(`{"product_name":"<b>&","payment_status":"finished"}`)
		const wantCanonical = `{"payment_status":"finished","product_name":"<b>&"}`
		mac := hmac.New(sha512.New, []byte(nowpaymentsTestIPNSecret))
		mac.Write([]byte(wantCanonical))
		header := hex.EncodeToString(mac.Sum(nil))
		if err := client.VerifyIPNSignature(header, pinnedBody); err != nil {
			t.Fatalf("expected valid signature over the raw-HTML canonical bytes, got %v", err)
		}
	})

	t.Run("key order insensitive", func(t *testing.T) {
		reordered := []byte(`{"price_currency":"usd","price_amount":19.99,"order_id":"42","payment_status":"finished","payment_id":5077125051}`)
		if err := client.VerifyIPNSignature(nowpaymentsSignatureHeader(t, nowpaymentsTestIPNSecret, body), reordered); err != nil {
			t.Fatalf("expected same logical body with different key order to verify, got %v", err)
		}
	})

	t.Run("wrong secret", func(t *testing.T) {
		header := nowpaymentsSignatureHeader(t, "np_other_secret", body)
		if err := client.VerifyIPNSignature(header, body); !errors.Is(err, ErrNowpaymentsSignatureMismatch) {
			t.Fatalf("expected ErrNowpaymentsSignatureMismatch, got %v", err)
		}
	})

	t.Run("tampered body", func(t *testing.T) {
		header := nowpaymentsSignatureHeader(t, nowpaymentsTestIPNSecret, body)
		tampered := []byte(`{"payment_id":5077125051,"payment_status":"finished","order_id":"43","price_amount":19.99,"price_currency":"usd"}`)
		if err := client.VerifyIPNSignature(header, tampered); !errors.Is(err, ErrNowpaymentsSignatureMismatch) {
			t.Fatalf("expected ErrNowpaymentsSignatureMismatch, got %v", err)
		}
	})

	t.Run("non-hex header", func(t *testing.T) {
		if err := client.VerifyIPNSignature("not-hex-at-all", body); !errors.Is(err, ErrNowpaymentsSignatureMalformed) {
			t.Fatalf("expected ErrNowpaymentsSignatureMalformed, got %v", err)
		}
	})

	t.Run("empty header", func(t *testing.T) {
		if err := client.VerifyIPNSignature("", body); !errors.Is(err, ErrNowpaymentsSignatureMalformed) {
			t.Fatalf("expected ErrNowpaymentsSignatureMalformed, got %v", err)
		}
	})

	t.Run("non-JSON body", func(t *testing.T) {
		header := nowpaymentsSignatureHeader(t, nowpaymentsTestIPNSecret, body)
		if err := client.VerifyIPNSignature(header, []byte("not json at all")); !errors.Is(err, ErrNowpaymentsSignatureMalformed) {
			t.Fatalf("expected ErrNowpaymentsSignatureMalformed, got %v", err)
		}
	})

	t.Run("unconfigured secret fails closed", func(t *testing.T) {
		bare := NewNowpaymentsPayment(nowpaymentsTestAPIKey, "", nowpaymentsTestReturnURL, nowpaymentsTestWebhookURL)
		header := nowpaymentsSignatureHeader(t, "", body)
		if err := bare.VerifyIPNSignature(header, body); !errors.Is(err, ErrNowpaymentsNotConfigured) {
			t.Fatalf("expected ErrNowpaymentsNotConfigured, got %v", err)
		}
	})
}

func TestCanonicalizeNowpaymentsIPNKeepsHTMLCharactersRaw(t *testing.T) {
	// Pins the SetEscapeHTML(false) property DIRECTLY: <, > and & must stay
	// raw in the canonical form (keys sorted recursively, compact, no
	// trailing newline). Go's json.Encoder escapes them as \u003c \u003e
	// \u0026 by default; a regression to the default would compute a
	// different HMAC than NOWPayments' signer over any IPN body containing
	// these characters — fail-closed in production (genuine IPNs never
	// verify), and invisible to the signature tests above without this pin.
	got, err := canonicalizeNowpaymentsIPN([]byte(`{"b":"<b>&","a":[1,{"z":"<p>&x"}]}`))
	if err != nil {
		t.Fatalf("canonicalizeNowpaymentsIPN: %v", err)
	}
	const want = `{"a":[1,{"z":"<p>&x"}],"b":"<b>&"}`
	if string(got) != want {
		t.Fatalf("canonical = %s, want %s", got, want)
	}
}

func TestNowpaymentsParseIPN(t *testing.T) {
	client := NewNowpaymentsPayment(nowpaymentsTestAPIKey, nowpaymentsTestIPNSecret,
		nowpaymentsTestReturnURL, nowpaymentsTestWebhookURL)

	t.Run("parses a finished IPN", func(t *testing.T) {
		body := `{"payment_id":5077125051,"payment_status":"finished","order_id":"42","price_amount":19.99,"price_currency":"usd"}`
		ipn, err := client.ParseIPN([]byte(body))
		if err != nil {
			t.Fatalf("ParseIPN: %v", err)
		}
		if ipn.PaymentID != "5077125051" {
			t.Errorf("PaymentID = %q, want %q (decimal string, no exponent)", ipn.PaymentID, "5077125051")
		}
		if ipn.PaymentStatus != "finished" {
			t.Errorf("PaymentStatus = %q, want finished", ipn.PaymentStatus)
		}
		if ipn.OrderID != 42 {
			t.Errorf("OrderID = %d, want 42", ipn.OrderID)
		}
		if ipn.PriceAmount != 19.99 {
			t.Errorf("PriceAmount = %v, want 19.99", ipn.PriceAmount)
		}
		if ipn.PriceCurrency != "USD" {
			t.Errorf("PriceCurrency = %q, want USD (normalized)", ipn.PriceCurrency)
		}
	})

	t.Run("payment id as string", func(t *testing.T) {
		body := `{"payment_id":"5077125051","payment_status":"finished","order_id":"42","price_amount":19.99,"price_currency":"usd"}`
		ipn, err := client.ParseIPN([]byte(body))
		if err != nil {
			t.Fatalf("ParseIPN: %v", err)
		}
		if ipn.PaymentID != "5077125051" {
			t.Errorf("PaymentID = %q, want %q", ipn.PaymentID, "5077125051")
		}
	})

	t.Run("payment id int-valued float stays decimal", func(t *testing.T) {
		body := `{"payment_id":5077125051.0,"payment_status":"finished","order_id":"42","price_amount":19.99,"price_currency":"usd"}`
		ipn, err := client.ParseIPN([]byte(body))
		if err != nil {
			t.Fatalf("ParseIPN: %v", err)
		}
		if ipn.PaymentID != "5077125051" {
			t.Errorf("PaymentID = %q, want %q (no fraction, no exponent)", ipn.PaymentID, "5077125051")
		}
	})

	t.Run("garbage JSON errors", func(t *testing.T) {
		if _, err := client.ParseIPN([]byte("not json at all")); err == nil {
			t.Fatal("expected an error for undecodable JSON")
		}
	})

	t.Run("missing payment id errors", func(t *testing.T) {
		body := `{"payment_status":"finished","order_id":"42","price_amount":19.99,"price_currency":"usd"}`
		if _, err := client.ParseIPN([]byte(body)); !errors.Is(err, ErrInvalidNowpaymentsReceipt) {
			t.Fatalf("expected ErrInvalidNowpaymentsReceipt, got %v", err)
		}
	})
}

func TestNowpaymentsIPNPaymentReceipt(t *testing.T) {
	valid := func() *NowpaymentsIPN {
		return &NowpaymentsIPN{
			PaymentID:     "5077125051",
			PaymentStatus: "finished",
			OrderID:       42,
			PriceAmount:   19.99,
			PriceCurrency: "USD",
		}
	}

	receipt, err := valid().PaymentReceipt()
	if err != nil {
		t.Fatalf("valid IPN receipt error: %v", err)
	}
	if receipt.OrderID != 42 || receipt.Provider != storage.PaymentMethodNowpayments ||
		receipt.ExternalID != "5077125051" || receipt.Currency != "USD" ||
		receipt.AmountMinor != 1999 || receipt.Scale != 2 {
		t.Fatalf("receipt = %+v", receipt)
	}
	// NOWPayments has no Telegram payer identity; PayerID stays zero.
	if receipt.PayerID != 0 {
		t.Fatalf("PayerID = %d, want 0", receipt.PayerID)
	}

	for _, status := range []string{
		"waiting", "confirming", "confirmed", "sending",
		"partially_paid", "failed", "refunded", "expired",
	} {
		t.Run("status "+status+" rejected", func(t *testing.T) {
			ipn := valid()
			ipn.PaymentStatus = status
			if _, err := ipn.PaymentReceipt(); !errors.Is(err, ErrInvalidNowpaymentsReceipt) {
				t.Fatalf("expected ErrInvalidNowpaymentsReceipt, got %v", err)
			}
		})
	}

	mutations := []struct {
		name   string
		mutate func(*NowpaymentsIPN)
	}{
		{name: "currency not usd", mutate: func(n *NowpaymentsIPN) { n.PriceCurrency = "EUR" }},
		{name: "currency not normalized", mutate: func(n *NowpaymentsIPN) { n.PriceCurrency = "usd" }},
		{name: "amount zero", mutate: func(n *NowpaymentsIPN) { n.PriceAmount = 0 }},
		{name: "amount negative", mutate: func(n *NowpaymentsIPN) { n.PriceAmount = -5 }},
		{name: "order id missing", mutate: func(n *NowpaymentsIPN) { n.OrderID = 0 }},
		{name: "order id negative", mutate: func(n *NowpaymentsIPN) { n.OrderID = -42 }},
		{name: "empty payment id", mutate: func(n *NowpaymentsIPN) { n.PaymentID = "" }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			ipn := valid()
			tc.mutate(ipn)
			if _, err := ipn.PaymentReceipt(); !errors.Is(err, ErrInvalidNowpaymentsReceipt) {
				t.Fatalf("expected ErrInvalidNowpaymentsReceipt, got %v", err)
			}
		})
	}
}

func TestNowpaymentsIPNPaymentAnomalyPreservesFacts(t *testing.T) {
	unpaid := &NowpaymentsIPN{
		PaymentID:     "5077125051",
		PaymentStatus: "expired",
		OrderID:       42,
		PriceAmount:   19.99,
		PriceCurrency: "USD",
	}
	anomaly, err := unpaid.PaymentAnomaly("ipn_not_finished")
	if err != nil {
		t.Fatalf("PaymentAnomaly returned error: %v", err)
	}
	if anomaly.Provider != storage.PaymentMethodNowpayments ||
		anomaly.ExternalID != "5077125051" ||
		anomaly.ProposedOrderID != 42 ||
		anomaly.AmountMinor != 1999 || anomaly.Scale != 2 ||
		anomaly.Currency != "USD" ||
		anomaly.RawAmount != "19.99" ||
		anomaly.RawPayload != "payment_id:5077125051" ||
		anomaly.Reason != "ipn_not_finished" {
		t.Fatalf("anomaly did not preserve the IPN facts: %+v", anomaly)
	}

	zeroAmount := &NowpaymentsIPN{
		PaymentID:     "5077125052",
		PaymentStatus: "finished",
		OrderID:       0,
		PriceAmount:   0,
		PriceCurrency: "USD",
	}
	zeroAnomaly, err := zeroAmount.PaymentAnomaly("amount_not_representable")
	if err != nil {
		t.Fatalf("PaymentAnomaly returned error: %v", err)
	}
	if zeroAnomaly.AmountMinor != 0 || zeroAnomaly.Scale != 0 || zeroAnomaly.RawAmount != "0" {
		t.Fatalf("zero-amount anomaly did not preserve the raw amount: %+v", zeroAnomaly)
	}

	for _, reason := range []string{"", "   "} {
		if _, err := unpaid.PaymentAnomaly(reason); !errors.Is(err, ErrInvalidNowpaymentsReceipt) {
			t.Fatalf("reason %q: expected ErrInvalidNowpaymentsReceipt, got %v", reason, err)
		}
	}
}

func TestNowpaymentsNotConfiguredFailsClosed(t *testing.T) {
	client := NewNowpaymentsPayment("", "", "", "")

	if _, err := client.CreateInvoice(context.Background(), 1, 100, "Order 1"); !errors.Is(err, ErrNowpaymentsNotConfigured) {
		t.Fatalf("CreateInvoice: expected ErrNowpaymentsNotConfigured, got %v", err)
	}
	// An empty IPN secret would make every forged signature valid, so
	// verification must fail closed before any signature is compared.
	if err := client.VerifyIPNSignature(strings.Repeat("0", 128), []byte(`{}`)); !errors.Is(err, ErrNowpaymentsNotConfigured) {
		t.Fatalf("VerifyIPNSignature: expected ErrNowpaymentsNotConfigured, got %v", err)
	}

	for _, tc := range []struct {
		apiKey, ipnSecret, returnURL, webhookURL string
		configured                               bool
	}{
		{apiKey: "", ipnSecret: "", returnURL: "", webhookURL: "", configured: false},
		{apiKey: nowpaymentsTestAPIKey, ipnSecret: "", returnURL: nowpaymentsTestReturnURL, webhookURL: nowpaymentsTestWebhookURL, configured: false},
		{apiKey: "", ipnSecret: nowpaymentsTestIPNSecret, returnURL: nowpaymentsTestReturnURL, webhookURL: nowpaymentsTestWebhookURL, configured: false},
		{apiKey: nowpaymentsTestAPIKey, ipnSecret: nowpaymentsTestIPNSecret, returnURL: "", webhookURL: nowpaymentsTestWebhookURL, configured: false},
		{apiKey: nowpaymentsTestAPIKey, ipnSecret: nowpaymentsTestIPNSecret, returnURL: nowpaymentsTestReturnURL, webhookURL: "", configured: false},
		{apiKey: "  ", ipnSecret: nowpaymentsTestIPNSecret, returnURL: nowpaymentsTestReturnURL, webhookURL: nowpaymentsTestWebhookURL, configured: false},
		{apiKey: nowpaymentsTestAPIKey, ipnSecret: nowpaymentsTestIPNSecret, returnURL: nowpaymentsTestReturnURL, webhookURL: nowpaymentsTestWebhookURL, configured: true},
	} {
		if got := NewNowpaymentsPayment(tc.apiKey, tc.ipnSecret, tc.returnURL, tc.webhookURL).Configured(); got != tc.configured {
			t.Errorf("Configured(%q, %q, %q, %q) = %v, want %v",
				tc.apiKey, tc.ipnSecret, tc.returnURL, tc.webhookURL, got, tc.configured)
		}
	}
}
