package bot

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/service"
	"shop_bot/internal/storage"
)

func (b *Bot) isAdmin(userID int64) bool {
	_, ok := b.adminMap[userID]
	return ok
}

func (b *Bot) handleAdmin(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}

	lang := msg.From.LanguageCode
	b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_help_text")))
}

func (b *Bot) handleAddProduct(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	ctx := context.Background()
	lang := msg.From.LanguageCode
	_ = b.fsm.SetAddProductState(ctx, msg.From.ID, &storage.AddProductState{Step: storage.StepName, CreatedAt: time.Now()}, 30*time.Minute)
	b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_prompt_product_name")))
}

func (b *Bot) handleAddProductStep(msg *tgbotapi.Message) bool {
	ctx := context.Background()
	lang := msg.From.LanguageCode
	if msg.Text == "/cancel" {
		state, _ := b.fsm.GetAddProductState(ctx, msg.From.ID)
		_ = b.fsm.DelAddProductState(ctx, msg.From.ID)
		if state != nil {
			b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "cancel_add_product")))
			return true
		}
		return false
	}

	state, _ := b.fsm.GetAddProductState(ctx, msg.From.ID)
	if state == nil {
		return false
	}

	chatID := msg.Chat.ID
	switch state.Step {
	case storage.StepName:
		state.Name = msg.Text
		state.Step = storage.StepDescription
		_ = b.fsm.SetAddProductState(ctx, msg.From.ID, state, 30*time.Minute)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_prompt_product_description")))
	case storage.StepDescription:
		state.Description = msg.Text
		state.Step = storage.StepPriceUSD
		_ = b.fsm.SetAddProductState(ctx, msg.From.ID, state, 30*time.Minute)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_prompt_product_price")))
	case storage.StepPriceUSD:
		p, _ := strconv.ParseFloat(msg.Text, 64)
		state.PriceUSD = p
		state.Step = storage.StepStock
		_ = b.fsm.SetAddProductState(ctx, msg.From.ID, state, 30*time.Minute)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_prompt_product_stock")))
	case storage.StepStock:
		s, _ := strconv.Atoi(msg.Text)
		state.Stock = s
		state.Step = storage.StepPhoto
		_ = b.fsm.SetAddProductState(ctx, msg.From.ID, state, 30*time.Minute)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_prompt_product_photo")))
	case storage.StepPhoto:
		if msg.Photo != nil {
			state.PhotoURL = msg.Photo[len(msg.Photo)-1].FileID
		}
		state.Step = storage.StepCategory
		_ = b.fsm.SetAddProductState(ctx, msg.From.ID, state, 30*time.Minute)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_prompt_product_category")))
	case storage.StepCategory:
		id, _ := strconv.ParseInt(msg.Text, 10, 64)
		b.finishAddProduct(chatID, msg.From.ID, id, lang)
	}
	return true
}

func (b *Bot) finishAddProduct(chatID, userID, categoryID int64, lang string) {
	ctx := context.Background()
	state, _ := b.fsm.GetAddProductState(ctx, userID)
	_ = b.fsm.DelAddProductState(ctx, userID)
	if state == nil {
		return
	}
	p := &storage.Product{CategoryID: categoryID, Name: state.Name, Description: state.Description, PriceUSD: state.PriceUSD, Stock: state.Stock, PhotoURL: state.PhotoURL, IsActive: true}
	_, _ = b.products.CreateProduct(ctx, p)
	b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_product_created")))
}

func (b *Bot) sendAdminProductDetails(chatID int64, product *storage.Product, lang string) {
	toggleLabel := b.t(lang, "admin_btn_toggle_stock_off")
	if product.Stock <= 0 {
		toggleLabel = b.t(lang, "admin_btn_toggle_stock_on")
	}

	text := fmt.Sprintf(
		b.t(lang, "admin_product_details"),
		product.ID, product.Name, product.Description, product.PriceUSD, product.Stock, product.CategoryID, product.IsActive,
		product.ID, product.ID, product.ID, product.ID, product.ID, product.ID,
	)

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(toggleLabel, fmt.Sprintf("admin:togglestock:%d", product.ID)),
		),
	)

	reply := tgbotapi.NewMessage(chatID, text)
	reply.ReplyMarkup = keyboard
	b.send(reply)
}

func (b *Bot) handleEditProduct(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	lang := msg.From.LanguageCode
	id, err := strconv.ParseInt(strings.TrimSpace(msg.CommandArguments()), 10, 64)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_usage_edit_product")))
		return
	}

	product, err := b.products.GetProduct(context.Background(), id)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_error_product_not_found")))
		return
	}

	b.sendAdminProductDetails(msg.Chat.ID, product, lang)
}

