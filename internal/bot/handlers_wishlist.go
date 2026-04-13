package bot

import (
	"context"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
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

// handleWishlist shows the user's wishlist.
func (b *Bot) handleWishlist(msg *tgbotapi.Message) {
	ctx := context.Background()
	lang := msg.From.LanguageCode
	userID := msg.From.ID
	chatID := msg.Chat.ID

	products, err := b.wishlist.GetUserWishlist(ctx, userID)
	if err != nil {
		b.logger.Error("get user wishlist", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "error_short")))
		return
	}

	if len(products) == 0 {
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "wishlist_empty")))
		return
	}

	reply := tgbotapi.NewMessage(chatID, b.formatWishlistText(lang, products))
	reply.ParseMode = "HTML"
	b.send(reply)
}
