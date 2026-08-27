package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// Feature: shop_bot, Property 11: Round-trip подписи webhook CryptoBot
// Validates: Requirements 7.3, 7.7
func TestCryptoBotWebhookSignatureRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Use StringMatching to ensure token is non-empty after trimming (not all whitespace)
		token := rapid.StringMatching(`\S.*`).Draw(t, "token")
		// \S in Go regexp excludes [\t\n\f\r ] but NOT \v (0x0B); TrimSpace trims \v too.
		if strings.TrimSpace(token) == "" {
			t.Skip("token is all whitespace after TrimSpace (e.g. \\v); skipping")
		}
		body := rapid.SliceOfN(rapid.Byte(), 1, 500).Draw(t, "body")

		// Compute correct signature: SHA256(token) → HMAC-SHA256(body) → hex
		secret := sha256.Sum256([]byte(token))
		mac := hmac.New(sha256.New, secret[:])
		mac.Write(body)
		validSig := hex.EncodeToString(mac.Sum(nil))

		cb := NewCryptoBotPayment(token)

		if !cb.VerifyWebhook(body, validSig) {
			t.Fatalf("VerifyWebhook returned false for a correctly computed signature")
		}
	})
}

// Feature: shop_bot, Property 11: Round-trip подписи webhook CryptoBot (negative)
// Validates: Requirements 7.3, 7.7
func TestCryptoBotWebhookSignatureRejectsInvalid(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		token := rapid.StringN(1, 50, 100).Draw(t, "token")
		body := rapid.SliceOfN(rapid.Byte(), 1, 500).Draw(t, "body")
		invalidSig := rapid.StringMatching(`[0-9a-f]{64}`).Draw(t, "invalidSig")

		// Compute the correct signature to ensure invalidSig differs
		secret := sha256.Sum256([]byte(token))
		mac := hmac.New(sha256.New, secret[:])
		mac.Write(body)
		validSig := hex.EncodeToString(mac.Sum(nil))

		if invalidSig == validSig {
			t.Skip("randomly generated signature matches valid one; skipping")
		}

		cb := NewCryptoBotPayment(token)

		if cb.VerifyWebhook(body, invalidSig) {
			t.Fatalf("VerifyWebhook returned true for an invalid signature %q", invalidSig)
		}
	})
}

// Unit test: CreateInvoice sends correct request to CryptoBot API
// Validates: Requirements 7.1
func TestCreateInvoice_CorrectRequest(t *testing.T) {
	var receivedHeader string
	var receivedBody createInvoiceRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("Crypto-Pay-API-Token")

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		if err := json.Unmarshal(body, &receivedBody); err != nil {
			t.Fatalf("failed to unmarshal request body: %v", err)
		}

		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type application/json, got %q", r.Header.Get("Content-Type"))
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/createInvoice" {
			t.Errorf("expected path /createInvoice, got %s", r.URL.Path)
		}

		resp := `{"ok":true,"result":{"invoice_id":12345,"pay_url":"https://pay.crypt.bot/invoice/12345"}}`
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(resp))
	}))
	defer srv.Close()

	cb := &CryptoBotPayment{
		token:   "test-api-token",
		baseURL: srv.URL,
		client:  &http.Client{},
	}

	inv, err := cb.CreateInvoice(context.Background(), 42, 19.99, "Test order")
	if err != nil {
		t.Fatalf("CreateInvoice returned error: %v", err)
	}

	// Verify header
	if receivedHeader != "test-api-token" {
		t.Errorf("expected Crypto-Pay-API-Token %q, got %q", "test-api-token", receivedHeader)
	}

	// Verify JSON body fields
	if receivedBody.Asset != "USDT" {
		t.Errorf("expected asset USDT, got %q", receivedBody.Asset)
	}
	if receivedBody.Amount != "19.99" {
		t.Errorf("expected amount 19.99, got %q", receivedBody.Amount)
	}
	if receivedBody.Description != "Test order" {
		t.Errorf("expected description %q, got %q", "Test order", receivedBody.Description)
	}
	if receivedBody.Payload != "42" {
		t.Errorf("expected payload %q, got %q", "42", receivedBody.Payload)
	}

	// Verify returned invoice
	if inv.InvoiceID != "12345" {
		t.Errorf("expected InvoiceID %q, got %q", "12345", inv.InvoiceID)
	}
	if inv.PayURL != "https://pay.crypt.bot/invoice/12345" {
		t.Errorf("expected PayURL %q, got %q", "https://pay.crypt.bot/invoice/12345", inv.PayURL)
	}
}

