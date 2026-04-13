package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/storage"
)

const productsPerPage = 5

// handleCatalog displays product categories with emoji.
func (b *Bot) handleCatalog(msg *tgbotapi.Message) {
	b.sendCatalog(msg.Chat.ID, 0, msg.From.LanguageCode)
}

// sendCatalog sends the category list. If msgID > 0, it edits the existing message.
func (b *Bot) sendCatalog(chatID int64, msgID int, lang string) {
	ctx := context.Background()
	categories, err := b.catalog.ListCategories(ctx)
	if err != nil {
		b.logger.Error("list categories", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "error_load_catalog")))
		return
	}

	if len(categories) == 0 {
		kb := StyledKeyboard{{Btn(b.t(lang, "btn_menu"), "back:menu")}}
		b.sendOrEditStyled(chatID, msgID, b.t(lang, "catalog_empty"), "", kb)
		return
	}

	// Category buttons — primary style to highlight them as navigation targets.
	kb := make(StyledKeyboard, 0, len(categories)+1)
	for _, cat := range categories {
		label := cat.Emoji + " " + cat.Name
	kb = append(kb, []StyledButton{b.styledBtn(BtnKeyMenuCatalog, label, fmt.Sprintf("category:%d", cat.ID), StylePrimary)})
	}
	kb = append(kb, []StyledButton{Btn(b.t(lang, "btn_menu"), "back:menu")})

	b.sendOrEditStyled(chatID, msgID, b.t(lang, "catalog_choose_category"), "", kb)
}

func (b *Bot) onCategorySelected(chatID, userID int64, msgID int, data, lang string) {
	// Parse: category:<id> or category:<id>:page:<n>
	parts := strings.SplitN(data, ":", 4)
	catID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		b.logger.Error("parse category callback", "error", err)
		return
	}

	page := 0
	if len(parts) == 4 && parts[2] == "page" {
		if n, err := strconv.Atoi(parts[3]); err == nil && n >= 0 {
			page = n
		}
	}

	ctx := context.Background()
	category, err := b.catalog.GetCategory(ctx, catID)
	if err != nil {
		b.logger.Error("get category", "category_id", catID, "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "error_load_catalog")))
		return
	}
	if category == nil {
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "error_load_catalog")))
		return
	}

	products, total, err := b.catalog.ListProductsPaged(ctx, catID, productsPerPage, page*productsPerPage)
	if err != nil {
		b.logger.Error("list products paged", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "error_load_catalog")))
		return
	}

	if total == 0 {
		kb := StyledKeyboard{
			{Btn(b.t(lang, "btn_back"), "back:catalog"), Btn(b.t(lang, "btn_menu"), "back:menu")},
		}
		b.sendOrEditStyled(chatID, msgID, b.t(lang, "category_no_products"), "", kb)
		return
	}

	totalPages := (total + productsPerPage - 1) / productsPerPage
	wishlistIDs, err := b.wishlist.GetUserWishlistIDs(ctx, userID)
	if err != nil {
		b.logger.Warn("get wishlist ids for category", "user_id", userID, "error", err)
		wishlistIDs = map[int64]struct{}{}
	}

	kb := make(StyledKeyboard, 0, len(products)+3)
	for _, p := range products {
		label := "🛍 " + p.Name
		if _, ok := wishlistIDs[p.ID]; ok {
			label = "❤️ " + p.Name
		}
		kb = append(kb, []StyledButton{b.styledBtn(BtnKeyMenuCatalog, label, fmt.Sprintf("product:%d", p.ID), StylePrimary)})
	}

	// Pagination row (only if more than one page).
	if totalPages > 1 {
		navRow := []StyledButton{}
		if page > 0 {
			navRow = append(navRow, Btn("◀️", fmt.Sprintf("category:%d:page:%d", catID, page-1)))
		}
		navRow = append(navRow, Btn(fmt.Sprintf("  %d/%d  ", page+1, totalPages), "noop"))
		if page+1 < totalPages {
			navRow = append(navRow, Btn("▶️", fmt.Sprintf("category:%d:page:%d", catID, page+1)))
		}
		kb = append(kb, navRow)
	}

	kb = append(kb, []StyledButton{
		Btn(b.t(lang, "btn_back"), "back:catalog"),
		Btn(b.t(lang, "btn_menu"), "back:menu"),
	})

	text := b.formatCategoryProductsText(lang, category, products, page, totalPages, wishlistIDs)
	b.sendOrEditStyled(chatID, msgID, text, "HTML", kb)
}

