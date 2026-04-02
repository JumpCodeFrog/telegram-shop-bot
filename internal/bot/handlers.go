package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/storage"
)

const productsPerPage = 5

// route dispatches an incoming update to the appropriate handler.
func (b *Bot) route(update tgbotapi.Update) {
	switch {
	case update.PreCheckoutQuery != nil:
		b.handlePreCheckout(update.PreCheckoutQuery)

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

	// Check if user is entering a promo code.
	if msg.Command() == "" {
		promoAt, _ := b.fsm.GetPromoState(context.Background(), msg.From.ID)
		if !promoAt.IsZero() {
			b.handlePromoInput(msg)
			return
		}
	}

	// Check if the user is in an add-product dialog.
	addState, _ := b.fsm.GetAddProductState(context.Background(), msg.From.ID)
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
	}
}

// handleCancel cancels any active dialog for the user.
func (b *Bot) handleCancel(msg *tgbotapi.Message) {
	ctx := context.Background()
	lang := msg.From.LanguageCode

	inAdd := false
	if _, err := b.fsm.GetAddProductState(ctx, msg.From.ID); err == nil {
		inAdd = true
		_ = b.fsm.DelAddProductState(ctx, msg.From.ID)
	}

	inPromo := false
	if _, err := b.fsm.GetPromoState(ctx, msg.From.ID); err == nil {
		inPromo = true
		_ = b.fsm.DelPromoState(ctx, msg.From.ID)
	}

	switch {
	case inAdd:
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "cancel_add_product")))
	case inPromo:
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "cancel_promo")))
	default:
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "cancel_nothing")))
	}
}

// handleStart sends a welcome message with the main menu inline keyboard.
func (b *Bot) handleStart(msg *tgbotapi.Message) {
	lang := msg.From.LanguageCode
	// Handle referral deep links
	refCode := strings.TrimSpace(msg.CommandArguments())
	if refCode != "" {
		ctx := context.Background()
		referrer, err := b.referrals.GetUserByReferralCode(ctx, refCode)
		if err == nil && referrer != nil && referrer.TelegramID != msg.From.ID {
			// Check registration limit (Anti-Fraud)
			allowed, _ := b.referralService.CheckRegistrationLimit(ctx, referrer.TelegramID)
			if allowed {
				_ = b.referrals.SetReferrer(ctx, msg.From.ID, referrer.ID)
				// Bonus will be awarded on first purchase (Anti-Fraud)
				b.logger.Info("referral link used", "user_id", msg.From.ID, "referrer_id", referrer.ID)
			} else {
				b.logger.Warn("referral limit reached", "referrer_id", referrer.ID)
			}
		}
	}

	ctx := context.Background()

	// Build cart button label with item count if cart is non-empty.
	cartLabel := b.t(lang, "btn_cart")
	if cartView, err := b.cart.Get(ctx, msg.From.ID); err == nil && len(cartView.Items) > 0 {
		total := 0
		for _, item := range cartView.Items {
			total += item.Quantity
		}
		cartLabel = fmt.Sprintf("🛒 %s (%d)", b.t(lang, "cart"), total)
	}

	// Build welcome text with brief user status (active orders count).
	welcomeText := b.t(lang, "start_welcome")
	if orders, err := b.order.GetUserOrders(ctx, msg.From.ID); err == nil {
		activeOrders := 0
		for _, o := range orders {
			if o.Status == storage.OrderStatusPending {
				activeOrders++
			}
		}
		if activeOrders > 0 {
			welcomeText += fmt.Sprintf("\n\n📦 %s: %d", b.t(lang, "active_orders"), activeOrders)
		}
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_catalog"), "back:catalog"),
			tgbotapi.NewInlineKeyboardButtonData(cartLabel, "back:cart"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_orders"), "back:orders"),
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_profile"), "back:profile"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_support"), "support"),
		),
	)

	reply := tgbotapi.NewMessage(msg.Chat.ID, welcomeText)
	reply.ReplyMarkup = keyboard
	b.send(reply)
}