func (b *Bot) handleEditProductField(msg *tgbotapi.Message, prodID int64, field, value string) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	lang := msg.From.LanguageCode
	if strings.TrimSpace(value) == "" {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_error_provide_value")))
		return
	}

	ctx := context.Background()
	product, err := b.products.GetProduct(ctx, prodID)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_error_product_not_found")))
		return
	}

	switch strings.ToLower(field) {
	case "name":
		product.Name = value
	case "description":
		product.Description = value
	case "price":
		price, err := strconv.ParseFloat(value, 64)
		if err != nil {
			b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_error_invalid_price")))
			return
		}
		product.PriceUSD = price
	case "stock":
		stock, err := strconv.Atoi(value)
		if err != nil {
			b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_error_invalid_stock")))
			return
		}
		product.Stock = stock
	case "category":
		categoryID, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_error_invalid_category_id")))
			return
		}
		product.CategoryID = categoryID
	case "active":
		active, err := strconv.ParseBool(value)
		if err != nil {
			b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_error_true_false")))
			return
		}
		product.IsActive = active
	default:
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_error_update_product_fields")))
		return
	}

	if err := b.products.UpdateProduct(ctx, product); err != nil {
		b.logger.Error("update product", "product_id", prodID, "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_error_update_product_failed")))
		return
	}

	b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_product_updated")))
}

func (b *Bot) handleDeleteProduct(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	lang := msg.From.LanguageCode
	id, err := strconv.ParseInt(strings.TrimSpace(msg.CommandArguments()), 10, 64)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_usage_delete_product")))
		return
	}
	if err := b.products.DeleteProduct(context.Background(), id); err != nil {
		b.logger.Error("delete product", "product_id", id, "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_error_delete_product_failed")))
		return
	}
	b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_product_deleted")))
}

func (b *Bot) handleAddCategory(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	lang := msg.From.LanguageCode
	args := strings.Fields(msg.CommandArguments())
	if len(args) < 2 {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_usage_add_category")))
		return
	}
	cat := &storage.Category{
		Emoji:    args[0],
		Name:     strings.Join(args[1:], " "),
		IsActive: true,
	}
	id, err := b.catalog.CreateCategory(context.Background(), cat)
	if err != nil {
		b.logger.Error("create category", "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_error_add_category_failed")))
		return
	}
	b.send(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf(b.t(lang, "admin_category_created"), id, cat.Emoji, cat.Name)))
}

func (b *Bot) handleEditCategory(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	lang := msg.From.LanguageCode
	args := strings.Fields(msg.CommandArguments())
	if len(args) < 3 {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_usage_edit_category")))
		return
	}

	categoryID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_error_invalid_category_id")))
		return
	}

	ctx := context.Background()
	category, err := b.catalog.GetCategory(ctx, categoryID)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_error_category_not_found")))
		return
	}

	value := strings.Join(args[2:], " ")
	switch strings.ToLower(args[1]) {
	case "name":
		category.Name = value
	case "emoji":
		category.Emoji = value
	default:
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_error_edit_category_fields")))
		return
	}

	if err := b.catalog.UpdateCategory(ctx, category); err != nil {
		b.logger.Error("update category", "category_id", categoryID, "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_error_edit_category_failed")))
		return
	}

	b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_category_updated")))
}

func (b *Bot) handleDeleteCategory(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	lang := msg.From.LanguageCode
	id, err := strconv.ParseInt(strings.TrimSpace(msg.CommandArguments()), 10, 64)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_usage_delete_category")))
		return
	}
	if err := b.catalog.DeleteCategory(context.Background(), id); err != nil {
		b.logger.Error("delete category", "category_id", id, "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_error_delete_category_failed")))
		return
	}
	b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_category_deleted")))
}

func (b *Bot) handleListCategories(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	lang := msg.From.LanguageCode
	categories, err := b.catalog.ListCategories(context.Background())
	if err != nil {
		b.logger.Error("list categories", "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_error_load_categories_failed")))
		return
	}
	if len(categories) == 0 {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_categories_not_found")))
		return
	}

	var sb strings.Builder
	sb.WriteString(b.t(lang, "admin_categories_list_title"))
	for _, category := range categories {
		sb.WriteString(fmt.Sprintf("%d: %s %s\n", category.ID, category.Emoji, category.Name))
	}
	b.send(tgbotapi.NewMessage(msg.Chat.ID, sb.String()))
}

