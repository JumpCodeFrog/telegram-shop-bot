package payment

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/storage"
)

// newInvoiceCaptureStars builds a StarsPayment against an httptest Telegram
// API that records the form values of every sendInvoice call.
func newInvoiceCaptureStars(t *testing.T) (*StarsPayment, *answerCapture) {
	t.Helper()
	capture := &answerCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if strings.HasSuffix(r.URL.Path, "sendInvoice") {
			capture.values = r.Form
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":1},"date":1}}`))
	}))
	t.Cleanup(srv.Close)

	api := &tgbotapi.BotAPI{Token: "test-token", Client: srv.Client(), Buffer: 100}
	api.SetAPIEndpoint(srv.URL + "/bot%s/%s")
	return NewStarsPayment(api, nil, nil), capture
}

func invoiceItems() []storage.OrderItem {
	return []storage.OrderItem{{ProductID: 3, ProductName: "VPN Access", Quantity: 1}}
}

func requireInvoiceCall(t *testing.T, capture *answerCapture) url.Values {
	t.Helper()
	if capture.values == nil {
		t.Fatal("sendInvoice was not called")
	}
	return capture.values
}

// TestSendInvoice_SubscriptionPeriod: a subscription product's invoice must
// carry subscription_period=2592000 (30 days) alongside the XTR currency and
// the order-ID payload.
func TestSendInvoice_SubscriptionPeriod(t *testing.T) {
	stars, capture := newInvoiceCaptureStars(t)

	if err := stars.SendInvoice(42, 15, 100, invoiceItems(), SubscriptionPeriodSeconds(30)); err != nil {
		t.Fatalf("SendInvoice: %v", err)
	}

	form := requireInvoiceCall(t, capture)
	if got := form.Get("subscription_period"); got != "2592000" {
		t.Fatalf("subscription_period = %q, want %q", got, "2592000")
	}
	if got := form.Get("currency"); got != "XTR" {
		t.Fatalf("currency = %q, want XTR", got)
	}
	if got := form.Get("payload"); got != "15" {
		t.Fatalf("payload = %q, want order ID 15", got)
	}
	if prices := form.Get("prices"); !strings.Contains(prices, `"amount":100`) {
		t.Fatalf("prices %q does not contain the Stars total 100", prices)
	}
}

// TestSendInvoice_RegularHasNoSubscriptionPeriod: a regular order keeps the
// plain one-off invoice without any subscription_period field.
func TestSendInvoice_RegularHasNoSubscriptionPeriod(t *testing.T) {
	stars, capture := newInvoiceCaptureStars(t)

	if err := stars.SendInvoice(42, 15, 100, invoiceItems(), 0); err != nil {
		t.Fatalf("SendInvoice: %v", err)
	}

	form := requireInvoiceCall(t, capture)
	if got := form.Get("subscription_period"); got != "" {
		t.Fatalf("regular invoice unexpectedly has subscription_period=%q", got)
	}
	if got := form.Get("currency"); got != "XTR" {
		t.Fatalf("currency = %q, want XTR", got)
	}
}

func TestSubscriptionPeriodSeconds(t *testing.T) {
	if got := SubscriptionPeriodSeconds(30); got != 2592000 {
		t.Fatalf("SubscriptionPeriodSeconds(30) = %d, want 2592000", got)
	}
	if got := SubscriptionPeriodSeconds(0); got != 0 {
		t.Fatalf("SubscriptionPeriodSeconds(0) = %d, want 0", got)
	}
}
