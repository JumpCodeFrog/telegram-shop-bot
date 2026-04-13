package bot

import (
	"strings"
	"testing"

	"shop_bot/internal/service"
	"shop_bot/internal/shop"
	"shop_bot/internal/storage"
)

func newTextBot(t *testing.T) *Bot {
	t.Helper()

	i18n, err := service.NewI18nService("../../locales")
	if err != nil {
		t.Fatalf("load locales: %v", err)
	}

	return &Bot{i18n: i18n}
}

func TestOrderStatusText_Localized(t *testing.T) {
	t.Parallel()

	b := newTextBot(t)

	if got := b.orderStatusText("en", storage.OrderStatusPaid); got != "✅ Paid" {
		t.Fatalf("english paid status = %q", got)
	}
	if got := b.orderStatusText("ru", storage.OrderStatusPending); got != "⏳ Ожидает оплаты" {
		t.Fatalf("russian pending status = %q", got)
	}
}

func TestFormatProductText_EscapesHTML(t *testing.T) {
	t.Parallel()

	b := newTextBot(t)
	product := &storage.Product{
		Name:        "<Spring Tee>",
		Description: "soft & bright",
		PriceUSD:    12.99,
		PriceStars:  650,
		Stock:       5,
	}

	got := b.formatProductText("en", product)

	if !strings.Contains(got, "&lt;Spring Tee&gt;") {
		t.Fatalf("escaped product name not found: %q", got)
	}
	if !strings.Contains(got, "soft &amp; bright") {
		t.Fatalf("escaped description not found: %q", got)
	}
	if !strings.Contains(got, "$12.99") || !strings.Contains(got, "650 ⭐") {
		t.Fatalf("price line missing: %q", got)
	}
}

func TestFormatCheckoutText_UsesLocalizedLabels(t *testing.T) {
	t.Parallel()

	b := newTextBot(t)
	view := &shop.CartView{
		Items: []shop.CartItemView{
			{
				Product: storage.Product{
					Name:       "Basic Tee",
					PriceUSD:   12.99,
					PriceStars: 650,
				},
				Quantity: 2,
			},
		},
		TotalUSD:   25.98,
		TotalStars: 1300,
	}

	got := b.formatCheckoutText("en", view)

	if !strings.Contains(got, "<b>Checkout</b>") {
		t.Fatalf("checkout title missing: %q", got)
	}
	if !strings.Contains(got, "🛒 Items:") {
		t.Fatalf("checkout items header missing: %q", got)
	}
	if !strings.Contains(got, "To pay: $25.98 / 1300 ⭐") {
		t.Fatalf("checkout total missing: %q", got)
	}
}

func TestFormatPaymentMethodsText_UsesOrderSummary(t *testing.T) {
	t.Parallel()

	b := newTextBot(t)
	view := &shop.CartView{
		Items: []shop.CartItemView{
			{
				Product: storage.Product{
					Name:       "Basic Tee",
					PriceUSD:   12.99,
					PriceStars: 650,
				},
				Quantity: 2,
			},
		},
		TotalUSD:   25.98,
		TotalStars: 1300,
	}

	got := b.formatPaymentMethodsText("en", 42, view, false)

	if !strings.Contains(got, "Checkout for order <code>#42</code>") {
		t.Fatalf("payment methods title missing: %q", got)
	}
	if !strings.Contains(got, "Choose a payment method:") {
		t.Fatalf("payment methods hint missing: %q", got)
	}
	if !strings.Contains(got, "only Telegram Stars payment is available") {
		t.Fatalf("no-crypto hint missing: %q", got)
	}
}

func TestFormatCategoryProductsText_ShowsPreviewAndWishlistMark(t *testing.T) {
	t.Parallel()

	b := newTextBot(t)
	category := &storage.Category{Name: "Clothing", Emoji: "👕"}
	products := []storage.Product{
		{
			ID:          1,
			Name:        "Basic Tee",
			Description: "Very soft cotton shirt with a breathable fabric and relaxed fit for daily wear.",
			PriceUSD:    12.99,
			PriceStars:  650,
			Stock:       5,
		},
	}

	got := b.formatCategoryProductsText("en", category, products, 0, 1, map[int64]struct{}{1: {}})

	if !strings.Contains(got, "<b>👕 Clothing</b>") {
		t.Fatalf("category title missing: %q", got)
	}
	if !strings.Contains(got, "1. <b>❤️ Basic Tee</b>") {
		t.Fatalf("wishlist mark missing: %q", got)
	}
	if !strings.Contains(got, "In stock: 5 pcs.") {
		t.Fatalf("stock line missing: %q", got)
	}
}

func TestProductKeyboard_UsesQuantityStepper(t *testing.T) {
	t.Parallel()

	b := newTextBot(t)
	kb := b.productKeyboard(&storage.Product{ID: 7, CategoryID: 3}, true, 2, "ru")

	if len(kb) != 3 {
		t.Fatalf("expected 3 keyboard rows, got %d", len(kb))
	}
	if kb[0][1].Text != "🧺 2 шт" {
		t.Fatalf("quantity label = %q", kb[0][1].Text)
	}
	if kb[2][0].Text != "💔 Убрать из желаемого" {
		t.Fatalf("wishlist button label = %q", kb[2][0].Text)
	}
}