func (b *Bot) handleOrdersAll(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	lang := msg.From.LanguageCode
	statusFilter := strings.TrimSpace(msg.CommandArguments())
	orders, err := b.order.GetAllOrders(context.Background(), statusFilter)
	if err != nil {
		b.logger.Error("get all orders", "status", statusFilter, "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_error_load_orders_failed")))
		return
	}
	if len(orders) == 0 {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_orders_not_found")))
		return
	}

	var sb strings.Builder
	sb.WriteString(b.t(lang, "admin_orders_list_title"))
	for _, order := range orders {
		status := b.t(lang, "order_status_"+order.Status)
		if status == "" {
			status = order.Status
		}
		sb.WriteString(fmt.Sprintf("#%d | user %d | $%.2f / %d ⭐ | %s\n",
			order.ID, order.UserID, order.TotalUSD, order.TotalStars, status))
	}
	b.send(tgbotapi.NewMessage(msg.Chat.ID, sb.String()))
}

func (b *Bot) handleSetDelivered(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	lang := msg.From.LanguageCode
	id, err := strconv.ParseInt(strings.TrimSpace(msg.CommandArguments()), 10, 64)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_usage_set_delivered")))
		return
	}
	order, err := b.order.SetDelivered(context.Background(), id)
	if err != nil {
		b.logger.Error("set delivered", "order_id", id, "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_error_set_delivered_failed")))
		return
	}
	b.send(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf(b.t(lang, "admin_order_marked_delivered"), order.ID)))

	b.outWebhook.Send(service.OutboundWebhookEvent{
		Event:      "order.delivered",
		OrderID:    order.ID,
		UserID:     order.UserID,
		TotalUSD:   order.TotalUSD,
		TotalStars: order.TotalStars,
	})
}

func (b *Bot) handleAnalytics(msg *tgbotapi.Message) { b.sendAnalytics(msg.Chat.ID, 0, 7, msg.From.LanguageCode) }

func (b *Bot) handleAnalyticsCallback(chatID int64, msgID int, data string, lang string) {
	days := 7
	if strings.HasPrefix(data, "analytics:") {
		if parsed, err := strconv.Atoi(strings.TrimPrefix(data, "analytics:")); err == nil && parsed > 0 {
			days = parsed
		}
	}
	b.sendAnalytics(chatID, msgID, days, lang)
}

func (b *Bot) sendAnalytics(chatID int64, msgID int, days int, lang string) {
	ctx := context.Background()

	summary, err := b.analytics.GetRevenueSummary(ctx)
	if err != nil {
		b.logger.Error("analytics summary", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_error_load_analytics_failed")))
		return
	}
	revenueByDays, err := b.analytics.GetRevenueByDays(ctx, days)
	if err != nil {
		b.logger.Error("analytics revenue by days", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_error_load_analytics_failed")))
		return
	}
	topProducts, err := b.analytics.GetTopProducts(ctx, 5)
	if err != nil {
		b.logger.Error("analytics top products", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_error_load_analytics_failed")))
		return
	}
	paymentStats, err := b.analytics.GetPaymentMethodStats(ctx)
	if err != nil {
		b.logger.Error("analytics payment stats", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_error_load_analytics_failed")))
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(b.t(lang, "admin_analytics_title"), days))
	sb.WriteString(fmt.Sprintf(b.t(lang, "admin_analytics_total_orders"), summary.TotalOrders))
	sb.WriteString(fmt.Sprintf(b.t(lang, "admin_analytics_paid_orders"), summary.PaidOrders))

	var periodUSD float64
	var periodStars int
	var periodOrders int
	for _, day := range revenueByDays {
		periodUSD += day.TotalUSD
		periodStars += day.TotalStars
		periodOrders += day.OrderCount
	}
	sb.WriteString(fmt.Sprintf(b.t(lang, "admin_analytics_period_revenue"), periodUSD, periodStars, periodOrders))

	if len(revenueByDays) > 0 {
		sb.WriteString(b.t(lang, "admin_analytics_by_days"))
		for _, day := range revenueByDays {
			sb.WriteString(fmt.Sprintf(b.t(lang, "admin_analytics_date_line"), day.Date, day.TotalUSD, day.TotalStars, day.OrderCount))
		}
		sb.WriteString("\n")
	}

	sb.WriteString(b.t(lang, "admin_analytics_top_products"))
	if len(topProducts) == 0 {
		sb.WriteString(b.t(lang, "admin_analytics_no_paid_orders"))
	} else {
		for _, product := range topProducts {
			sb.WriteString(fmt.Sprintf(b.t(lang, "admin_analytics_product_line"), product.Name, product.TotalSold, product.TotalRevenue))
		}
	}

	sb.WriteString(b.t(lang, "admin_analytics_payments_by_method"))
	if len(paymentStats) == 0 {
		sb.WriteString(b.t(lang, "admin_analytics_no_paid_orders"))
	} else {
		for _, stat := range paymentStats {
			sb.WriteString(fmt.Sprintf(b.t(lang, "admin_analytics_payment_stat_line"), stat.Method, stat.OrderCount, stat.TotalUSD))
		}
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "admin_btn_analytics_7d"), "analytics:7"),
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "admin_btn_analytics_30d"), "analytics:30"),
		),
	)

	if msgID > 0 {
		edit := tgbotapi.NewEditMessageText(chatID, msgID, sb.String())
		edit.ReplyMarkup = &keyboard
		b.send(edit)
		return
	}

	reply := tgbotapi.NewMessage(chatID, sb.String())
	reply.ReplyMarkup = keyboard
	b.send(reply)
}
func (b *Bot) handleAddPromo(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	lang := msg.From.LanguageCode
	args := strings.Fields(msg.CommandArguments())
	if len(args) < 2 {
		return
	}
	discount, _ := strconv.Atoi(args[1])
	p := &storage.PromoCode{Code: strings.ToUpper(args[0]), Discount: discount, IsActive: true}
	_, _ = b.promos.CreatePromo(context.Background(), p)
	b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_promo_created")))
}