// handleHelp sends a list of available commands.
func (b *Bot) handleHelp(msg *tgbotapi.Message) {
	reply := tgbotapi.NewMessage(msg.Chat.ID, b.t(msg.From.LanguageCode, "help_text"))
	b.send(reply)
}

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
		text := b.t(lang, "catalog_empty")
		if msgID > 0 {
			edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
			b.send(edit)
		} else {
			b.send(tgbotapi.NewMessage(chatID, text))
		}
		return
	}

	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(categories))
	for _, cat := range categories {
		label := cat.Emoji + " " + cat.Name
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(label, fmt.Sprintf("category:%d", cat.ID)),
		))
	}
	keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)

	text := b.t(lang, "catalog_choose_category")
	if msgID > 0 {
		edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
		edit.ReplyMarkup = &keyboard
		b.send(edit)
	} else {
		reply := tgbotapi.NewMessage(chatID, text)
		reply.ReplyMarkup = keyboard
		b.send(reply)
	}
}

// handleCart displays the user's cart with quantity controls and totals.
func (b *Bot) handleCart(msg *tgbotapi.Message) {
	b.sendCart(msg.Chat.ID, msg.From.ID, 0, msg.From.LanguageCode)
}

// sendCart sends the cart view. If msgID > 0, it edits the existing message.
func (b *Bot) sendCart(chatID, userID int64, msgID int, lang string) {
	ctx := context.Background()
	view, err := b.cart.Get(ctx, userID)
	if err != nil {
		b.logger.Error("get cart", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "error_load_cart")))
		return
	}

	if len(view.Items) == 0 {
		text := b.t(lang, "cart_empty_text")
		backKeyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_back"), "back:catalog"),
			),
		)
		if msgID > 0 {
			edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
			edit.ReplyMarkup = &backKeyboard
			b.send(edit)
		} else {
			reply := tgbotapi.NewMessage(chatID, text)
			reply.ReplyMarkup = backKeyboard
			b.send(reply)
		}
		return
	}

	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(view.Items)+2)
	for _, item := range view.Items {
		pid := strconv.FormatInt(item.Product.ID, 10)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("➖", "cart:minus:"+pid),
			tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("%s (%d)", item.Product.Name, item.Quantity), "noop"),
			tgbotapi.NewInlineKeyboardButtonData("➕", "cart:plus:"+pid),
			tgbotapi.NewInlineKeyboardButtonData("🗑", "cart:del:"+pid),
		))
	}
	rows = append(rows,
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_checkout"), "cart:checkout"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_back"), "back:catalog"),
		),
	)
	keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)

	text := b.formatCartText(lang, view)
	if msgID > 0 {
		edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
		edit.ParseMode = "HTML"
		edit.ReplyMarkup = &keyboard
		b.send(edit)
	} else {
		reply := tgbotapi.NewMessage(chatID, text)
		reply.ParseMode = "HTML"
		reply.ReplyMarkup = keyboard
		b.send(reply)
	}
}

// handleOrders displays the user's order history with formatted statuses.
func (b *Bot) handleOrders(msg *tgbotapi.Message) {
	b.sendOrders(msg.Chat.ID, msg.From.ID, 0, msg.From.LanguageCode)
}

// sendOrders sends the order history. If msgID > 0, it edits the existing message.
func (b *Bot) sendOrders(chatID, userID int64, msgID int, lang string) {
	ctx := context.Background()
	orders, err := b.order.GetUserOrders(ctx, userID)
	if err != nil {
		b.logger.Error("get user orders", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "error_load_orders")))
		return
	}

	if len(orders) == 0 {
		text := b.t(lang, "orders_empty")
		if msgID > 0 {
			edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
			b.send(edit)
		} else {
			b.send(tgbotapi.NewMessage(chatID, text))
		}
		return
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_back"), "back:catalog"),
		),
	)

	text := b.formatOrdersText(lang, orders)
	if msgID > 0 {
		edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
		edit.ParseMode = "HTML"
		edit.ReplyMarkup = &keyboard
		b.send(edit)
	} else {
		reply := tgbotapi.NewMessage(chatID, text)
		reply.ParseMode = "HTML"
		reply.ReplyMarkup = keyboard
		b.send(reply)
	}
}

// ack answers a callback query silently (removes the loading spinner).
func (b *Bot) ack(cbID string) {
	b.api.Request(tgbotapi.NewCallback(cbID, ""))
}

