package bot

import (
	"context"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// route dispatches an incoming update to the appropriate handler.
func (b *Bot) route(ctx context.Context, update tgbotapi.Update) {
	switch {
	case update.PreCheckoutQuery != nil:
		b.handlePreCheckout(ctx, update.PreCheckoutQuery)

	case update.InlineQuery != nil:
		b.handleInlineQuery(ctx, update.InlineQuery)

	case update.Message != nil:
		b.routeMessage(ctx, update.Message)

	case update.CallbackQuery != nil:
		b.handleCallback(ctx, update.CallbackQuery)
	}
}

// routeMessage dispatches a message to the correct command handler.
func (b *Bot) routeMessage(ctx context.Context, msg *tgbotapi.Message) {
	if msg.SuccessfulPayment != nil {
		b.handleSuccessfulPayment(ctx, msg)
		return
	}

	// Check if user is entering a promo code.
	if msg.Command() == "" {
		promoAt, _ := b.fsm.GetPromoState(ctx, msg.From.ID)
		if !promoAt.IsZero() {
			b.handlePromoInput(ctx, msg)
			return
		}
	}

	// Check if user is writing a review text (post-rating FSM step).
	if msg.Command() == "" {
		reviewState, _ := b.fsm.GetReviewState(ctx, msg.From.ID)
		if reviewState != nil {
			b.handleReviewTextInput(ctx, msg, reviewState)
			return
		}
	}

	// Check if the user is in an add-product dialog.
	addState, _ := b.fsm.GetAddProductState(ctx, msg.From.ID)
	inAddState := addState != nil

	if msg.Command() == "" ||
		(msg.Command() == "skip" && inAddState) ||
		(msg.Command() == "done" && inAddState) ||
		(msg.Command() == "cancel" && inAddState) {
		if b.handleAddProductStep(ctx, msg) {
			return
		}
	}

	switch msg.Command() {
	case "start":
		b.handleStart(ctx, msg)
	case "help":
		b.handleHelp(msg)
	case "support":
		b.onSupport(msg.Chat.ID, 0, msg.From.LanguageCode)
	case "paysupport":
		b.onPaySupport(msg.Chat.ID, 0, msg.From.LanguageCode)
	case "terms":
		b.onTerms(msg.Chat.ID, 0, msg.From.LanguageCode)
	case "catalog":
		b.handleCatalog(ctx, msg)
	case "search":
		b.handleSearch(ctx, msg)
	case "cart":
		b.handleCart(ctx, msg)
	case "orders":
		b.handleOrders(ctx, msg)

	case "mysubs":
		b.handleMySubs(ctx, msg)

	case "profile":
		b.handleProfile(ctx, msg)

	case "referral":
		b.handleReferral(ctx, msg)

	case "wishlist":
		b.handleWishlist(ctx, msg)

	case "cancel":
		b.handleCancel(ctx, msg)

	// Admin commands.
	case "admin":
		b.handleAdmin(msg)
	case "addproduct":
		b.handleAddProduct(ctx, msg)
	case "editproduct":
		b.routeEditProduct(ctx, msg)
	case "deleteproduct":
		b.handleDeleteProduct(ctx, msg)
	case "orders_all":
		b.handleOrdersAll(msg)
	case "order":
		b.handleOrderCard(msg)
	case "setdelivered":
		b.handleSetDelivered(msg)
	case "reviews":
		b.handleReviewsAdmin(ctx, msg)

	// Category management.
	case "addcategory":
		b.handleAddCategory(ctx, msg)
	case "editcategory":
		b.handleEditCategory(ctx, msg)
	case "deletecategory":
		b.handleDeleteCategory(ctx, msg)
	case "listcategories":
		b.handleListCategories(ctx, msg)

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

	// Payment review queue.
	case "payreview":
		b.handlePayReview(msg)

	// Admin refunds.
	case "refund":
		b.handleRefundCommand(msg)

	// Payment provider status.
	case "paystatus":
		b.handlePayStatus(msg)

	// Balance adjustments.
	case "setbalance":
		b.handleSetBalance(msg)

	// Export.
	case "export_orders":
		b.handleExportOrders(msg)

	// Button style customization.
	case "btnstyle":
		b.handleBtnStyleAdmin(ctx, msg)
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
func (b *Bot) handleCallback(ctx context.Context, cb *tgbotapi.CallbackQuery) {
	// Inline-mode callbacks arrive without an attached message; nothing to render on.
	if cb.Message == nil {
		b.ack(cb.ID)
		return
	}

	data := cb.Data
	chatID := cb.Message.Chat.ID
	msgID := cb.Message.MessageID
	userID := cb.From.ID
	lang := cb.From.LanguageCode

	switch {
	case strings.HasPrefix(data, "category:"):
		b.ack(cb.ID)
		b.onCategorySelected(ctx, chatID, userID, b.prepareTextRenderMessageID(chatID, cb.Message), data, lang)

	case strings.HasPrefix(data, "product:"):
		b.ack(cb.ID)
		b.onProductSelected(ctx, chatID, userID, msgID, data, lang)

	case strings.HasPrefix(data, "productqty:plus:"):
		b.onProductQuantityChange(ctx, cb.ID, chatID, userID, msgID, data, "productqty:plus:", 1, lang)

	case strings.HasPrefix(data, "productqty:minus:"):
		b.onProductQuantityChange(ctx, cb.ID, chatID, userID, msgID, data, "productqty:minus:", -1, lang)

	case strings.HasPrefix(data, "cart:add:"):
		b.onCartAdd(ctx, cb.ID, chatID, userID, msgID, data, lang)

	case strings.HasPrefix(data, "cart:plus:"):
		b.ack(cb.ID)
		b.onCartPlus(ctx, chatID, userID, msgID, data, lang)

	case strings.HasPrefix(data, "cart:minus:"):
		b.ack(cb.ID)
		b.onCartMinus(ctx, chatID, userID, msgID, data, lang)

	case strings.HasPrefix(data, "cart:del:"):
		b.ack(cb.ID)
		b.onCartDel(ctx, chatID, userID, msgID, data, lang)

	case data == "cart:checkout":
		b.ack(cb.ID)
		b.onCartCheckout(ctx, chatID, userID, b.prepareTextRenderMessageID(chatID, cb.Message), lang)

	case data == "promo:enter":
		b.ack(cb.ID)
		b.onPromoEnter(ctx, chatID, userID, lang)

	case strings.HasPrefix(data, "order:confirm"):
		b.ack(cb.ID)
		b.onOrderConfirm(ctx, chatID, userID, b.prepareTextRenderMessageID(chatID, cb.Message), data, lang)

	case strings.HasPrefix(data, "order:cancel:"):
		b.onOrderCancel(ctx, cb.ID, chatID, userID, b.prepareTextRenderMessageID(chatID, cb.Message), data, lang)

	case strings.HasPrefix(data, "pay:stars:"):
		b.onPayStars(ctx, cb.ID, chatID, userID, msgID, data, lang)

	case strings.HasPrefix(data, "pay:crypto:"):
		b.onPayCrypto(ctx, cb.ID, chatID, userID, msgID, data, lang)

	case strings.HasPrefix(data, "pay:yookassa:"):
		b.onPayYooKassa(ctx, cb.ID, chatID, userID, msgID, data, lang)

	case strings.HasPrefix(data, "pay:stripe:"):
		b.onPayStripe(ctx, cb.ID, chatID, userID, msgID, data, lang)

	case strings.HasPrefix(data, "pay:ton:"):
		b.onPayTON(ctx, cb.ID, chatID, userID, msgID, data, lang)

	case strings.HasPrefix(data, "pay:nowpayments:"):
		b.onPayNowpayments(ctx, cb.ID, chatID, userID, msgID, data, lang)

	case strings.HasPrefix(data, "pay:balance:"):
		b.onPayBalance(ctx, cb.ID, chatID, userID, msgID, data, lang)

	case strings.HasPrefix(data, "admin:togglestock:"):
		b.ack(cb.ID)
		if b.isAdmin(userID) {
			b.onAdminToggleStock(ctx, chatID, data, lang)
		}

	case strings.HasPrefix(data, "admin:photos:"):
		b.ack(cb.ID)
		if b.isAdmin(userID) {
			if prodID, err := parseIDFromCallback(data, "admin:photos:"); err == nil {
				b.sendAdminPhotoList(ctx, chatID, msgID, prodID, lang)
			}
		}

	case strings.HasPrefix(data, "admin:photodel:"):
		b.ack(cb.ID)
		if b.isAdmin(userID) {
			b.onAdminPhotoDelete(ctx, chatID, msgID, data, lang)
		}

	case strings.HasPrefix(data, "admin:photoadd:"):
		b.ack(cb.ID)
		if b.isAdmin(userID) {
			b.onAdminPhotoAdd(ctx, chatID, userID, data, lang)
		}

	case strings.HasPrefix(data, "admin:payrevdo:"):
		b.ack(cb.ID)
		if b.isAdmin(userID) {
			b.onAdminPayReviewCallback(chatID, msgID, userID, data, lang)
		}

	case strings.HasPrefix(data, "admin:payrev:"):
		b.ack(cb.ID)
		if b.isAdmin(userID) {
			b.onAdminPayReviewCallback(chatID, msgID, userID, data, lang)
		}

	case strings.HasPrefix(data, "admin:refund:"):
		b.ack(cb.ID)
		if b.isAdmin(userID) {
			b.onAdminRefundCallback(chatID, msgID, userID, data, lang)
		}

	case strings.HasPrefix(data, "analytics:"):
		b.ack(cb.ID)
		if b.isAdmin(userID) {
			b.handleAnalyticsCallback(chatID, msgID, data, cb.From.LanguageCode)
		}

	case data == "admin:btnlist":
		b.ack(cb.ID)
		if b.isAdmin(userID) {
			b.sendBtnStyleList(ctx, chatID, msgID, lang)
		}

	case strings.HasPrefix(data, "admin:btnpick:"):
		b.ack(cb.ID)
		if b.isAdmin(userID) {
			key := strings.TrimPrefix(data, "admin:btnpick:")
			b.sendBtnStylePicker(ctx, chatID, msgID, key, lang)
		}

	case strings.HasPrefix(data, "admin:setstyle:"):
		b.ack(cb.ID)
		if b.isAdmin(userID) {
			b.onAdminSetStyle(ctx, chatID, msgID, data, lang)
		}

	case strings.HasPrefix(data, "wish:rm:"):
		b.onWishlistRemove(ctx, cb.ID, chatID, userID, msgID, data, lang)

	case strings.HasPrefix(data, "wish:"):
		b.onWishlistToggle(ctx, cb.ID, chatID, userID, msgID, data, lang)

	case data == "profile_view":
		b.ack(cb.ID)
		b.sendProfile(ctx, chatID, userID, b.prepareTextRenderMessageID(chatID, cb.Message), lang)

	case strings.HasPrefix(data, "review:"):
		b.handleReviewCallback(ctx, cb)

	case strings.HasPrefix(data, "sub:cancel:"):
		b.onSubCancel(ctx, cb.ID, chatID, userID, b.prepareTextRenderMessageID(chatID, cb.Message), data, lang)

	case strings.HasPrefix(data, "ref:"):
		b.ack(cb.ID)
		if data == "ref:open" {
			b.sendReferralScreen(ctx, chatID, userID, b.prepareTextRenderMessageID(chatID, cb.Message), lang)
		}

	case data == "search:hint":
		b.ack(cb.ID)
		b.sendSearchHint(chatID, b.prepareTextRenderMessageID(chatID, cb.Message), lang)

	case strings.HasPrefix(data, "back:"):
		b.ack(cb.ID)
		b.onBack(ctx, chatID, userID, b.prepareTextRenderMessageID(chatID, cb.Message), data, lang)

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

func (b *Bot) onBack(ctx context.Context, chatID, userID int64, msgID int, data, lang string) {
	target := strings.TrimPrefix(data, "back:")

	switch {
	case target == "menu":
		b.sendMainMenu(chatID, userID, msgID, lang, ctx)

	case target == "catalog":
		b.sendCatalog(ctx, chatID, msgID, lang)

	case target == "cart":
		b.sendCart(ctx, chatID, userID, msgID, lang)

	case target == "orders":
		b.sendOrders(ctx, chatID, userID, msgID, lang)

	case target == "profile":
		b.sendProfile(ctx, chatID, userID, msgID, lang)

	case target == "wishlist":
		b.sendWishlist(ctx, chatID, userID, msgID, lang)

	case target == "search":
		b.sendSearchHint(chatID, msgID, lang)

	case strings.HasPrefix(target, "category:"):
		b.onCategorySelected(ctx, chatID, userID, msgID, target, lang)
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
