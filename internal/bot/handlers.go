package bot

import (
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// route dispatches an incoming update to the appropriate handler.
func (b *Bot) route(update tgbotapi.Update) {
	switch {
	case update.PreCheckoutQuery != nil:
		b.handlePreCheckout(update.PreCheckoutQuery)

	case update.InlineQuery != nil:
		b.handleInlineQuery(update.InlineQuery)

	case update.Message != nil:
		b.routeMessage(update.Message)

	case update.CallbackQuery != nil:
		b.handleCallback(update.CallbackQuery)
	}
}

// routeMessage dispatches a message to the correct command handler.
func (b *Bot) routeMessage(msg *tgbotapi.Message) {
	if msg.SuccessfulPayment != nil {
		b.handleSuccessfulPayment(msg)
		return
	}

	routeCtx, routeCancel := handlerCtx()
	defer routeCancel()

	// Check if user is entering a promo code.
	if msg.Command() == "" {
		promoAt, _ := b.fsm.GetPromoState(routeCtx, msg.From.ID)
		if !promoAt.IsZero() {
			b.handlePromoInput(msg)
			return
		}
	}

	// Check if the user is in an add-product dialog.
	addState, _ := b.fsm.GetAddProductState(routeCtx, msg.From.ID)
	inAddState := addState != nil

	if msg.Command() == "" ||
		(msg.Command() == "skip" && inAddState) ||
		(msg.Command() == "cancel" && inAddState) {
		if b.handleAddProductStep(msg) {
			return
		}
	}

	switch msg.Command() {
	case "start":
		b.handleStart(msg)
	case "help":
		b.handleHelp(msg)
	case "support":
		b.onSupport(msg.Chat.ID, 0, msg.From.LanguageCode)
	case "paysupport":
		b.onPaySupport(msg.Chat.ID, 0, msg.From.LanguageCode)
	case "terms":
		b.onTerms(msg.Chat.ID, 0, msg.From.LanguageCode)
	case "catalog":
		b.handleCatalog(msg)
	case "search":
		b.handleSearch(msg)
	case "cart":
		b.handleCart(msg)
	case "orders":
		b.handleOrders(msg)

	case "profile":
		b.handleProfile(msg)

	case "wishlist":
		b.handleWishlist(msg)

	case "cancel":
		b.handleCancel(msg)

	// Admin commands.
	case "admin":
		b.handleAdmin(msg)
	case "addproduct":
		b.handleAddProduct(msg)
	case "editproduct":
		b.routeEditProduct(msg)
	case "deleteproduct":
		b.handleDeleteProduct(msg)
	case "orders_all":
		b.handleOrdersAll(msg)
	case "setdelivered":
		b.handleSetDelivered(msg)

	// Category management.
	case "addcategory":
		b.handleAddCategory(msg)
	case "editcategory":
		b.handleEditCategory(msg)
	case "deletecategory":
		b.handleDeleteCategory(msg)
	case "listcategories":
		b.handleListCategories(msg)

	// Promo codes.
	case "addpromo":
		b.handleAddPromo(msg)
	case "listpromos":
		b.handleListPromos(msg)
	case "deletepromo":
		b.handleDeletePromo(msg)

	// Analytics.
	case "analytics":
		b.handleAnalytics(msg)

	// Export.
	case "export_orders":
		b.handleExportOrders(msg)

	// Button style customization.
	case "btnstyle":
		b.handleBtnStyleAdmin(msg)
	}
}

// ack answers a callback query silently (removes the loading spinner).
func (b *Bot) ack(cbID string) {
	_, _ = b.api.Request(tgbotapi.NewCallback(cbID, ""))
}

// toast answers a callback query with a short non-blocking notification at the top of the screen.
// Use for quick confirmations (cart add, wishlist). Use alert() for blocking popups on errors.
func (b *Bot) toast(cbID, text string) {
	_, _ = b.api.Request(tgbotapi.NewCallback(cbID, text))
}

// alert answers a callback query with a native Telegram popup (show_alert).
func (b *Bot) alert(cbID, text string) {
	cb := tgbotapi.NewCallback(cbID, text)
	cb.ShowAlert = true
	_, _ = b.api.Request(cb)
}

// handleCallback routes callback queries based on their data prefix.
func (b *Bot) handleCallback(cb *tgbotapi.CallbackQuery) {
	data := cb.Data
	chatID := cb.Message.Chat.ID
	msgID := cb.Message.MessageID
	userID := cb.From.ID
	lang := cb.From.LanguageCode

	switch {
	case strings.HasPrefix(data, "category:"):
		b.ack(cb.ID)
		b.onCategorySelected(chatID, userID, b.prepareTextRenderMessageID(chatID, cb.Message), data, lang)

	case strings.HasPrefix(data, "product:"):
		b.ack(cb.ID)
		b.onProductSelected(chatID, userID, msgID, data, lang)

	case strings.HasPrefix(data, "productqty:plus:"):
		b.onProductQuantityChange(cb.ID, chatID, userID, msgID, data, "productqty:plus:", 1, lang)

	case strings.HasPrefix(data, "productqty:minus:"):
		b.onProductQuantityChange(cb.ID, chatID, userID, msgID, data, "productqty:minus:", -1, lang)

	case strings.HasPrefix(data, "cart:add:"):
		b.onCartAdd(cb.ID, chatID, userID, msgID, data, lang)

	case strings.HasPrefix(data, "cart:plus:"):
		b.ack(cb.ID)
		b.onCartPlus(chatID, userID, msgID, data, lang)

	case strings.HasPrefix(data, "cart:minus:"):
		b.ack(cb.ID)
		b.onCartMinus(chatID, userID, msgID, data, lang)

	case strings.HasPrefix(data, "cart:del:"):
		b.ack(cb.ID)
		b.onCartDel(chatID, userID, msgID, data, lang)

	case data == "cart:checkout":
		b.ack(cb.ID)
		b.onCartCheckout(chatID, userID, b.prepareTextRenderMessageID(chatID, cb.Message), lang)

	case data == "promo:enter":
		b.ack(cb.ID)
		b.onPromoEnter(chatID, userID, lang)

	case strings.HasPrefix(data, "order:confirm"):
		b.ack(cb.ID)
		b.onOrderConfirm(chatID, userID, b.prepareTextRenderMessageID(chatID, cb.Message), data, lang)

	case strings.HasPrefix(data, "order:cancel:"):
		b.onOrderCancel(cb.ID, chatID, userID, b.prepareTextRenderMessageID(chatID, cb.Message), data, lang)

	case strings.HasPrefix(data, "pay:stars:"):
		b.onPayStars(cb.ID, chatID, userID, msgID, data, lang)

	case strings.HasPrefix(data, "pay:crypto:"):
		b.onPayCrypto(cb.ID, chatID, userID, msgID, data, lang)

	case strings.HasPrefix(data, "admin:togglestock:"):
		b.ack(cb.ID)
		if b.isAdmin(userID) {
			b.onAdminToggleStock(chatID, data)
		}

	case strings.HasPrefix(data, "analytics:"):
		b.ack(cb.ID)
		if b.isAdmin(userID) {
			b.handleAnalyticsCallback(chatID, msgID, data)
		}

	case data == "admin:btnlist":
		b.ack(cb.ID)
		if b.isAdmin(userID) {
			b.sendBtnStyleList(chatID, msgID)
		}

	case strings.HasPrefix(data, "admin:btnpick:"):
		b.ack(cb.ID)
		if b.isAdmin(userID) {
			key := strings.TrimPrefix(data, "admin:btnpick:")
			b.sendBtnStylePicker(chatID, msgID, key)
		}

	case strings.HasPrefix(data, "admin:setstyle:"):
		b.ack(cb.ID)
		if b.isAdmin(userID) {
			b.onAdminSetStyle(chatID, msgID, data)
		}

	case strings.HasPrefix(data, "wish:"):
		b.onWishlistToggle(cb.ID, chatID, userID, msgID, data, lang)

	case data == "profile_view":
		b.ack(cb.ID)
		b.sendProfile(chatID, userID, b.prepareTextRenderMessageID(chatID, cb.Message), lang)

	case strings.HasPrefix(data, "back:"):
		b.ack(cb.ID)
		b.onBack(chatID, userID, b.prepareTextRenderMessageID(chatID, cb.Message), data, lang)

	case data == "support":
		b.ack(cb.ID)
		b.onSupport(chatID, b.prepareTextRenderMessageID(chatID, cb.Message), lang)

	case data == "paysupport":
		b.ack(cb.ID)
		b.onPaySupport(chatID, b.prepareTextRenderMessageID(chatID, cb.Message), lang)

	case data == "terms":
		b.ack(cb.ID)
		b.onTerms(chatID, b.prepareTextRenderMessageID(chatID, cb.Message), lang)

	default:
		b.ack(cb.ID)
	}
}

func (b *Bot) onBack(chatID, userID int64, msgID int, data, lang string) {
	target := strings.TrimPrefix(data, "back:")

	switch {
	case target == "menu":
		ctx, cancel := handlerCtx()
		defer cancel()
		b.sendMainMenu(chatID, userID, msgID, lang, ctx)

	case target == "catalog":
		b.sendCatalog(chatID, msgID, lang)

	case target == "cart":
		b.sendCart(chatID, userID, msgID, lang)

	case target == "orders":
		b.sendOrders(chatID, userID, msgID, lang)

	case target == "profile":
		b.sendProfile(chatID, userID, msgID, lang)

	case strings.HasPrefix(target, "category:"):
		b.onCategorySelected(chatID, userID, msgID, target, lang)
	}
}

// --- Payment handlers ---

// send is a convenience wrapper that logs errors from the Telegram API.
func (b *Bot) send(c tgbotapi.Chattable) {
	if _, err := b.api.Send(c); err != nil {
		b.logger.Error("send message", "error", err)
	}
}

// --- Helpers ---

// parseIDFromCallback extracts a numeric ID from callback data after the given prefix.
func parseIDFromCallback(data, prefix string) (int64, error) {
	raw := strings.TrimPrefix(data, prefix)
	return strconv.ParseInt(raw, 10, 64)
}

// messageSupportsTextEdit reports whether a callback-origin message can be
// updated with EditMessageText rather than replaced with a fresh text message.
func messageSupportsTextEdit(msg *tgbotapi.Message) bool {
	if msg == nil {
		return true
	}

	return len(msg.Photo) == 0 &&
		msg.Animation == nil &&
		msg.Audio == nil &&
		msg.Document == nil &&
		msg.Sticker == nil &&
		msg.Video == nil &&
		msg.VideoNote == nil &&
		msg.Voice == nil
}

// textRenderMessageID returns the message ID to edit for a text screen.
// Media-origin messages return 0 so the caller can send a fresh text message.
func textRenderMessageID(msg *tgbotapi.Message) int {
	if msg == nil || !messageSupportsTextEdit(msg) {
		return 0
	}
	return msg.MessageID
}

// prepareTextRenderMessageID converts media-origin callback messages into
// send-new-message mode and removes the old media message to avoid stale UI.
func (b *Bot) prepareTextRenderMessageID(chatID int64, msg *tgbotapi.Message) int {
	msgID := textRenderMessageID(msg)
	if msg == nil || msgID != 0 || msg.MessageID <= 0 {
		return msgID
	}

	del := tgbotapi.NewDeleteMessage(chatID, msg.MessageID)
	if _, err := b.api.Request(del); err != nil {
		b.logger.Warn("delete media callback message before text render",
			"chat_id", chatID,
			"message_id", msg.MessageID,
			"error", err,
		)
	}

	return 0
}