// alert answers a callback query with a native Telegram popup (show_alert).
func (b *Bot) alert(cbID, text string) {
	cb := tgbotapi.NewCallback(cbID, text)
	cb.ShowAlert = true
	b.api.Request(cb)
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

// --- Callback sub-handlers ---

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
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_back"), "back:catalog"),
			),
		)
		if msgID > 0 {
			edit := tgbotapi.NewEditMessageText(chatID, msgID, b.t(lang, "category_no_products"))
			edit.ReplyMarkup = &keyboard
			b.send(edit)
		} else {
			reply := tgbotapi.NewMessage(chatID, b.t(lang, "category_no_products"))
			reply.ReplyMarkup = keyboard
			b.send(reply)
		}
		return
	}

	totalPages := (total + productsPerPage - 1) / productsPerPage
	wishlistIDs, err := b.wishlist.GetUserWishlistIDs(ctx, userID)
	if err != nil {
		b.logger.Warn("get wishlist ids for category", "user_id", userID, "error", err)
		wishlistIDs = map[int64]struct{}{}
	}

	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(products)+2)
	for _, p := range products {
		label := "🛍 " + p.Name
		if _, ok := wishlistIDs[p.ID]; ok {
			label = "❤️ " + p.Name
		}
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(label, fmt.Sprintf("product:%d", p.ID)),
		))
	}

	// Pagination row (only if more than one page).
	if totalPages > 1 {
		navRow := make([]tgbotapi.InlineKeyboardButton, 0, 3)
		if page > 0 {
			navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData(
				"◀️", fmt.Sprintf("category:%d:page:%d", catID, page-1),
			))
		}
		navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData(
			fmt.Sprintf("%d/%d", page+1, totalPages), "noop",
		))
		if page+1 < totalPages {
			navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData(
				"▶️", fmt.Sprintf("category:%d:page:%d", catID, page+1),
			))
		}
		rows = append(rows, navRow)
	}

	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_back"), "back:catalog"),
	))
	keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)

	text := b.formatCategoryProductsText(lang, category, products, page, totalPages, wishlistIDs)
	if msgID > 0 {
		edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
		edit.ParseMode = "HTML"
		edit.ReplyMarkup = &keyboard
		b.send(edit)
	} else {
		reply := tgbotapi.NewMessage(chatID, text)
		reply.ParseMode = "HTML"
		reply.ReplyMarkup = keyboard
		b.send(reply)
	}
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
	keyboard := b.productKeyboard(p, inWishlist, quantity, lang)

	// If the product has a photo, send it as a photo message; otherwise edit text.
	if p.PhotoURL != "" {
		// Delete the old message and send a photo.
		del := tgbotapi.NewDeleteMessage(chatID, msgID)
		b.api.Request(del)

		photo := tgbotapi.NewPhoto(chatID, tgbotapi.FileID(p.PhotoURL))
		photo.Caption = text
		photo.ParseMode = "HTML"
		photo.ReplyMarkup = keyboard
		b.send(photo)
	} else {
		edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
		edit.ParseMode = "HTML"
		edit.ReplyMarkup = &keyboard
		b.send(edit)
	}
}

func (b *Bot) onCartAdd(cbID string, chatID, userID int64, msgID int, data, lang string) {
	prodID, err := parseIDFromCallback(data, "cart:add:")
	if err != nil {
		b.logger.Error("parse cart:add callback", "error", err)
		b.ack(cbID)
		return
	}

	ctx := context.Background()
	if err := b.cart.Add(ctx, userID, prodID); err != nil {
		b.logger.Error("add to cart", "error", err)
		b.alert(cbID, b.t(lang, "error_add_cart"))
		return
	}

	b.alert(cbID, b.t(lang, "cart_item_added"))
	b.refreshProductKeyboard(chatID, userID, msgID, prodID, lang)
}

func (b *Bot) onProductQuantityChange(cbID string, chatID, userID int64, msgID int, data, prefix string, delta int, lang string) {
	prodID, err := parseIDFromCallback(data, prefix)
	if err != nil {
		b.logger.Error("parse product quantity callback", "prefix", prefix, "error", err)
		b.ack(cbID)
		return
	}

	ctx := context.Background()
	if err := b.cart.ChangeQuantity(ctx, userID, prodID, delta); err != nil {
		b.logger.Error("change quantity from product card", "product_id", prodID, "delta", delta, "error", err)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	}

	b.ack(cbID)
	b.refreshProductKeyboard(chatID, userID, msgID, prodID, lang)
}