func TestCreateInvoiceRoundsWithLedgerMinorUnits(t *testing.T) {
	var received createInvoiceRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"invoice_id":7,"pay_url":"https://pay.example/7"}}`)
	}))
	defer server.Close()
	client := NewCryptoBotPayment("token")
	client.baseURL = server.URL
	if _, err := client.CreateInvoice(context.Background(), 7, 2.675, "rounding"); err != nil {
		t.Fatal(err)
	}
	if received.Amount != "2.68" {
		t.Fatalf("invoice amount=%q, want ledger-compatible 2.68", received.Amount)
	}
}

func TestCreateInvoicePrefersCurrentBotInvoiceURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true,"result":{"invoice_id":7,"bot_invoice_url":"https://t.me/$current"}}`)
	}))
	defer server.Close()
	client := NewCryptoBotPayment("token")
	client.baseURL = server.URL
	invoice, err := client.CreateInvoice(context.Background(), 1, 1, "test")
	if err != nil {
		t.Fatal(err)
	}
	if invoice.PayURL != "https://t.me/$current" {
		t.Fatalf("PayURL = %q", invoice.PayURL)
	}
}

// Unit test: CreateInvoice returns error when CryptoBot API returns error
// Validates: Requirements 7.6
func TestCreateInvoice_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := `{"ok":false,"error":{"code":400,"name":"INVALID_AMOUNT"}}`
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(resp))
	}))
	defer srv.Close()

	cb := &CryptoBotPayment{
		token:   "test-api-token",
		baseURL: srv.URL,
		client:  &http.Client{},
	}

	_, err := cb.CreateInvoice(context.Background(), 1, 0.0, "Bad order")
	if err == nil {
		t.Fatal("expected error from CreateInvoice when API returns error, got nil")
	}

	want := "cryptobot: API error 400: INVALID_AMOUNT"
	if err.Error() != want {
		t.Errorf("expected error %q, got %q", want, err.Error())
	}
}

func TestCryptoBotConfigured(t *testing.T) {
	if NewCryptoBotPayment(" ").Configured() {
		t.Fatal("expected blank token to disable CryptoBot integration")
	}
	if !NewCryptoBotPayment("token").Configured() {
		t.Fatal("expected non-empty token to enable CryptoBot integration")
	}
}

func TestCreateInvoice_NotConfigured(t *testing.T) {
	cb := NewCryptoBotPayment("")

	_, err := cb.CreateInvoice(context.Background(), 1, 10, "Test")
	if !errors.Is(err, ErrCryptoBotNotConfigured) {
		t.Fatalf("expected ErrCryptoBotNotConfigured, got %v", err)
	}
}

func TestGetInvoices_NotConfigured(t *testing.T) {
	cb := NewCryptoBotPayment("")

	_, err := cb.GetInvoices(context.Background(), "paid")
	if !errors.Is(err, ErrCryptoBotNotConfigured) {
		t.Fatalf("expected ErrCryptoBotNotConfigured, got %v", err)
	}
}