func (b *Bot) handleListPromos(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	promos, _ := b.promos.ListPromos(context.Background())
	var sb strings.Builder
	for _, p := range promos {
		sb.WriteString(fmt.Sprintf("%d: %s (-%d%%)\n", p.ID, p.Code, p.Discount))
	}
	b.send(tgbotapi.NewMessage(msg.Chat.ID, sb.String()))
}

func (b *Bot) handleDeletePromo(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	lang := msg.From.LanguageCode
	id, _ := strconv.ParseInt(msg.CommandArguments(), 10, 64)
	_ = b.promos.DeactivatePromo(context.Background(), id)
	b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_promo_deactivated")))
}

func (b *Bot) handleExportOrders(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	lang := msg.From.LanguageCode

	orders, err := b.order.GetAllOrders(context.Background(), "")
	if err != nil {
		b.logger.Error("export orders", "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_error_export_orders_failed")))
		return
	}

	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	_ = writer.Write([]string{
		"order_id",
		"user_id",
		"status",
		"total_usd",
		"total_stars",
		"payment_method",
		"payment_id",
		"discount_pct",
		"promo_code",
		"created_at",
	})

	for _, order := range orders {
		_ = writer.Write([]string{
			strconv.FormatInt(order.ID, 10),
			strconv.FormatInt(order.UserID, 10),
			order.Status,
			fmt.Sprintf("%.2f", order.TotalUSD),
			strconv.Itoa(order.TotalStars),
			order.PaymentMethod,
			order.PaymentID,
			strconv.Itoa(order.DiscountPct),
			order.PromoCode,
			order.CreatedAt.Format(time.RFC3339),
		})
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		b.logger.Error("flush order export csv", "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_error_export_csv_failed")))
		return
	}

	doc := tgbotapi.NewDocument(msg.Chat.ID, tgbotapi.FileBytes{
		Name:  fmt.Sprintf("orders_%s.csv", time.Now().Format("20060102_150405")),
		Bytes: buf.Bytes(),
	})
	doc.Caption = fmt.Sprintf(b.t(lang, "admin_export_orders_caption"), len(orders))
	b.send(doc)
}

func (b *Bot) onAdminToggleStock(chatID int64, data string, lang string) {
	productID, err := parseIDFromCallback(data, "admin:togglestock:")
	if err != nil {
		b.logger.Error("parse admin:togglestock callback", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_error_identify_product_failed")))
		return
	}

	ctx := context.Background()
	product, err := b.products.GetProduct(ctx, productID)
	if err != nil {
		b.logger.Error("get product for stock toggle", "product_id", productID, "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_error_product_not_found")))
		return
	}

	if product.Stock > 0 {
		product.Stock = 0
	} else {
		product.Stock = 1
		product.IsActive = true
	}

	if err := b.products.UpdateProduct(ctx, product); err != nil {
		b.logger.Error("toggle product stock", "product_id", productID, "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_error_update_product_failed")))
		return
	}

	b.sendAdminProductDetails(chatID, product, lang)
}