func (b *Bot) onCartPlus(chatID, userID int64, msgID int, data, lang string) {
	prodID, err := parseIDFromCallback(data, "cart:plus:")
	if err != nil {
		b.logger.Error("parse cart:plus callback", "error", err)
		return
	}

	ctx := context.Background()
	if err := b.cart.ChangeQuantity(ctx, userID, prodID, 1); err != nil {
		b.logger.Error("cart plus", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "error_short")))
		return
	}
	b.sendCart(chatID, userID, msgID, lang)
}

func (b *Bot) onCartMinus(chatID, userID int64, msgID int, data, lang string) {
	prodID, err := parseIDFromCallback(data, "cart:minus:")
	if err != nil {
		b.logger.Error("parse cart:minus callback", "error", err)
		return
	}

	ctx := context.Background()
	view, err := b.cart.Get(ctx, userID)
	if err != nil {
		b.logger.Error("get cart for minus", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "error_short")))
		return
	}

	for _, item := range view.Items {
		if item.Product.ID == prodID {
			if item.Quantity <= 1 {
				if err := b.cart.Remove(ctx, userID, prodID); err != nil {
					b.logger.Error("cart remove on minus", "error", err)
					b.send(tgbotapi.NewMessage(chatID, b.t(lang, "error_short")))
					return
				}
			} else {
				if err := b.cart.ChangeQuantity(ctx, userID, prodID, -1); err != nil {
					b.logger.Error("cart minus", "error", err)
					b.send(tgbotapi.NewMessage(chatID, b.t(lang, "error_short")))
					return
				}
			}
			break
		}
	}
	b.sendCart(chatID, userID, msgID, lang)
}

func (b *Bot) onCartDel(chatID, userID int64, msgID int, data, lang string) {
	prodID, err := parseIDFromCallback(data, "cart:del:")
	if err != nil {
		b.logger.Error("parse cart:del callback", "error", err)
		return
	}

	ctx := context.Background()
	if err := b.cart.Remove(ctx, userID, prodID); err != nil {
		b.logger.Error("cart del", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "error_remove_cart")))
		return
	}
	b.sendCart(chatID, userID, msgID, lang)
}

func (b *Bot) onCartCheckout(chatID, userID int64, msgID int, lang string) {
	ctx := context.Background()
	view, err := b.cart.Get(ctx, userID)
	if err != nil {
		b.logger.Error("get cart for checkout", "error", err)
		return
	}

	if len(view.Items) == 0 {
		edit := tgbotapi.NewEditMessageText(chatID, msgID, b.t(lang, "cart_empty_text"))
		b.send(edit)
		return
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_enter_promo"), "promo:enter"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_confirm_order"), "order:confirm"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_back_to_cart"), "back:cart"),
		),
	)

	edit := tgbotapi.NewEditMessageText(chatID, msgID, b.formatCheckoutText(lang, view))
	edit.ParseMode = "HTML"
	edit.ReplyMarkup = &keyboard
	b.send(edit)
}

// onPromoEnter sets the user into promo-entry mode and asks for the code.
func (b *Bot) onPromoEnter(chatID, userID int64, lang string) {
	_ = b.fsm.SetPromoState(context.Background(), userID, time.Now(), 10*time.Hour)
	b.send(tgbotapi.NewMessage(chatID, b.t(lang, "promo_enter_prompt")))
}

// handlePromoInput processes a text message from a user who is in promo-entry mode.
func (b *Bot) handlePromoInput(msg *tgbotapi.Message) {
	userID := msg.From.ID
	chatID := msg.Chat.ID
	lang := msg.From.LanguageCode

	// Clear promo state immediately regardless of outcome.
	_ = b.fsm.DelPromoState(context.Background(), userID)

	code := strings.TrimSpace(strings.ToUpper(msg.Text))

	ctx := context.Background()
	promo, err := b.promos.GetPromoByCode(ctx, code)
	if err != nil {
		if err == storage.ErrNotFound {
			b.send(tgbotapi.NewMessage(chatID, b.t(lang, "promo_not_found")))
			return
		}
		b.logger.Error("get promo", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "error_promo_check")))
		return
	}

	// Check if user has already used this promo.
	used, err := b.promos.HasUserUsedPromo(ctx, promo.ID, userID)
	if err != nil {
		b.logger.Error("check promo usage", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "error_promo_check")))
		return
	}
	if used {
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "promo_already_used")))
		return
	}

	// Fetch cart to show updated totals.
	view, err := b.cart.Get(ctx, userID)
	if err != nil {
		b.logger.Error("get cart for promo", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "error_load_cart")))
		return
	}

	if len(view.Items) == 0 {
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "cart_empty")))
		return
	}

	// Check category restriction if promo has one.
	if promo.CategoryID != nil {
		hasMatch := false
		for _, item := range view.Items {
			if item.Product.CategoryID == *promo.CategoryID {
				hasMatch = true
				break
			}
		}
		if !hasMatch {
			b.send(tgbotapi.NewMessage(chatID, b.t(lang, "promo_category_mismatch")))
			return
		}
	}

	discountedUSD := view.TotalUSD * float64(100-promo.Discount) / 100
	discountedStars := view.TotalStars * (100 - promo.Discount) / 100

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(b.t(lang, "promo_applied_header"), promo.Code, promo.Discount))
	sb.WriteString(fmt.Sprintf(b.t(lang, "promo_original_total"), view.TotalUSD, view.TotalStars))
	sb.WriteString(fmt.Sprintf(b.t(lang, "promo_discounted_total"), discountedUSD, discountedStars))

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_confirm_with_promo"),
				fmt.Sprintf("order:confirm:promo:%s", promo.Code)),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_confirm_no_promo"), "order:confirm"),
		),
	)

	reply := tgbotapi.NewMessage(chatID, sb.String())
	reply.ReplyMarkup = keyboard
	b.send(reply)
}

