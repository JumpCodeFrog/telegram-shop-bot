package bot

import (
	"context"
	"fmt"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/storage"
)

// onWishlistToggle toggles a product in the user's wishlist and updates the button in-place.
func (b *Bot) onWishlistToggle(cbID string, chatID, userID int64, msgID int, data, lang string) {
	prodID, err := parseIDFromCallback(data, "wish:")
	if err != nil {
		b.logger.Error("parse wish callback", "error", err)
		b.ack(cbID)
		return
	}

	ctx := context.Background()
	inWishlist, err := b.wishlist.IsInWishlist(ctx, userID, prodID)
	if err != nil {
		b.logger.Error("check wishlist", "error", err)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	}

	if inWishlist {
		if err := b.wishlist.Remove(ctx, userID, prodID); err != nil {
			b.logger.Error("wishlist remove", "error", err)
			b.alert(cbID, b.t(lang, "error_short"))
			return
		}
		b.toast(cbID, b.t(lang, "wishlist_removed"))
	} else {
		p, err := b.catalog.GetProduct(ctx, prodID)
		if err != nil {
			b.logger.Error("get product for wishlist", "error", err)
			b.alert(cbID, b.t(lang, "error_short"))
			return
		}
		if err := b.wishlist.Add(ctx, userID, prodID, p.PriceUSD, p.Stock); err != nil {
			b.logger.Error("wishlist add", "error", err)
			b.alert(cbID, b.t(lang, "error_short"))
			return
		}
		b.toast(cbID, b.t(lang, "wishlist_added"))
	}

	// Re-fetch product to rebuild keyboard with updated wishlist state.
	b.refreshProductKeyboard(chatID, userID, msgID, prodID, lang)
}

// onWishlistRemove removes a product from the wishlist screen (✖ button)
// and re-renders the wishlist in place.
func (b *Bot) onWishlistRemove(cbID string, chatID, userID int64, msgID int, data, lang string) {
	prodID, err := parseIDFromCallback(data, "wish:rm:")
	if err != nil {
		b.logger.Error("parse wish remove callback", "error", err)
		b.ack(cbID)
		return
	}

	ctx := context.Background()
	if err := b.wishlist.Remove(ctx, userID, prodID); err != nil {
		b.logger.Error("wishlist remove", "error", err)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	}
	b.toast(cbID, b.t(lang, "wishlist_removed"))
	b.sendWishlist(chatID, userID, msgID, lang)
}

// wishlistKeyboard builds one row per wishlist item: product button + ✖ removal.
func (b *Bot) wishlistKeyboard(lang string, products []storage.Product) StyledKeyboard {
	kb := make(StyledKeyboard, 0, len(products)+1)
	for _, p := range products {
		kb = append(kb, []StyledButton{
			b.styledBtn(BtnKeyCatalogProduct, "🛍 "+p.Name, fmt.Sprintf("product:%d", p.ID), StylePrimary),
			Btn("✖", fmt.Sprintf("wish:rm:%d", p.ID)),
		})
	}
	return append(kb, []StyledButton{
		Btn(b.t(lang, "btn_back"), "back:menu"),
		Btn(b.t(lang, "btn_menu"), "back:menu"),
	})
}

// handleWishlist shows the user's wishlist.
func (b *Bot) handleWishlist(msg *tgbotapi.Message) {
	b.sendWishlist(msg.Chat.ID, msg.From.ID, 0, msg.From.LanguageCode)
}

// sendWishlist renders the wishlist. If msgID > 0 it edits the existing message.
func (b *Bot) sendWishlist(chatID, userID int64, msgID int, lang string) {
	ctx := context.Background()

	products, err := b.wishlist.GetUserWishlist(ctx, userID)
	if err != nil {
		b.logger.Error("get user wishlist", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "error_short")))
		return
	}

	if len(products) == 0 {
		kb := StyledKeyboard{
			{b.styledBtn(BtnKeyMenuCatalog, b.t(lang, "btn_catalog"), "back:catalog", StylePrimary)},
			{Btn(b.t(lang, "btn_menu"), "back:menu")},
		}
		b.sendOrEditStyled(chatID, msgID, b.t(lang, "wishlist_empty"), "", kb)
		return
	}

	b.sendOrEditStyled(chatID, msgID, b.formatWishlistText(lang, products), "HTML", b.wishlistKeyboard(lang, products))
}