func (b *Bot) routeEditProduct(msg *tgbotapi.Message) {
	args := strings.Fields(msg.CommandArguments())
	if len(args) == 0 {
		return
	}
	id, _ := strconv.ParseInt(args[0], 10, 64)
	if len(args) == 1 {
		b.handleEditProduct(msg)
	} else {
		b.handleEditProductField(msg, id, args[1], strings.Join(args[2:], " "))
	}
}

// handleBtnStyleAdmin handles the /btnstyle command.
func (b *Bot) handleBtnStyleAdmin(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	b.sendBtnStyleList(msg.Chat.ID, 0, msg.From.LanguageCode)
}

// sendBtnStyleList renders (or edits) the button style overview for the admin.
// Each row shows the button label and its current style emoji, tapping opens the picker.
func (b *Bot) sendBtnStyleList(chatID int64, msgID int, lang string) {
	ctx := context.Background()
	stored, _ := b.uiSettings.ListButtonStyles(ctx)

	var sb strings.Builder
	sb.WriteString(b.t(lang, "admin_btn_style_title"))
	sb.WriteString(b.t(lang, "admin_btn_style_hint"))
	for _, key := range AllButtonKeys {
		style := ButtonStyle(stored[key])
		sb.WriteString(fmt.Sprintf("%s %s → %s\n", StyleEmoji(style), ButtonKeyLabel(key), styleLabel(style)))
	}

	// Build inline keyboard: 2 buttons per row (label + current style indicator).
	var rows [][]StyledButton
	for i := 0; i < len(AllButtonKeys); i += 2 {
		var row []StyledButton
		for j := i; j < i+2 && j < len(AllButtonKeys); j++ {
			key := AllButtonKeys[j]
			style := ButtonStyle(stored[key])
			label := fmt.Sprintf("%s %s", StyleEmoji(style), ButtonKeyLabel(key))
			row = append(row, Btn(label, "admin:btnpick:"+key))
		}
		rows = append(rows, row)
	}

	kb := StyledKeyboard(rows)
	b.sendOrEditStyled(chatID, msgID, sb.String(), "HTML", kb)
}

// sendBtnStylePicker renders (or edits) the style picker for a single button key.
func (b *Bot) sendBtnStylePicker(chatID int64, msgID int, key string, lang string) {
	ctx := context.Background()
	current, _ := b.uiSettings.GetButtonStyle(ctx, key)

	text := fmt.Sprintf(
		b.t(lang, "admin_btn_style_picker_title"),
		ButtonKeyLabel(key),
		StyleEmoji(ButtonStyle(current)),
		styleLabel(ButtonStyle(current)),
	)

	styleOptions := []struct {
		label string
		style ButtonStyle
	}{
		{b.t(lang, "admin_btn_style_primary"), StylePrimary},
		{b.t(lang, "admin_btn_style_success"), StyleSuccess},
		{b.t(lang, "admin_btn_style_danger"), StyleDanger},
		{b.t(lang, "admin_btn_style_default"), StyleDefault},
	}

	var styleRow []StyledButton
	for _, opt := range styleOptions {
		styleRow = append(styleRow, Btn(opt.label, fmt.Sprintf("admin:setstyle:%s:%s", key, string(opt.style))))
	}

	kb := StyledKeyboard{
		styleRow,
		{Btn(b.t(lang, "admin_btn_back_to_list"), "admin:btnlist")},
	}
	b.sendOrEditStyled(chatID, msgID, text, "HTML", kb)
}

// onAdminSetStyle persists the style choice and returns to the overview.
func (b *Bot) onAdminSetStyle(chatID int64, msgID int, data string, lang string) {
	// data format: "admin:setstyle:<key>:<style>"
	rest := strings.TrimPrefix(data, "admin:setstyle:")
	sep := strings.LastIndex(rest, ":")
	if sep < 0 {
		return
	}
	key := rest[:sep]
	style := rest[sep+1:]

	ctx := context.Background()
	if err := b.uiSettings.SetButtonStyle(ctx, key, style); err != nil {
		b.logger.Error("set button style", "key", key, "style", style, "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_error_save_style_failed")))
		return
	}
	// Invalidate cache entry and reload.
	b.uiStyles.Store(key, style)

	b.sendBtnStyleList(chatID, msgID, lang)
}

func styleLabel(s ButtonStyle) string {
	switch s {
	case StylePrimary:
		return "primary"
	case StyleSuccess:
		return "success"
	case StyleDanger:
		return "danger"
	default:
		return "default"
	}
}
