package payment

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/storage"
)

func TestBuildDescription_Empty(t *testing.T) {
	desc := buildDescription(nil)
	if desc != "Оплата заказа" {
		t.Fatalf("expected fallback description, got %q", desc)
	}
}

func TestBuildDescription_SingleItem(t *testing.T) {
	items := []storage.OrderItem{
		{ProductID: 5, ProductName: "Футболка", Quantity: 2},
	}
	desc := buildDescription(items)
	want := "Футболка × 2"
	if desc != want {
		t.Fatalf("got %q, want %q", desc, want)
	}
}

func TestBuildDescription_MultipleItems(t *testing.T) {
	items := []storage.OrderItem{
		{ProductID: 1, ProductName: "Футболка", Quantity: 3},
		{ProductID: 7, ProductName: "Кепка", Quantity: 1},
	}
	desc := buildDescription(items)
	want := "Футболка × 3, ещё 1 товар"
	if desc != want {
		t.Fatalf("got %q, want %q", desc, want)
	}
}

func TestBuildDescription_FallsBackToProductID(t *testing.T) {
	items := []storage.OrderItem{
		{ProductID: 7, Quantity: 1},
	}
	desc := buildDescription(items)
	want := "Товар #7 × 1"
	if desc != want {
		t.Fatalf("got %q, want %q", desc, want)
	}
}

func TestInvoiceStartParameter(t *testing.T) {
	if got := invoiceStartParameter(42); got != "order-42" {
		t.Fatalf("invoiceStartParameter = %q", got)
	}
}

// mockOrderGetter serves a single order (or an error) for pre-checkout tests.
type mockOrderGetter struct {
	order *storage.Order
	err   error
}

type guardedOrderGetter struct {
	mockOrderGetter
	conflict bool
	err      error
}

func (g guardedOrderGetter) HasSubscriptionEntitlementConflict(context.Context, int64, int64) (bool, error) {
	return g.conflict, g.err
}

func (m mockOrderGetter) GetOrder(_ context.Context, _ int64) (*storage.Order, error) {
	return m.order, m.err
}

// answerCapture records the last answerPreCheckoutQuery request the fake
// Telegram API received.
type answerCapture struct {
	values url.Values
}