func (b *Bot) onOrderConfirm(chatID, userID int64, msgID int, data, lang string) {
	// Extract optional promo code from callback data.
	var promoCode string
	if strings.HasPrefix(data, "order:confirm:promo:") {
		promoCode = strings.TrimPrefix(data, "order:confirm:promo:")
	}

	ctx := context.Background()
	view, err := b.cart.Get(ctx, userID)
	if err != nil {
		b.logger.Error("get cart for order confirm", "error", err)
		return
	}

	if len(view.Items) == 0 {
		edit := tgbotapi.NewEditMessageText(chatID, msgID, b.t(lang, "order_empty_cart"))
		b.send(edit)
		return
	}

	// Resolve promo if provided.
	var promo *storage.PromoCode
	if promoCode != "" {
		promo, err = b.promos.GetPromoByCode(ctx, promoCode)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				b.send(tgbotapi.NewMessage(chatID, b.t(lang, "promo_expired")))
				return
			}
			b.logger.Error("get promo for order confirm", "error", err)
			b.send(tgbotapi.NewMessage(chatID, b.t(lang, "error_promo_check")))
			return
		}

		used, err := b.promos.HasUserUsedPromo(ctx, promo.ID, userID)
		if err != nil {
			b.logger.Error("check promo usage for order confirm", "error", err)
			b.send(tgbotapi.NewMessage(chatID, b.t(lang, "error_promo_check")))
			return
		}
		if used {
			b.send(tgbotapi.NewMessage(chatID, b.t(lang, "promo_already_used")))
			return
		}

		userOrders, err := b.order.GetUserOrders(ctx, userID)
		if err != nil {
			b.logger.Error("get user orders for promo validation", "error", err)
			b.send(tgbotapi.NewMessage(chatID, b.t(lang, "error_promo_check")))
			return
		}
		if hasPendingOrderWithPromo(userOrders, promo.Code) {
			b.send(tgbotapi.NewMessage(chatID, b.t(lang, "promo_pending_order")))
			return
		}

		if promo.CategoryID != nil {
			hasMatch := false
			for _, item := range view.Items {
				if item.Product.CategoryID == *promo.CategoryID {
					hasMatch = true
					break
				}
			}
			if !hasMatch {
				b.send(tgbotapi.NewMessage(chatID, b.t(lang, "promo_category_mismatch")))
				return
			}
		}
	}

	orderID, err := b.order.CreateFromCart(ctx, userID, view, promo)
	if err != nil {
		b.logger.Error("create order", "error", err)
		edit := tgbotapi.NewEditMessageText(chatID, msgID, b.t(lang, "order_create_error"))
		b.send(edit)
		return
	}

	b.notifyAdmins(fmt.Sprintf(
		"🛍 Новый заказ #%d\nПользователь: %d\n💰 $%.2f / %d ⭐",
		orderID, userID, view.TotalUSD, view.TotalStars,
	))

	text := b.formatPaymentMethodsText(lang, orderID, view, b.cryptoPaymentsEnabled())
	keyboard := paymentMethodKeyboard(orderID, b.cryptoPaymentsEnabled(), lang, b)

	edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
	edit.ParseMode = "HTML"
	edit.ReplyMarkup = &keyboard
	b.send(edit)
}

