package bot

import (
	"fmt"
	"testing"

	"shop_bot/internal/storage"
)

// flattenData returns callback data of every button row by row.
func flattenData(kb StyledKeyboard) []string {
	var data []string
	for _, row := range kb {
		for _, btn := range row {
			data = append(data, btn.CallbackData)
		}
	}
	return data
}

// TestMainMenuKeyboard_Layout verifies the reference two-column layout:
// [Каталог|Поиск] [Корзина|Избранное] [Заказы|Профиль] [Бонус за друга|Поддержка] [Условия].
func TestMainMenuKeyboard_Layout(t *testing.T) {
	t.Parallel()

	b := newTextBot(t)
	kb := b.mainMenuKeyboard("ru", b.t("ru", "btn_cart"))

	wantRows := [][]string{
		{"back:catalog", "search:hint"},
		{"back:cart", "back:wishlist"},
		{"back:orders", "back:profile"},
		{"ref:open", "support"},
		{"terms"},
	}

	if len(kb) != len(wantRows) {
		t.Fatalf("menu rows = %d, want %d", len(kb), len(wantRows))
	}

	total := 0
	for i, row := range kb {
		if len(row) != len(wantRows[i]) {
			t.Fatalf("row %d has %d buttons, want %d", i, len(row), len(wantRows[i]))
		}
		for j, btn := range row {
			total++
			if btn.CallbackData != wantRows[i][j] {
				t.Fatalf("row %d button %d callback = %q, want %q", i, j, btn.CallbackData, wantRows[i][j])
			}
			if btn.Text == "" {
				t.Fatalf("row %d button %d has empty label", i, j)
			}
		}
	}

	if total != 9 {
		t.Fatalf("menu has %d buttons, want 9", total)
	}
}

// TestMainMenuKeyboard_LabelsLocalized ensures button labels come from the locale files.
func TestMainMenuKeyboard_LabelsLocalized(t *testing.T) {
	t.Parallel()

	b := newTextBot(t)
	kb := b.mainMenuKeyboard("en", b.t("en", "btn_cart"))

	if got := kb[0][1].Text; got != "🔍 Search" {
		t.Fatalf("search label = %q, want %q", got, "🔍 Search")
	}
	if got := kb[1][1].Text; got != "❤️ Wishlist" {
		t.Fatalf("wishlist label = %q, want %q", got, "❤️ Wishlist")
	}
	if got := kb[3][0].Text; got != "🎁 Invite a friend" {
		t.Fatalf("referral label = %q, want %q", got, "🎁 Invite a friend")
	}
	if got := kb[4][0].Text; got != "📄 Terms" {
		t.Fatalf("terms label = %q, want %q", got, "📄 Terms")
	}
}

// TestSearchResultsKeyboard_ProductButtons verifies one product button per result
// plus the trailing Назад|Меню row.
func TestSearchResultsKeyboard_ProductButtons(t *testing.T) {
	t.Parallel()

	b := newTextBot(t)
	products := []storage.Product{
		{ID: 5, Name: "Tee", PriceUSD: 12.99},
		{ID: 7, Name: "Mug", PriceUSD: 4.50},
	}

	kb := b.searchResultsKeyboard("ru", products)

	if len(kb) != 3 {
		t.Fatalf("keyboard rows = %d, want 3 (2 products + nav)", len(kb))
	}
	for i, p := range products {
		if got, want := kb[i][0].CallbackData, fmt.Sprintf("product:%d", p.ID); got != want {
			t.Fatalf("product row %d callback = %q, want %q", i, got, want)
		}
	}

	nav := kb[len(kb)-1]
	if len(nav) != 2 || nav[0].CallbackData != "back:search" || nav[1].CallbackData != "back:menu" {
		t.Fatalf("nav row = %+v, want back:search + back:menu", nav)
	}
}

// TestWishlistKeyboard_RowsWithRemove verifies product button + ✖ removal per item.
func TestWishlistKeyboard_RowsWithRemove(t *testing.T) {
	t.Parallel()

	b := newTextBot(t)
	products := []storage.Product{
		{ID: 3, Name: "Cap", PriceUSD: 9.99},
	}

	kb := b.wishlistKeyboard("ru", products)

	if len(kb) != 2 {
		t.Fatalf("keyboard rows = %d, want 2 (1 product + nav)", len(kb))
	}
	row := kb[0]
	if len(row) != 2 {
		t.Fatalf("product row has %d buttons, want 2", len(row))
	}
	if row[0].CallbackData != "product:3" {
		t.Fatalf("product button callback = %q, want product:3", row[0].CallbackData)
	}
	if row[1].CallbackData != "wish:rm:3" || row[1].Text != "✖" {
		t.Fatalf("remove button = %+v, want ✖ wish:rm:3", row[1])
	}

	nav := flattenData(StyledKeyboard{kb[1]})
	if len(nav) != 2 || nav[0] != "back:menu" || nav[1] != "back:menu" {
		t.Fatalf("nav row callbacks = %v, want back:menu twice", nav)
	}
}