// newTestStarsPayment builds a StarsPayment against an httptest Telegram API
// that captures answerPreCheckoutQuery calls. The translator echoes the key,
// so assertions can match rejection reasons directly.
func newTestStarsPayment(t *testing.T, orders OrderGetter) (*StarsPayment, *answerCapture) {
	t.Helper()
	capture := &answerCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if strings.HasSuffix(r.URL.Path, "answerPreCheckoutQuery") {
			capture.values = r.Form
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	t.Cleanup(srv.Close)

	api := &tgbotapi.BotAPI{Token: "test-token", Client: srv.Client(), Buffer: 100}
	api.SetAPIEndpoint(srv.URL + "/bot%s/%s")
	return NewStarsPayment(api, orders, nil), capture
}

func preCheckoutQuery(payload string, fromID int64, totalAmount int) *tgbotapi.PreCheckoutQuery {
	return &tgbotapi.PreCheckoutQuery{
		ID:             "pcq-1",
		From:           &tgbotapi.User{ID: fromID, LanguageCode: "en"},
		Currency:       "XTR",
		TotalAmount:    totalAmount,
		InvoicePayload: payload,
	}
}

func assertPreCheckoutAnswer(t *testing.T, capture *answerCapture, wantOK bool, wantErrKey string) {
	t.Helper()
	if capture.values == nil {
		t.Fatal("answerPreCheckoutQuery was not called")
	}
	// tgbotapi only sends "ok" when it is true.
	gotOK := capture.values.Get("ok") == "true"
	if gotOK != wantOK {
		t.Fatalf("answered ok=%v, want %v (values: %v)", gotOK, wantOK, capture.values)
	}
	if got := capture.values.Get("error_message"); got != wantErrKey {
		t.Fatalf("error_message = %q, want %q", got, wantErrKey)
	}
}

func pendingOrder() *storage.Order {
	return &storage.Order{ID: 7, UserID: 42, Status: storage.OrderStatusPending, TotalStars: 100}
}

func TestHandlePreCheckout_HappyPath(t *testing.T) {
	stars, capture := newTestStarsPayment(t, mockOrderGetter{order: pendingOrder()})

	if err := stars.HandlePreCheckout(context.Background(), preCheckoutQuery("7", 42, 100)); err != nil {
		t.Fatalf("HandlePreCheckout: %v", err)
	}
	assertPreCheckoutAnswer(t, capture, true, "")
}

func TestHandlePreCheckoutRejectsSubscriptionEntitlementConflict(t *testing.T) {
	order := pendingOrder()
	order.SubscriptionProductID = 9
	order.SubscriptionPeriodDays = 30
	stars, capture := newTestStarsPayment(t, guardedOrderGetter{
		mockOrderGetter: mockOrderGetter{order: order}, conflict: true,
	})
	if err := stars.HandlePreCheckout(context.Background(), preCheckoutQuery("7", 42, 100)); err != nil {
		t.Fatal(err)
	}
	assertPreCheckoutAnswer(t, capture, false, PreCheckoutKeyNotPending)
}

func TestHandlePreCheckout_RejectsUnknownOrder(t *testing.T) {
	stars, capture := newTestStarsPayment(t, mockOrderGetter{err: storage.ErrNotFound})

	if err := stars.HandlePreCheckout(context.Background(), preCheckoutQuery("7", 42, 100)); err != nil {
		t.Fatalf("HandlePreCheckout: %v", err)
	}
	assertPreCheckoutAnswer(t, capture, false, PreCheckoutKeyOrderNotFound)
}

func TestHandlePreCheckout_RejectsForeignOrder(t *testing.T) {
	stars, capture := newTestStarsPayment(t, mockOrderGetter{order: pendingOrder()})

	if err := stars.HandlePreCheckout(context.Background(), preCheckoutQuery("7", 99, 100)); err != nil {
		t.Fatalf("HandlePreCheckout: %v", err)
	}
	assertPreCheckoutAnswer(t, capture, false, PreCheckoutKeyWrongUser)
}

func TestHandlePreCheckout_RejectsNonPendingOrder(t *testing.T) {
	order := pendingOrder()
	order.Status = storage.OrderStatusPaid
	stars, capture := newTestStarsPayment(t, mockOrderGetter{order: order})

	if err := stars.HandlePreCheckout(context.Background(), preCheckoutQuery("7", 42, 100)); err != nil {
		t.Fatalf("HandlePreCheckout: %v", err)
	}
	assertPreCheckoutAnswer(t, capture, false, PreCheckoutKeyNotPending)
}

func TestHandlePreCheckout_RejectsNeedsReviewOrder(t *testing.T) {
	order := pendingOrder()
	order.PaymentState = storage.PaymentStateNeedsReview
	stars, capture := newTestStarsPayment(t, mockOrderGetter{order: order})

	if err := stars.HandlePreCheckout(context.Background(), preCheckoutQuery("7", 42, 100)); err != nil {
		t.Fatalf("HandlePreCheckout: %v", err)
	}
	assertPreCheckoutAnswer(t, capture, false, PreCheckoutKeyNotPending)
}

func TestHandlePreCheckout_RejectsAmountMismatch(t *testing.T) {
	stars, capture := newTestStarsPayment(t, mockOrderGetter{order: pendingOrder()})

	if err := stars.HandlePreCheckout(context.Background(), preCheckoutQuery("7", 42, 55)); err != nil {
		t.Fatalf("HandlePreCheckout: %v", err)
	}
	assertPreCheckoutAnswer(t, capture, false, PreCheckoutKeyAmountMismatch)
}

func TestHandlePreCheckout_RejectsMalformedPayload(t *testing.T) {
	stars, capture := newTestStarsPayment(t, mockOrderGetter{order: pendingOrder()})

	if err := stars.HandlePreCheckout(context.Background(), preCheckoutQuery("not-a-number", 42, 100)); err != nil {
		t.Fatalf("HandlePreCheckout: %v", err)
	}
	assertPreCheckoutAnswer(t, capture, false, PreCheckoutKeyOrderNotFound)
}