func paymentMethodKeyboard(orderID int64, cryptoEnabled bool, lang string, b *Bot) tgbotapi.InlineKeyboardMarkup {
	starsLabel := "⭐ Telegram Stars"
	cryptoLabel := "💎 Crypto (USDT)"
	termsLabel := "📄 Terms"
	paySupportLabel := "🆘 Payment support"
	cancelLabel := "❌ Cancel order"
	if b != nil {
		starsLabel = b.t(lang, "btn_pay_stars")
		cryptoLabel = b.t(lang, "btn_pay_crypto")
		termsLabel = b.t(lang, "btn_terms")
		paySupportLabel = b.t(lang, "btn_paysupport")
		cancelLabel = b.t(lang, "btn_cancel_order")
	}
	rows := [][]tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(starsLabel, fmt.Sprintf("pay:stars:%d", orderID)),
		),
	}
	if cryptoEnabled {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(cryptoLabel, fmt.Sprintf("pay:crypto:%d", orderID)),
		))
	}
	ordersLabel := "📦 Мои заказы"
	if b != nil {
		ordersLabel = b.t(lang, "btn_orders")
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(termsLabel, "terms"),
		tgbotapi.NewInlineKeyboardButtonData(paySupportLabel, "paysupport"),
	))
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(cancelLabel, fmt.Sprintf("order:cancel:%d", orderID)),
		tgbotapi.NewInlineKeyboardButtonData(ordersLabel, "back:orders"),
	))
	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func ensureOrderPayableForUser(order *storage.Order, userID int64) error {
	if order == nil || order.UserID != userID {
		return storage.ErrNotFound
	}
	if order.Status != storage.OrderStatusPending {
		return storage.ErrOrderStatusConflict
	}
	return nil
}

func hasPendingOrderWithPromo(orders []storage.Order, promoCode string) bool {
	for _, order := range orders {
		if order.Status == storage.OrderStatusPending && order.PromoCode == promoCode {
			return true
		}
	}
	return false
}

func (b *Bot) loadPayableOrder(ctx context.Context, userID, orderID int64) (*storage.Order, error) {
	order, err := b.order.GetOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if err := ensureOrderPayableForUser(order, userID); err != nil {
		return nil, err
	}
	return order, nil
}

func (b *Bot) onPayStars(cbID string, chatID, userID int64, msgID int, data, lang string) {
	orderID, err := parseIDFromCallback(data, "pay:stars:")
	if err != nil {
		b.logger.Error("parse pay:stars callback", "error", err)
		b.ack(cbID)
		return
	}

	ctx := context.Background()
	target, err := b.loadPayableOrder(ctx, userID, orderID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			b.alert(cbID, b.t(lang, "order_not_found"))
			return
		}
		if errors.Is(err, storage.ErrOrderStatusConflict) {
			b.alert(cbID, b.t(lang, "order_already_paid"))
			return
		}
		b.logger.Error("load payable order for stars payment", "order_id", orderID, "error", err)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	}

	b.ack(cbID)
	if err := b.stars.SendInvoice(chatID, orderID, target.TotalStars, target.Items); err != nil {
		b.logger.Error("send stars invoice", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "payment_error")))
		return
	}
}

func (b *Bot) onOrderCancel(cbID string, chatID, userID int64, msgID int, data, lang string) {
	orderID, err := parseIDFromCallback(data, "order:cancel:")
	if err != nil {
		b.logger.Error("parse order:cancel callback", "error", err)
		b.ack(cbID)
		return
	}

	ctx := context.Background()
	if _, err := b.loadPayableOrder(ctx, userID, orderID); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			b.alert(cbID, b.t(lang, "order_not_found"))
			return
		}
		if errors.Is(err, storage.ErrOrderStatusConflict) {
			b.alert(cbID, b.t(lang, "order_already_paid"))
			return
		}
		b.logger.Error("load payable order for cancel", "order_id", orderID, "error", err)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	}

	if err := b.order.CancelOrder(ctx, orderID, userID); err != nil {
		b.logger.Error("cancel order", "order_id", orderID, "user_id", userID, "error", err)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	}

	b.ack(cbID)
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_catalog"), "back:catalog"),
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_orders"), "back:orders"),
		),
	)

	text := fmt.Sprintf(b.t(lang, "order_cancelled"), orderID)
	if msgID > 0 {
		edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
		edit.ParseMode = "HTML"
		edit.ReplyMarkup = &keyboard
		b.send(edit)
		return
	}

	reply := tgbotapi.NewMessage(chatID, text)
	reply.ParseMode = "HTML"
	reply.ReplyMarkup = keyboard
	b.send(reply)
}

