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

func TestOrderSubscriptionProductUsesImmutableOrderSnapshot(t *testing.T) {
	b := &Bot{}
	regular := &storage.Order{ID: 1, Items: []storage.OrderItem{{ProductID: 9, Quantity: 1}}}
	productID, days, err := b.orderSubscriptionProduct(t.Context(), regular)
	if err != nil || productID != 0 || days != 0 {
		t.Fatalf("regular snapshot reclassified: product=%d days=%d err=%v", productID, days, err)
	}
	recurring := &storage.Order{ID: 2, SubscriptionProductID: 9, SubscriptionPeriodDays: 30}
	productID, days, err = b.orderSubscriptionProduct(t.Context(), recurring)
	if err != nil || productID != 9 || days != 30 {
		t.Fatalf("recurring snapshot lost: product=%d days=%d err=%v", productID, days, err)
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
	want := time.Unix(1767225600, 0)
	got, ok := b.takePendingSubExpiry("ch_1")
	if !ok {
		t.Fatal("expiry was not stashed for ch_1")
	}
	if !got.Equal(want) {
		t.Fatalf("expiry = %v, want %v", got, want)
	}
	// Duplicate delivery observes the same provider expiry until cleanup.
	if duplicate, ok := b.takePendingSubExpiry("ch_1"); !ok || !duplicate.Equal(want) {
		t.Fatalf("duplicate expiry = %v, ok=%t", duplicate, ok)
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

func TestDecodeTelegramUpdatePreservesRenewalWithoutExpiry(t *testing.T) {
	b := &Bot{}
	raw := []byte(`{"update_id":9,"message":{"message_id":1,"successful_payment":{"currency":"XTR","total_amount":25,"invoice_payload":"7","telegram_payment_charge_id":"renewal-1","provider_payment_charge_id":"","is_recurring":true,"is_first_recurring":false}}}`)
	update, cleanup, err := b.decodeTelegramUpdate(raw)
	if err != nil {
		t.Fatal(err)
	}
	if update.UpdateID != 9 || update.Message == nil || update.Message.SuccessfulPayment == nil {
		t.Fatalf("update = %+v", update)
	}
	if !b.isPendingSubscriptionRenewal("renewal-1") {
		t.Fatal("renewal flag was not preserved")
	}
	if _, ok := b.takePendingSubExpiry("renewal-1"); ok {
		t.Fatal("missing expiry unexpectedly stashed")
	}
	cleanup()
	if b.isPendingSubscriptionRenewal("renewal-1") {
		t.Fatal("cleanup kept renewal flag")
	}
}

func TestDuplicateRenewalCleanupIsReferenceCounted(t *testing.T) {
	b := &Bot{}
	raw := []byte(`{"message":{"successful_payment":{"telegram_payment_charge_id":"same","is_recurring":true,"is_first_recurring":false}}}`)
	first := b.stashSubscriptionExpiry(raw)
	second := b.stashSubscriptionExpiry(raw)
	first()
	if !b.isPendingSubscriptionRenewal("same") {
		t.Fatal("first cleanup deleted concurrent duplicate metadata")
	}
	second()
	if b.isPendingSubscriptionRenewal("same") {
		t.Fatal("last cleanup kept metadata")
	}
}