func TestGetInvoicesPaginatesPaidWindow(t *testing.T) {
	var offsets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offsets = append(offsets, r.URL.Query().Get("offset"))
		if r.URL.Query().Get("count") != "100" || r.URL.Query().Get("status") != "paid" {
			t.Fatalf("query = %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("offset") == "0" {
			items := make([]map[string]any, 100)
			for i := range items {
				items[i] = map[string]any{"invoice_id": i + 1, "status": "paid", "payload": "7", "asset": "USDT", "amount": "1.00", "paid_at": "2026-08-27T10:00:00Z"}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"items": items}})
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"items":[]}}`)
	}))
	defer srv.Close()
	client := NewCryptoBotPayment("token")
	client.baseURL = srv.URL
	invoices, err := client.GetInvoices(context.Background(), "paid")
	if err != nil {
		t.Fatal(err)
	}
	if len(invoices) != 100 || !reflect.DeepEqual(offsets, []string{"0", "100"}) {
		t.Fatalf("invoices=%d offsets=%v", len(invoices), offsets)
	}
	wantPaidAt := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	if !invoices[0].OccurredAt.Equal(wantPaidAt) || invoices[0].PaidAt != "2026-08-27T10:00:00Z" {
		t.Fatalf("paid_at was not propagated: %+v", invoices[0])
	}
}

func TestGetInvoicesInvalidPaidAtCannotProduceReceipt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true,"result":{"items":[{"invoice_id":9,"status":"paid",`+
			`"payload":"7","asset":"USDT","amount":"1.00","paid_at":"not-a-time"}]}}`)
	}))
	defer server.Close()
	client := NewCryptoBotPayment("token")
	client.baseURL = server.URL
	invoices, err := client.GetInvoices(context.Background(), "paid")
	if err != nil || len(invoices) != 1 {
		t.Fatalf("invoices=%+v err=%v", invoices, err)
	}
	if !invoices[0].OccurredAt.IsZero() {
		t.Fatalf("invalid paid_at parsed as %v", invoices[0].OccurredAt)
	}
	if _, err := invoices[0].PaymentReceipt(); !errors.Is(err, ErrInvalidCryptoReceipt) {
		t.Fatalf("invalid paid_at receipt error = %v", err)
	}
	anomaly, err := invoices[0].PaymentAnomaly("polling_invalid_paid_invoice")
	if err != nil || anomaly.RawAmount != "1.00" || anomaly.ExternalID != "9" {
		t.Fatalf("anomaly=%+v err=%v", anomaly, err)
	}
}