func (b *Bot) onPayCrypto(cbID string, chatID, userID int64, msgID int, data, lang string) {
	if !b.cryptoPaymentsEnabled() {
		b.alert(cbID, b.t(lang, "crypto_unavailable"))
		return
	}

	orderID, err := parseIDFromCallback(data, "pay:crypto:")
	if err != nil {
		b.logger.Error("parse pay:crypto callback", "error", err)
		b.ack(cbID)
		return
	}

	ctx := context.Background()
	target, err := b.loadPayableOrder(ctx, userID, orderID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			b.alert(cbID, b.t(lang, "order_not_found"))
			return
		}
		if errors.Is(err, storage.ErrOrderStatusConflict) {
			b.alert(cbID, b.t(lang, "order_already_paid"))
			return
		}
		b.logger.Error("load payable order for crypto payment", "order_id", orderID, "error", err)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	}

	// Show skeleton state while generating the invoice.
	skeletonKeyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_generating_invoice"), "noop"),
		),
	)
	editSkeleton := tgbotapi.NewEditMessageReplyMarkup(chatID, msgID, skeletonKeyboard)
	b.send(editSkeleton)
	b.ack(cbID)

	desc := fmt.Sprintf(b.t(lang, "crypto_invoice_desc"), orderID)
	invoice, err := b.crypto.CreateInvoice(ctx, orderID, target.TotalUSD, desc)
	if err != nil {
		b.logger.Error("create crypto invoice", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "payment_error")))
		return
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonURL(b.t(lang, "btn_pay_usdt"), invoice.PayURL),
		),
	)

	text := fmt.Sprintf(b.t(lang, "crypto_pay_title"), orderID, target.TotalUSD)
	reply := tgbotapi.NewMessage(chatID, text)
	reply.ParseMode = "HTML"
	reply.ReplyMarkup = keyboard
	b.send(reply)
}

func (b *Bot) onBack(chatID, userID int64, msgID int, data, lang string) {
	target := strings.TrimPrefix(data, "back:")

	switch {
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

// handlePreCheckout handles Telegram PreCheckoutQuery for Stars payments.
func (b *Bot) handlePreCheckout(query *tgbotapi.PreCheckoutQuery) {
	if err := b.stars.HandlePreCheckout(query); err != nil {
		b.logger.Error("handle pre-checkout", "error", err)
	}
}

// handleSuccessfulPayment handles a successful Stars payment, updating the order status.
func (b *Bot) handleSuccessfulPayment(msg *tgbotapi.Message) {
	sp := msg.SuccessfulPayment
	if sp == nil {
		return
	}

	orderID, err := strconv.ParseInt(sp.InvoicePayload, 10, 64)
	if err != nil {
		b.logger.Error("parse order ID from successful payment", "error", err)
		return
	}

	ctx := context.Background()
	if err := b.order.ConfirmPayment(ctx, orderID, "stars", sp.TelegramPaymentChargeID); err != nil {
		if errors.Is(err, storage.ErrOrderStatusConflict) {
			// Duplicate Stars payment event — already confirmed, safe to ignore.
			b.logger.Info("stars payment already confirmed (idempotent)", "order_id", orderID)
			return
		}
		b.logger.Error("confirm stars payment", "order_id", orderID, "error", err)
		return
	}

	if b.metrics != nil {
		b.metrics.SuccessfulPayments.WithLabelValues("stars").Inc()
	}

	b.notifyAdmins(fmt.Sprintf("⭐ Оплачен заказ #%d (Stars) — пользователь %d",
		orderID, msg.From.ID))

	receipt := fmt.Sprintf(
		"<code>✅ ОПЛАЧЕНО\n---\nID: #%d\nИТОГО: %d Stars\nДАТА: %s\n---\nСпасибо за покупку!</code>",
		orderID,
		sp.TotalAmount,
		time.Now().Format("02.01.2006"),
	)
	reply := tgbotapi.NewMessage(msg.Chat.ID, receipt)
	reply.ParseMode = "HTML"
	b.send(reply)
}

// handleSearch searches for in-stock products matching the query.
func (b *Bot) handleSearch(msg *tgbotapi.Message) {
	lang := msg.From.LanguageCode
	query := strings.TrimSpace(msg.CommandArguments())
	if len([]rune(query)) < 2 {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "search_too_short")))
		return
	}

	ctx := context.Background()
	products, err := b.products.SearchProducts(ctx, query)
	if err != nil {
		b.logger.Error("search products", "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "error_short")))
		return
	}

	if len(products) == 0 {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf(b.t(lang, "search_not_found"), query)))
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(b.t(lang, "search_results_title"), query))
	for _, p := range products {
		sb.WriteString(fmt.Sprintf("• [ID %d] %s — $%.2f / %d ⭐\n", p.ID, p.Name, p.PriceUSD, p.PriceStars))
	}

	b.send(tgbotapi.NewMessage(msg.Chat.ID, sb.String()))
}

