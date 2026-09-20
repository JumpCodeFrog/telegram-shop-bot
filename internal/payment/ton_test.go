package payment

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shop_bot/internal/storage"
)

const (
	tonTestWallet = "EQtestwalletaddress"
	tonTestAPIKey = "toncenter-test-key"
)

// tonTransactionsFixture mirrors the live toncenter v2 getTransactions
// response shape (controller-verified 2026-09-20): lt is a STRING (exceeds
// int32), value is a STRING of nanotons, and comments arrive either as
// msg.dataText (plain text) or msg.dataRaw (binary payload, skipped).
const tonTransactionsFixture = `{
  "ok": true,
  "result": [
    {
      "transaction_id": {"@type": "internal.transactionId", "lt": "104597075000005", "hash": "Um9ib2x5dGhhY2sx"},
      "utime": 1758294000,
      "in_msg": {
        "@type": "ext.message",
        "source": "EQA_sender",
        "destination": "EQtestwalletaddress",
        "value": "1500000000",
        "msg_data": {"@type": "msg.dataText", "text": "order-123"}
      }
    },
    {
      "transaction_id": {"@type": "internal.transactionId", "lt": "104597075000004", "hash": "UmF3aGFzaA=="},
      "utime": 1758293900,
      "in_msg": {
        "@type": "ext.message",
        "source": "EQB_sender",
        "destination": "EQtestwalletaddress",
        "value": "2000000000",
        "msg_data": {"@type": "msg.dataRaw", "body": "te6ccgEBAQEAAgAAAA=="}
      }
    },
    {
      "transaction_id": {"@type": "internal.transactionId", "lt": "104597075000003", "hash": "WmVyb2hhc2g="},
      "utime": 1758293800,
      "in_msg": {
        "@type": "ext.message",
        "source": "EQC_sender",
        "destination": "EQtestwalletaddress",
        "value": "0",
        "msg_data": {"@type": "msg.dataText", "text": "order-124"}
      }
    }
  ],
  "@extra": "1758294001.123:0:0.1"
}`

// newTONTestClient points a fully configured adapter at the test server,
// mirroring the production base URL https://toncenter.com.
func newTONTestClient(srv *httptest.Server) *TONPayment {
	client := NewTONPayment(tonTestWallet, tonTestAPIKey)
	client.SetBaseURL(srv.URL)
	return client
}

func TestTONConfigured(t *testing.T) {
	if NewTONPayment("", tonTestAPIKey).Configured() {
		t.Error("empty wallet address must not be configured")
	}
	if !NewTONPayment(tonTestWallet, "").Configured() {
		// The toncenter API key is optional (rate limits only); the wallet
		// address alone makes the integration usable.
		t.Error("wallet address without API key must be configured")
	}
}

func TestTONGetTransactionsParsesTextCommentsAndSkipsBinary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/api/v2/getTransactions" {
			t.Errorf("expected path /api/v2/getTransactions, got %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("address") != tonTestWallet {
			t.Errorf("address = %q, want %q", q.Get("address"), tonTestWallet)
		}
		if q.Get("limit") != "7" {
			t.Errorf("limit = %q, want 7", q.Get("limit"))
		}
		if q.Get("api_key") != tonTestAPIKey {
			t.Errorf("api_key = %q, want %q", q.Get("api_key"), tonTestAPIKey)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(tonTransactionsFixture))
	}))
	defer srv.Close()

	txs, err := newTONTestClient(srv).GetTransactions(context.Background(), 7)
	if err != nil {
		t.Fatalf("GetTransactions: %v", err)
	}
	// The dataRaw transaction must be skipped; the zero-value text
	// transaction stays (parsing is not validation — PaymentReceipt
	// rejects non-positive values).
	if len(txs) != 2 {
		t.Fatalf("expected 2 transactions (dataRaw skipped), got %d: %+v", len(txs), txs)
	}

	first := txs[0]
	if first.LT != "104597075000005" || first.Hash != "Um9ib2x5dGhhY2sx" {
		t.Errorf("first tx id = %q:%q", first.LT, first.Hash)
	}
	if first.Source != "EQA_sender" {
		t.Errorf("first tx source = %q", first.Source)
	}
	if first.ValueNano != 1500000000 {
		t.Errorf("first tx value = %d, want 1500000000", first.ValueNano)
	}
	if first.Comment != "order-123" {
		t.Errorf("first tx comment = %q, want order-123", first.Comment)
	}
	if first.Utime != 1758294000 {
		t.Errorf("first tx utime = %d, want 1758294000", first.Utime)
	}

	second := txs[1]
	if second.LT != "104597075000003" || second.ValueNano != 0 || second.Comment != "order-124" {
		t.Errorf("second tx = %+v, want zero-value order-124", second)
	}
}

func TestTONGetTransactionsOmitsAPIKeyWhenEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if raw := r.URL.RawQuery; strings.Contains(raw, "api_key") {
			t.Errorf("api_key must be omitted when empty, got query %q", raw)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer srv.Close()

	client := NewTONPayment(tonTestWallet, "")
	client.SetBaseURL(srv.URL)
	if _, err := client.GetTransactions(context.Background(), 10); err != nil {
		t.Fatalf("GetTransactions: %v", err)
	}
}

func TestTONGetTransactionsMapsAPIErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{name: "ok false with HTTP 200", status: http.StatusOK,
			body: `{"ok":false,"error":"invalid address","code":400}`, wantErr: "invalid address"},
		{name: "ok false with HTTP 500", status: http.StatusInternalServerError,
			body: `{"ok":false,"error":"LITE_SERVER_UNKNOWN","code":500}`, wantErr: "LITE_SERVER_UNKNOWN"},
		{name: "plain HTTP error", status: http.StatusBadGateway,
			body: `bad gateway`, wantErr: "502"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			_, err := newTONTestClient(srv).GetTransactions(context.Background(), 10)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q must contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestTONGetTransactionsRequiresConfiguration(t *testing.T) {
	if _, err := NewTONPayment("", tonTestAPIKey).GetTransactions(context.Background(), 10); !errors.Is(err, ErrTONNotConfigured) {
		t.Fatalf("GetTransactions unconfigured = %v, want ErrTONNotConfigured", err)
	}
}

// TestParseOrderComment pins the exact-comment contract: wallets prefill the
// comment via the TransferLink deeplink, and manual payers must copy it
// exactly (the instructions message shows it in a code block) — no fuzzy
// matching, no case folding, no whitespace tolerance.
func TestParseOrderComment(t *testing.T) {
	tests := []struct {
		comment string
		wantID  int64
		wantOK  bool
	}{
		{comment: "order-1", wantID: 1, wantOK: true},
		{comment: "order-123", wantID: 123, wantOK: true},
		// math.MaxInt64 fits int64, so it parses; the ledger simply finds
		// no such order and quarantines the payment. Safe.
		{comment: "order-9223372036854775807", wantID: math.MaxInt64, wantOK: true},
		{comment: "order-9223372036854775808", wantOK: false}, // int64 overflow
		{comment: "order-0", wantOK: false},                   // order ids are positive
		{comment: "Order-1", wantOK: false},                   // case-sensitive
		{comment: "order- 1", wantOK: false},
		{comment: "order-1 ", wantOK: false},
		{comment: " order-1", wantOK: false},
		{comment: "order-", wantOK: false},
		{comment: "order-abc", wantOK: false},
		{comment: "", wantOK: false},
		{comment: "1", wantOK: false},
	}
	for _, tt := range tests {
		gotID, gotOK := ParseOrderComment(tt.comment)
		if gotID != tt.wantID || gotOK != tt.wantOK {
			t.Errorf("ParseOrderComment(%q) = (%d, %v), want (%d, %v)",
				tt.comment, gotID, gotOK, tt.wantID, tt.wantOK)
		}
	}
}

func TestTONTransferLink(t *testing.T) {
	client := NewTONPayment(tonTestWallet, tonTestAPIKey)
	got := client.TransferLink(2000000000, 123)
	want := "ton://transfer/" + tonTestWallet + "?amount=2000000000&text=order-123"
	if got != want {
		t.Errorf("TransferLink = %q, want %q", got, want)
	}
}

// TestTONTransactionPaymentReceipt pins the TON receipt contract: the order
// id comes from the exact transfer comment, amounts are integer nanotons
// (scale 9), and the external id pairs lt with the transaction hash.
func TestTONTransactionPaymentReceipt(t *testing.T) {
	happy := TONTransaction{
		LT: "104597075000005", Hash: "Um9ib2x5dGhhY2sx", Source: "EQA_sender",
		ValueNano: 1500000000, Comment: "order-123", Utime: 1758294000,
	}
	receipt, err := happy.PaymentReceipt()
	if err != nil {
		t.Fatalf("PaymentReceipt: %v", err)
	}
	if receipt.OrderID != 123 || receipt.Provider != storage.PaymentMethodTON {
		t.Errorf("receipt order/provider = %d/%q", receipt.OrderID, receipt.Provider)
	}
	if receipt.ExternalID != "104597075000005:Um9ib2x5dGhhY2sx" {
		t.Errorf("receipt ExternalID = %q", receipt.ExternalID)
	}
	if receipt.PayerID != 0 || receipt.Currency != "TON" || receipt.AmountMinor != 1500000000 || receipt.Scale != 9 {
		t.Errorf("receipt = %+v", receipt)
	}
	if want := time.Unix(1758294000, 0).UTC(); !receipt.OccurredAt.Equal(want) {
		t.Errorf("receipt OccurredAt = %v, want %v", receipt.OccurredAt, want)
	}

	invalid := []TONTransaction{
		{LT: "1", Hash: "h", ValueNano: 1, Comment: "hello", Utime: 1758294000},   // no order comment
		{LT: "1", Hash: "h", ValueNano: 1, Comment: "", Utime: 1758294000},        // empty comment
		{LT: "1", Hash: "h", ValueNano: 1, Comment: "Order-1", Utime: 1758294000}, // wrong case
		{LT: "1", Hash: "h", ValueNano: 0, Comment: "order-1", Utime: 1758294000}, // zero value
		{LT: "1", Hash: "h", ValueNano: -5, Comment: "order-1", Utime: 1758294000},
		{LT: "", Hash: "h", ValueNano: 1, Comment: "order-1", Utime: 1758294000},
		{LT: "1", Hash: "", ValueNano: 1, Comment: "order-1", Utime: 1758294000},
	}
	for i, tx := range invalid {
		if _, err := tx.PaymentReceipt(); !errors.Is(err, ErrInvalidTONReceipt) {
			t.Errorf("invalid[%d]: PaymentReceipt = %v, want ErrInvalidTONReceipt", i, err)
		}
	}
}