func TestGetInvoicesPaginationUsesRawPageSize(t *testing.T) {
	var offsets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		offsets = append(offsets, offset)
		if offset == "0" {
			items := make([]map[string]any, 100)
			for i := range items {
				payload := "7"
				if i == 10 {
					payload = "malformed"
				}
				items[i] = map[string]any{"invoice_id": i + 1, "status": "paid", "payload": payload, "asset": "USDT", "amount": "1.00", "paid_at": "2026-08-27T10:00:00Z"}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"items": items}})
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"items":[]}}`)
	}))
	defer srv.Close()
	client := NewCryptoBotPayment("token")
	client.baseURL = srv.URL
	invoices, err := client.GetInvoices(context.Background(), "paid")
	if err != nil {
		t.Fatal(err)
	}
	if len(invoices) != 100 || invoices[10].OrderID != 0 || invoices[10].Payload != "malformed" ||
		!reflect.DeepEqual(offsets, []string{"0", "100"}) {
		t.Fatalf("invoices=%d offsets=%v", len(invoices), offsets)
	}
}

func TestPendingInvoicePaymentReceiptUsesStrictFixedDecimal(t *testing.T) {
	tests := []struct {
		amount string
		want   int64
		valid  bool
	}{
		{amount: "1", want: 100, valid: true},
		{amount: "1.2", want: 120, valid: true},
		{amount: "0.01", want: 1, valid: true},
		{amount: "0001.20", want: 120, valid: true},
		{amount: "0.009", valid: false}, // never round-accept an underpayment
		{amount: "1.001", valid: false},
		{amount: "1e2", valid: false},
		{amount: "+1.00", valid: false},
		{amount: "-1.00", valid: false},
		{amount: ".50", valid: false},
		{amount: "1.", valid: false},
		{amount: " 1.00", valid: false},
		{amount: "0.00", valid: false},
		{amount: "92233720368547758.08", valid: false},
	}
	for _, tc := range tests {
		t.Run(tc.amount, func(t *testing.T) {
			receipt, err := (PendingInvoice{
				InvoiceID: "42", OrderID: 7, Asset: "USDT", Amount: tc.amount, OccurredAt: time.Unix(1700000000, 0),
			}).PaymentReceipt()
			if tc.valid {
				if err != nil || receipt.AmountMinor != tc.want || receipt.Scale != 2 {
					t.Fatalf("receipt=%+v err=%v", receipt, err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidCryptoReceipt) {
				t.Fatalf("expected ErrInvalidCryptoReceipt, receipt=%+v err=%v", receipt, err)
			}
		})
	}
}

func TestPendingInvoicePaymentReceiptRejectsInvalidIdentity(t *testing.T) {
	for _, inv := range []PendingInvoice{
		{InvoiceID: "", OrderID: 7, Asset: "USDT", Amount: "1.00", OccurredAt: time.Unix(1700000000, 0)},
		{InvoiceID: "0", OrderID: 7, Asset: "USDT", Amount: "1.00", OccurredAt: time.Unix(1700000000, 0)},
		{InvoiceID: "+1", OrderID: 7, Asset: "USDT", Amount: "1.00", OccurredAt: time.Unix(1700000000, 0)},
		{InvoiceID: "not-numeric", OrderID: 7, Asset: "USDT", Amount: "1.00", OccurredAt: time.Unix(1700000000, 0)},
		{InvoiceID: "42", OrderID: 0, Asset: "USDT", Amount: "1.00", OccurredAt: time.Unix(1700000000, 0)},
	} {
		if _, err := inv.PaymentReceipt(); !errors.Is(err, ErrInvalidCryptoReceipt) {
			t.Fatalf("invoice=%+v err=%v", inv, err)
		}
	}
}

func TestPendingInvoicePaymentReceiptRequiresPaidAt(t *testing.T) {
	invoice := PendingInvoice{InvoiceID: "42", Status: "paid", OrderID: 7, Asset: "USDT", Amount: "1.00"}
	if _, err := invoice.PaymentReceipt(); !errors.Is(err, ErrInvalidCryptoReceipt) {
		t.Fatalf("missing paid_at receipt error = %v", err)
	}
	invoice.OccurredAt = time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	receipt, err := invoice.PaymentReceipt()
	if err != nil || !receipt.OccurredAt.Equal(invoice.OccurredAt) {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
}

func TestPendingInvoicePaymentAnomalyPreservesRawSignedFacts(t *testing.T) {
	inv := PendingInvoice{
		InvoiceID: "42", Status: "paid", Payload: "not-an-order", OrderID: 0,
		Asset: "USDT", Amount: "1.001e2",
	}
	anomaly, err := inv.PaymentAnomaly("polling_invalid_paid_invoice")
	if err != nil {
		t.Fatal(err)
	}
	if anomaly.AmountMinor != 1001 || anomaly.Scale != 1 || anomaly.ProposedOrderID != 0 {
		t.Fatalf("anomaly=%+v", anomaly)
	}
	if anomaly.Reason != "polling_invalid_paid_invoice" ||
		anomaly.RawPayload != "invoice_payload:not-an-order" || anomaly.RawAmount != "1.001e2" {
		t.Fatalf("anomaly did not preserve raw facts: %+v", anomaly)
	}
}

func TestNormalizeAnomalyAmountOnlyAcceptsExactlyRepresentableValues(t *testing.T) {
	tests := []struct {
		raw   string
		units int64
		scale int
		valid bool
	}{
		{raw: "1.001e2", units: 1001, scale: 1, valid: true},
		{raw: "1e-2", units: 1, scale: 2, valid: true},
		{raw: "1.0000000000", units: 1, scale: 0, valid: true},
		{raw: "1e-10", valid: false},
		{raw: "0.0000000001", valid: false},
		{raw: "not-a-number", valid: false},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			units, scale, err := normalizeAnomalyAmount(tc.raw)
			if tc.valid {
				if err != nil || units != tc.units || scale != tc.scale {
					t.Fatalf("units=%d scale=%d err=%v", units, scale, err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidCryptoReceipt) {
				t.Fatalf("units=%d scale=%d err=%v", units, scale, err)
			}
		})
	}
}

func TestGetInvoicesBoundedWindowSignalsContinuation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		overflow bool
	}{
		{name: "exactly_limit"},
		{name: "over_limit", overflow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var offsets []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				offset := r.URL.Query().Get("offset")
				offsets = append(offsets, offset)
				if offset == "1000" {
					items := []map[string]any{}
					if tc.overflow {
						items = append(items, map[string]any{"invoice_id": 1001, "status": "paid", "payload": "7", "asset": "USDT", "amount": "1.00", "paid_at": "2026-08-27T10:00:00Z"})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"items": items}})
					return
				}
				items := make([]map[string]any, 100)
				for i := range items {
					items[i] = map[string]any{"invoice_id": i + 1, "status": "paid", "payload": "7", "asset": "USDT", "amount": "1.00", "paid_at": "2026-08-27T10:00:00Z"}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"items": items}})
			}))
			defer srv.Close()
			client := NewCryptoBotPayment("token")
			client.baseURL = srv.URL
			invoices, next, err := client.GetInvoicesWindow(context.Background(), "paid", 0)
			if len(invoices) != 1000 || len(offsets) != 11 || offsets[10] != "1000" {
				t.Fatalf("invoices=%d offsets=%v", len(invoices), offsets)
			}
			if tc.overflow && !errors.Is(err, ErrCryptoInvoiceWindow) {
				t.Fatalf("expected bounded-window error, got %v", err)
			}
			if tc.overflow && next != 1000 {
				t.Fatalf("continuation=%d, want 1000", next)
			}
			if !tc.overflow && err != nil {
				t.Fatalf("exact limit returned error: %v", err)
			}
			if !tc.overflow && next != 0 {
				t.Fatalf("exact limit continuation=%d, want 0", next)
			}
		})
	}
}

func TestGetInvoicesWindowContinuesFromOffset(t *testing.T) {
	var offsets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		offsets = append(offsets, offset)
		if offset == "1100" {
			_, _ = io.WriteString(w, `{"ok":true,"result":{"items":[]}}`)
			return
		}
		items := make([]map[string]any, 100)
		for i := range items {
			items[i] = map[string]any{"invoice_id": 1001 + i, "status": "paid", "payload": "7", "asset": "USDT", "amount": "1.00", "paid_at": "2026-08-27T10:00:00Z"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"items": items}})
	}))
	defer srv.Close()
	client := NewCryptoBotPayment("token")
	client.baseURL = srv.URL
	invoices, next, err := client.GetInvoicesWindow(context.Background(), "paid", 1000)
	if err != nil || next != 0 || len(invoices) != 100 || !reflect.DeepEqual(offsets, []string{"1000", "1100"}) {
		t.Fatalf("invoices=%d next=%d offsets=%v err=%v", len(invoices), next, offsets, err)
	}
}

func TestVerifyWebhook_NotConfigured(t *testing.T) {
	cb := NewCryptoBotPayment("")

	if cb.VerifyWebhook([]byte(`{"status":"paid"}`), "deadbeef") {
		t.Fatal("expected unconfigured CryptoBot integration to reject webhook verification")
	}
}

// Unit test: ParseWebhook with valid JSON
// Validates: Requirements 7.1
func TestParseWebhook_ValidJSON(t *testing.T) {
	cb := NewCryptoBotPayment("token")

	body := []byte(`{
		"update_type": "invoice_paid",
		"payload": {
			"invoice_id": 999,
			"status": "paid",
			"payload": "77",
			"paid_at": "2026-08-27T10:00:00Z"
		}
	}`)

	wp, err := cb.ParseWebhook(body)
	if err != nil {
		t.Fatalf("ParseWebhook returned error: %v", err)
	}
	if wp.InvoiceID != "999" {
		t.Errorf("expected InvoiceID %q, got %q", "999", wp.InvoiceID)
	}
	if wp.Status != "paid" {
		t.Errorf("expected Status %q, got %q", "paid", wp.Status)
	}
	if wp.OrderID != 77 {
		t.Errorf("expected OrderID 77, got %d", wp.OrderID)
	}
	if !wp.OccurredAt.Equal(time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("OccurredAt = %v", wp.OccurredAt)
	}
}

func TestParseWebhookPaidReceiptRequiresValidPaidAt(t *testing.T) {
	cb := NewCryptoBotPayment("token")
	for _, paidAt := range []string{"", "not-a-time", " 2026-08-27T10:00:00Z"} {
		body := []byte(`{"update_type":"invoice_paid","payload":{"invoice_id":9,"status":"paid",` +
			`"payload":"77","asset":"USDT","amount":"1.00","paid_at":"` + paidAt + `"}}`)
		payload, err := cb.ParseWebhook(body)
		if err != nil {
			t.Fatalf("paid_at=%q: %v", paidAt, err)
		}
		if payload.ReceiptComplete || !payload.OccurredAt.IsZero() {
			t.Fatalf("paid_at=%q payload=%+v", paidAt, payload)
		}
	}
}

func TestParseWebhookReceiptCompletenessUsesStrictAmount(t *testing.T) {
	cb := NewCryptoBotPayment("token")
	for _, tc := range []struct {
		amount   string
		complete bool
	}{
		{amount: "1.00", complete: true},
		{amount: "0.009", complete: false},
		{amount: "1e2", complete: false},
	} {
		body := []byte(`{"update_type":"invoice_paid","payload":{"invoice_id":9,"status":"paid","payload":"77","asset":"USDT","amount":"` + tc.amount + `","paid_at":"2026-08-27T10:00:00Z"}}`)
		payload, err := cb.ParseWebhook(body)
		if err != nil {
			t.Fatalf("amount=%q: %v", tc.amount, err)
		}
		if payload.ReceiptComplete != tc.complete {
			t.Fatalf("amount=%q complete=%v, want %v", tc.amount, payload.ReceiptComplete, tc.complete)
		}
		if tc.complete && payload.AmountMinor != 100 {
			t.Fatalf("amount=%q minor=%d, want 100", tc.amount, payload.AmountMinor)
		}
	}
}

// Unit test: ParseWebhook with invalid JSON
// Validates: Requirements 7.1
func TestParseWebhook_InvalidJSON(t *testing.T) {
	cb := NewCryptoBotPayment("token")

	_, err := cb.ParseWebhook([]byte(`not json at all`))
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

// Unit test: ParseWebhook with valid outer JSON but invalid payload
// Validates: Requirements 7.1
func TestParseWebhook_InvalidPayload(t *testing.T) {
	cb := NewCryptoBotPayment("token")

	body := []byte(`{"update_type":"invoice_paid","payload":"not an object"}`)
	_, err := cb.ParseWebhook(body)
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}
}

// A signed paid event with a malformed payload still reaches the durable
// anomaly path instead of being rejected before its raw provider facts can be
// recorded.
func TestParseWebhook_NonNumericOrderIDIsIncompleteReceipt(t *testing.T) {
	cb := NewCryptoBotPayment("token")

	body := []byte(`{
		"update_type": "invoice_paid",
		"payload": {
			"invoice_id": 1,
			"status": "paid",
			"payload": "not-a-number"
		}
	}`)

	payload, err := cb.ParseWebhook(body)
	if err != nil {
		t.Fatal(err)
	}
	if payload.ReceiptComplete || payload.OrderID != 0 || payload.Payload != "not-a-number" {
		t.Fatalf("payload=%+v", payload)
	}
}