// send is a convenience wrapper that logs errors from the Telegram API.
func (b *Bot) send(c tgbotapi.Chattable) {
	if _, err := b.api.Send(c); err != nil {
		b.logger.Error("send message", "error", err)
	}
}

// productKeyboard builds the inline keyboard for a product detail view.
func (b *Bot) productKeyboard(p *storage.Product, inWishlist bool, quantity int, lang string) tgbotapi.InlineKeyboardMarkup {
	wishBtnLabel := "♥ " + b.t(lang, "btn_wishlist_add")
	if inWishlist {
		wishBtnLabel = "💔 " + b.t(lang, "btn_wishlist_remove")
	}
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("➖", fmt.Sprintf("productqty:minus:%d", p.ID)),
			tgbotapi.NewInlineKeyboardButtonData(b.productQuantityLabel(lang, quantity), "noop"),
			tgbotapi.NewInlineKeyboardButtonData("➕", fmt.Sprintf("productqty:plus:%d", p.ID)),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_add_to_cart"), fmt.Sprintf("cart:add:%d", p.ID)),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(wishBtnLabel, fmt.Sprintf("wish:%d", p.ID)),
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_back"), fmt.Sprintf("back:category:%d", p.CategoryID)),
		),
	)
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

	keyboard := b.productKeyboard(p, inWishlist, quantity, lang)
	edit := tgbotapi.NewEditMessageReplyMarkup(chatID, msgID, keyboard)
	b.send(edit)
}

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
		b.alert(cbID, b.t(lang, "wishlist_removed"))
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
		b.alert(cbID, b.t(lang, "wishlist_added"))
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

// onSupport shows support information as an inline message edit.
func (b *Bot) onSupport(chatID int64, msgID int, lang string) {
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_back"), "back:catalog"),
		),
	)
	if msgID > 0 {
		edit := tgbotapi.NewEditMessageText(chatID, msgID, b.t(lang, "support_welcome"))
		edit.ParseMode = "HTML"
		edit.ReplyMarkup = &keyboard
		b.send(edit)
	} else {
		reply := tgbotapi.NewMessage(chatID, b.t(lang, "support_welcome"))
		reply.ParseMode = "HTML"
		reply.ReplyMarkup = keyboard
		b.send(reply)
	}
}

func (b *Bot) onPaySupport(chatID int64, msgID int, lang string) {
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_terms"), "terms"),
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_back"), "back:orders"),
		),
	)
	if msgID > 0 {
		edit := tgbotapi.NewEditMessageText(chatID, msgID, b.t(lang, "paysupport_welcome"))
		edit.ParseMode = "HTML"
		edit.ReplyMarkup = &keyboard
		b.send(edit)
	} else {
		reply := tgbotapi.NewMessage(chatID, b.t(lang, "paysupport_welcome"))
		reply.ParseMode = "HTML"
		reply.ReplyMarkup = keyboard
		b.send(reply)
	}
}

func (b *Bot) onTerms(chatID int64, msgID int, lang string) {
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_paysupport"), "paysupport"),
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_back"), "back:catalog"),
		),
	)
	if msgID > 0 {
		edit := tgbotapi.NewEditMessageText(chatID, msgID, b.t(lang, "terms_text"))
		edit.ParseMode = "HTML"
		edit.ReplyMarkup = &keyboard
		b.send(edit)
	} else {
		reply := tgbotapi.NewMessage(chatID, b.t(lang, "terms_text"))
		reply.ParseMode = "HTML"
		reply.ReplyMarkup = keyboard
		b.send(reply)
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
