package bot

import (
	"testing"
	"time"

	"shop_bot/internal/shop"
	"shop_bot/internal/storage"
)

func TestCartHasSubscription(t *testing.T) {
	regular := shop.CartItemView{Product: storage.Product{ID: 1}, Quantity: 1}
	sub := shop.CartItemView{Product: storage.Product{ID: 2, SubPeriodDays: 30}, Quantity: 1}

	cases := []struct {
		name string
		view *shop.CartView
		want bool
	}{
		{"nil view", nil, false},
		{"empty cart", &shop.CartView{}, false},
		{"regular only", &shop.CartView{Items: []shop.CartItemView{regular}}, false},
		{"subscription present", &shop.CartView{Items: []shop.CartItemView{regular, sub}}, true},
	}
	for _, tc := range cases {
		if got := cartHasSubscription(tc.view); got != tc.want {
			t.Errorf("%s: cartHasSubscription = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestStashSubscriptionExpiry: the raw-update probe must expose
// subscription_expiration_date to takePendingSubExpiry exactly once, and the
// cleanup must drop unconsumed entries.
func TestStashSubscriptionExpiry(t *testing.T) {
	b := &Bot{}

	raw := []byte(`{"update_id":1,"message":{"message_id":2,"successful_payment":{
		"currency":"XTR","total_amount":100,"invoice_payload":"15",
		"telegram_payment_charge_id":"ch_1","is_recurring":true,
		"subscription_expiration_date":1767225600}}}`)

	cleanup := b.stashSubscriptionExpiry(raw)
	got, ok := b.takePendingSubExpiry("ch_1")
	if !ok {
		t.Fatal("expiry was not stashed for ch_1")
	}
	if want := time.Unix(1767225600, 0); !got.Equal(want) {
		t.Fatalf("expiry = %v, want %v", got, want)
	}
	// Consumed: a second take must miss.
	if _, ok := b.takePendingSubExpiry("ch_1"); ok {
		t.Fatal("expiry was not consumed by the first take")
	}
	cleanup()
}

func TestStashSubscriptionExpiry_IgnoresNonSubscriptionUpdates(t *testing.T) {
	b := &Bot{}

	for name, raw := range map[string]string{
		"no payment":     `{"update_id":1,"message":{"message_id":2,"text":"hi"}}`,
		"no expiration":  `{"message":{"successful_payment":{"telegram_payment_charge_id":"ch_2"}}}`,
		"malformed json": `{"message":`,
	} {
		cleanup := b.stashSubscriptionExpiry([]byte(raw))
		cleanup()
		if _, ok := b.takePendingSubExpiry("ch_2"); ok {
			t.Errorf("%s: unexpected stashed expiry", name)
		}
	}
}

func TestStashSubscriptionExpiry_CleanupDropsUnconsumedEntry(t *testing.T) {
	b := &Bot{}

	raw := []byte(`{"message":{"successful_payment":{
		"telegram_payment_charge_id":"ch_3","subscription_expiration_date":1767225600}}}`)

	cleanup := b.stashSubscriptionExpiry(raw)
	cleanup()
	if _, ok := b.takePendingSubExpiry("ch_3"); ok {
		t.Fatal("cleanup did not remove the unconsumed entry")
	}
}
