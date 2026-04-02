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
	"testing"

	"pgregory.net/rapid"
)

// Feature: shop_bot, Property 11: Round-trip подписи webhook CryptoBot
// Validates: Requirements 7.3, 7.7
func TestCryptoBotWebhookSignatureRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Use StringMatching to ensure token is non-empty after trimming (not all whitespace)
		token := rapid.StringMatching(`\S.*`).Draw(t, "token")
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
			"payload": "77"
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

// Unit test: ParseWebhook with non-numeric order ID in payload
// Validates: Requirements 7.1
func TestParseWebhook_NonNumericOrderID(t *testing.T) {
	cb := NewCryptoBotPayment("token")

	body := []byte(`{
		"update_type": "invoice_paid",
		"payload": {
			"invoice_id": 1,
			"status": "paid",
			"payload": "not-a-number"
		}
	}`)

	_, err := cb.ParseWebhook(body)
	if err == nil {
		t.Fatal("expected error for non-numeric order ID, got nil")
	}
}