func (b *Bot) onProductSelected(chatID, userID int64, msgID int, data, lang string) {
	prodID, err := parseIDFromCallback(data, "product:")
	if err != nil {
		b.logger.Error("parse product callback", "error", err)
		return
	}

	ctx := context.Background()
	p, err := b.catalog.GetProduct(ctx, prodID)
	if err != nil {
		b.logger.Error("get product", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "error_load_product")))
		return
	}

	text := b.formatProductText(lang, p)

	inWishlist, _ := b.wishlist.IsInWishlist(ctx, userID, prodID)
	quantity, err := b.cartQuantity(ctx, userID, prodID)
	if err != nil {
		b.logger.Warn("get cart quantity for product view", "user_id", userID, "product_id", prodID, "error", err)
	}
	kb := b.productKeyboard(p, inWishlist, quantity, lang)
	kbJSON, _ := json.Marshal(map[string]interface{}{"inline_keyboard": kb})
	kbStr := string(kbJSON)

	if p.PhotoURL != "" {
		if msgID > 0 {
			// EditMessageMedia with styled keyboard via raw API.
			mediaJSON, _ := json.Marshal(map[string]interface{}{
				"type":       "photo",
				"media":      p.PhotoURL,
				"caption":    text,
				"parse_mode": "HTML",
			})
			if _, err := b.api.MakeRequest("editMessageMedia", tgbotapi.Params{
				"chat_id":      strconv.FormatInt(chatID, 10),
				"message_id":   strconv.Itoa(msgID),
				"media":        string(mediaJSON),
				"reply_markup": kbStr,
			}); err != nil {
				// Fallback: delete and send fresh photo.
				b.api.Request(tgbotapi.NewDeleteMessage(chatID, msgID))
				b.api.MakeRequest("sendPhoto", tgbotapi.Params{
					"chat_id":      strconv.FormatInt(chatID, 10),
					"photo":        p.PhotoURL,
					"caption":      text,
					"parse_mode":   "HTML",
					"reply_markup": kbStr,
				})
			}
		} else {
			b.api.MakeRequest("sendPhoto", tgbotapi.Params{
				"chat_id":      strconv.FormatInt(chatID, 10),
				"photo":        p.PhotoURL,
				"caption":      text,
				"parse_mode":   "HTML",
				"reply_markup": kbStr,
			})
		}
	} else {
		b.sendOrEditStyled(chatID, msgID, text, "HTML", kb)
	}
}

// productKeyboard builds the styled inline keyboard for a product detail view.
// Uses Bot API 9.4 button styles: success for add-to-cart, default for others.
func (b *Bot) productKeyboard(p *storage.Product, inWishlist bool, quantity int, lang string) StyledKeyboard {
	wishBtnLabel := "♥ " + b.t(lang, "btn_wishlist_add")
	if inWishlist {
		wishBtnLabel = "💔 " + b.t(lang, "btn_wishlist_remove")
	}
	return StyledKeyboard{
		{
			Btn("➖", fmt.Sprintf("productqty:minus:%d", p.ID)),
			Btn(b.productQuantityLabel(lang, quantity), "noop"),
			Btn("➕", fmt.Sprintf("productqty:plus:%d", p.ID)),
		},
		{b.styledBtn(BtnKeyProductAdd, b.t(lang, "btn_add_to_cart"), fmt.Sprintf("cart:add:%d", p.ID), StyleSuccess)},
		{
			Btn(wishBtnLabel, fmt.Sprintf("wish:%d", p.ID)),
			Btn(b.t(lang, "btn_back"), fmt.Sprintf("back:category:%d", p.CategoryID)),
			Btn(b.t(lang, "btn_menu"), "back:menu"),
		},
	}
}

func (b *Bot) cartQuantity(ctx context.Context, userID, prodID int64) (int, error) {
	view, err := b.cart.Get(ctx, userID)
	if err != nil {
		return 0, err
	}

	for _, item := range view.Items {
		if item.Product.ID == prodID {
			return item.Quantity, nil
		}
	}

	return 0, nil
}

func (b *Bot) refreshProductKeyboard(chatID, userID int64, msgID int, prodID int64, lang string) {
	ctx := context.Background()

	p, err := b.catalog.GetProduct(ctx, prodID)
	if err != nil {
		b.logger.Error("get product for keyboard refresh", "product_id", prodID, "error", err)
		return
	}

	inWishlist, err := b.wishlist.IsInWishlist(ctx, userID, prodID)
	if err != nil {
		b.logger.Error("check wishlist for keyboard refresh", "product_id", prodID, "error", err)
		return
	}

	quantity, err := b.cartQuantity(ctx, userID, prodID)
	if err != nil {
		b.logger.Error("get cart quantity for keyboard refresh", "product_id", prodID, "error", err)
		return
	}

	kb := b.productKeyboard(p, inWishlist, quantity, lang)
	kbJSON, err := json.Marshal(map[string]interface{}{"inline_keyboard": kb})
	if err != nil {
		b.logger.Error("marshal product keyboard", "error", err)
		return
	}
	if _, err := b.api.MakeRequest("editMessageReplyMarkup", tgbotapi.Params{
		"chat_id":      strconv.FormatInt(chatID, 10),
		"message_id":   strconv.Itoa(msgID),
		"reply_markup": string(kbJSON),
	}); err != nil {
		b.logger.Warn("refreshProductKeyboard edit markup", "error", err)
	}
}
