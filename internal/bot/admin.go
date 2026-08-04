package bot

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/service"
	"shop_bot/internal/storage"
)

func (b *Bot) isAdmin(userID int64) bool {
	for _, id := range b.cfg.AdminIDs {
		if id == userID {
			return true
		}
	}
	return false
}

func (b *Bot) handleAdmin(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}

	text := b.t(msg.From.LanguageCode, "admin_panel")

	b.send(tgbotapi.NewMessage(msg.Chat.ID, text))
}

func (b *Bot) handleAddProduct(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	ctx := context.Background()
	_ = b.fsm.SetAddProductState(ctx, msg.From.ID, &storage.AddProductState{Step: storage.StepName, CreatedAt: time.Now()}, 30*time.Minute)
	b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(msg.From.LanguageCode, "admin_add_product_name")))
}

func (b *Bot) handleAddProductStep(msg *tgbotapi.Message) bool {
	ctx := context.Background()
	if msg.Text == "/cancel" {
		state, _ := b.fsm.GetAddProductState(ctx, msg.From.ID)
		_ = b.fsm.DelAddProductState(ctx, msg.From.ID)
		if state != nil {
			b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(msg.From.LanguageCode, "admin_cancelled")))
			return true
		}
		return false
	}

	state, _ := b.fsm.GetAddProductState(ctx, msg.From.ID)
	if state == nil {
		return false
	}

	chatID := msg.Chat.ID
	lang := msg.From.LanguageCode
	switch state.Step {
	case storage.StepName:
		state.Name = msg.Text
		state.Step = storage.StepDescription
		_ = b.fsm.SetAddProductState(ctx, msg.From.ID, state, 30*time.Minute)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_add_product_description")))
	case storage.StepDescription:
		state.Description = msg.Text
		state.Step = storage.StepPriceUSD
		_ = b.fsm.SetAddProductState(ctx, msg.From.ID, state, 30*time.Minute)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_add_product_price")))
	case storage.StepPriceUSD:
		p, _ := strconv.ParseFloat(msg.Text, 64)
		state.PriceUSD = p
		state.Step = storage.StepStock
		_ = b.fsm.SetAddProductState(ctx, msg.From.ID, state, 30*time.Minute)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_add_product_stock")))
	case storage.StepStock:
		s, _ := strconv.Atoi(msg.Text)
		state.Stock = s
		state.Step = storage.StepPhoto
		_ = b.fsm.SetAddProductState(ctx, msg.From.ID, state, 30*time.Minute)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_photo_prompt")))
	case storage.StepPhoto:
		b.handleWizardPhotoStep(ctx, msg, state)
	case storage.StepCategory:
		id, _ := strconv.ParseInt(msg.Text, 10, 64)
		state.CategoryID = id
		state.Step = storage.StepSubType
		_ = b.fsm.SetAddProductState(ctx, msg.From.ID, state, 30*time.Minute)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_sub_type_prompt")))
	case storage.StepSubType:
		// "2" = 30-day Stars subscription, anything else = regular product.
		if strings.TrimSpace(msg.Text) == "2" {
			state.SubPeriodDays = 30
		}
		_ = b.fsm.SetAddProductState(ctx, msg.From.ID, state, 30*time.Minute)
		b.finishAddProduct(chatID, msg.From.ID, state.CategoryID, lang)
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
	cover := ""
	if len(state.Photos) > 0 {
		cover = state.Photos[0]
	}
	p := &storage.Product{CategoryID: categoryID, Name: state.Name, Description: state.Description, PriceUSD: state.PriceUSD, Stock: state.Stock, PhotoURL: cover, IsActive: true, SubPeriodDays: state.SubPeriodDays}
	id, err := b.products.CreateProduct(ctx, p)
	if err != nil {
		b.logger.Error("create product", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_product_create_failed")))
		return
	}
	for _, fileID := range state.Photos {
		if err := b.photos.Add(ctx, id, fileID); err != nil {
			b.logger.Error("add product photo", "product_id", id, "error", err)
		}
	}
	b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_product_created")))
}

// handleWizardPhotoStep processes StepPhoto: a photo message (largest
// PhotoSize wins) or a URL adds an image, up to storage.MaxProductPhotos;
// /done (or /skip) finishes the step. When state.EditProductID is set the
// photo is persisted directly on that product instead of the wizard state.
func (b *Bot) handleWizardPhotoStep(ctx context.Context, msg *tgbotapi.Message, state *storage.AddProductState) {
	chatID := msg.Chat.ID
	lang := msg.From.LanguageCode

	switch msg.Command() {
	case "done", "skip":
		if state.EditProductID != 0 {
			_ = b.fsm.DelAddProductState(ctx, msg.From.ID)
			b.sendAdminPhotoList(chatID, 0, state.EditProductID, lang)
			return
		}
		state.Step = storage.StepCategory
		_ = b.fsm.SetAddProductState(ctx, msg.From.ID, state, 30*time.Minute)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_add_product_category")))
		return
	}

	fileID := ""
	if len(msg.Photo) > 0 {
		fileID = largestPhotoFileID(msg.Photo)
	} else if text := strings.TrimSpace(msg.Text); text != "" {
		fileID = text
	}
	if fileID == "" {
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_photo_prompt")))
		return
	}

	if state.EditProductID != 0 {
		b.addProductPhoto(ctx, chatID, state.EditProductID, fileID, lang)
		return
	}

	if !appendWizardPhoto(state, fileID) {
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_photo_limit")))
		return
	}
	_ = b.fsm.SetAddProductState(ctx, msg.From.ID, state, 30*time.Minute)
	b.send(tgbotapi.NewMessage(chatID, b.i18n.Tf(lang, "admin_photo_more", len(state.Photos), storage.MaxProductPhotos)))
}

// largestPhotoFileID returns the FileID of the PhotoSize with the largest
// pixel area. Telegram usually sends sizes sorted ascending, but the order is
// not guaranteed, so pick the maximum explicitly.
func largestPhotoFileID(sizes []tgbotapi.PhotoSize) string {
	fileID := ""
	bestArea := -1
	for _, s := range sizes {
		if area := s.Width * s.Height; area > bestArea {
			bestArea = area
			fileID = s.FileID
		}
	}
	return fileID
}

// appendWizardPhoto adds fileID to the in-progress wizard state and reports
// whether it fit under the storage.MaxProductPhotos limit.
func appendWizardPhoto(state *storage.AddProductState, fileID string) bool {
	if len(state.Photos) >= storage.MaxProductPhotos {
		return false
	}
	state.Photos = append(state.Photos, fileID)
	return true
}

// addProductPhoto persists one gallery photo on an existing product and sets
// it as the cover when the product has none.
func (b *Bot) addProductPhoto(ctx context.Context, chatID, productID int64, fileID, lang string) {
	if err := b.photos.Add(ctx, productID, fileID); err != nil {
		if errors.Is(err, storage.ErrTooManyPhotos) {
			b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_photo_limit")))
			return
		}
		b.logger.Error("add product photo", "product_id", productID, "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_photo_error")))
		return
	}
	if p, err := b.products.GetProduct(ctx, productID); err == nil && p.PhotoURL == "" {
		p.PhotoURL = fileID
		if err := b.products.UpdateProduct(ctx, p); err != nil {
			b.logger.Warn("update product cover", "product_id", productID, "error", err)
		}
	}
	b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_photo_added")))
}

// sendAdminPhotoList renders the photo management screen for a product:
// one delete button per photo plus an add button.
func (b *Bot) sendAdminPhotoList(chatID int64, msgID int, productID int64, lang string) {
	ctx := context.Background()
	photos, err := b.photos.List(ctx, productID)
	if err != nil {
		b.logger.Error("list product photos", "product_id", productID, "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_photo_error")))
		return
	}

	text := b.i18n.Tf(lang, "admin_photo_list_title", productID, len(photos), storage.MaxProductPhotos)
	if len(photos) == 0 {
		text += "\n" + b.t(lang, "admin_photo_none")
	}

	kb := make(StyledKeyboard, 0, len(photos)+1)
	for i, ph := range photos {
		kb = append(kb, []StyledButton{BtnDanger(fmt.Sprintf("🗑 %d", i+1), fmt.Sprintf("admin:photodel:%d:%d", ph.ID, productID))})
	}
	if len(photos) < storage.MaxProductPhotos {
		kb = append(kb, []StyledButton{Btn(b.t(lang, "admin_photo_add_btn"), fmt.Sprintf("admin:photoadd:%d", productID))})
	}
	b.sendOrEditStyled(chatID, msgID, text, "", kb)
}

// onAdminPhotoDelete handles admin:photodel:<photoID>:<productID>. After
// deleting it re-syncs the cover for wizard-managed (file_id) covers and
// re-renders the list.
func (b *Bot) onAdminPhotoDelete(chatID int64, msgID int, data, lang string) {
	parts := strings.Split(strings.TrimPrefix(data, "admin:photodel:"), ":")
	if len(parts) != 2 {
		return
	}
	photoID, err1 := strconv.ParseInt(parts[0], 10, 64)
	productID, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil {
		return
	}

	ctx := context.Background()
	if err := b.photos.Delete(ctx, photoID); err != nil {
		b.logger.Error("delete product photo", "photo_id", photoID, "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_photo_error")))
		return
	}
	b.syncProductCover(ctx, productID)
	b.sendAdminPhotoList(chatID, msgID, productID, lang)
}

// syncProductCover keeps products.photo_url pointing at an existing gallery
// photo. Explicit http(s) URL covers set by the admin are left untouched.
func (b *Bot) syncProductCover(ctx context.Context, productID int64) {
	p, err := b.products.GetProduct(ctx, productID)
	if err != nil {
		b.logger.Warn("get product for cover sync", "product_id", productID, "error", err)
		return
	}
	if strings.HasPrefix(p.PhotoURL, "http://") || strings.HasPrefix(p.PhotoURL, "https://") {
		return
	}
	photos, err := b.photos.List(ctx, productID)
	if err != nil {
		b.logger.Warn("list photos for cover sync", "product_id", productID, "error", err)
		return
	}
	for _, ph := range photos {
		if ph.FileID == p.PhotoURL {
			return
		}
	}
	cover := ""
	if len(photos) > 0 {
		cover = photos[0].FileID
	}
	if p.PhotoURL == cover {
		return
	}
	p.PhotoURL = cover
	if err := b.products.UpdateProduct(ctx, p); err != nil {
		b.logger.Warn("update product cover", "product_id", productID, "error", err)
	}
}

// onAdminPhotoAdd handles admin:photoadd:<productID>: puts the admin into a
// photo-only wizard state bound to the existing product.
func (b *Bot) onAdminPhotoAdd(chatID, userID int64, data, lang string) {
	productID, err := parseIDFromCallback(data, "admin:photoadd:")
	if err != nil {
		b.logger.Error("parse admin:photoadd callback", "error", err)
		return
	}
	ctx := context.Background()
	_ = b.fsm.SetAddProductState(ctx, userID, &storage.AddProductState{Step: storage.StepPhoto, EditProductID: productID, CreatedAt: time.Now()}, 30*time.Minute)
	b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_photo_prompt")))
}

func (b *Bot) sendAdminProductDetails(chatID int64, product *storage.Product, lang string) {
	toggleLabel := b.t(lang, "admin_btn_stock_off")
	if product.Stock <= 0 {
		toggleLabel = b.t(lang, "admin_btn_stock_on")
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
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "admin_photo_btn"), fmt.Sprintf("admin:photos:%d", product.ID)),
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
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_usage_editproduct")))
		return
	}

	product, err := b.products.GetProduct(context.Background(), id)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_product_not_found")))
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
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_field_value_required")))
		return
	}

	ctx := context.Background()
	product, err := b.products.GetProduct(ctx, prodID)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_product_not_found")))
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
			b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_invalid_price")))
			return
		}
		product.PriceUSD = price
	case "stock":
		stock, err := strconv.Atoi(value)
		if err != nil {
			b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_invalid_stock")))
			return
		}
		product.Stock = stock
	case "category":
		categoryID, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_invalid_category_id")))
			return
		}
		product.CategoryID = categoryID
	case "active":
		active, err := strconv.ParseBool(value)
		if err != nil {
			b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_invalid_bool")))
			return
		}
		product.IsActive = active
	default:
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_supported_fields")))
		return
	}

	if err := b.products.UpdateProduct(ctx, product); err != nil {
		b.logger.Error("update product", "product_id", prodID, "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_product_update_failed")))
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
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_usage_deleteproduct")))
		return
	}
	if err := b.products.DeleteProduct(context.Background(), id); err != nil {
		b.logger.Error("delete product", "product_id", id, "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_product_delete_failed")))
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
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_usage_addcategory")))
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
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_category_create_failed")))
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
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_usage_editcategory")))
		return
	}

	categoryID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_invalid_category_id")))
		return
	}

	ctx := context.Background()
	category, err := b.catalog.GetCategory(ctx, categoryID)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_category_not_found")))
		return
	}

	value := strings.Join(args[2:], " ")
	switch strings.ToLower(args[1]) {
	case "name":
		category.Name = value
	case "emoji":
		category.Emoji = value
	default:
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_category_fields")))
		return
	}

	if err := b.catalog.UpdateCategory(ctx, category); err != nil {
		b.logger.Error("update category", "category_id", categoryID, "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_category_update_failed")))
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
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_usage_deletecategory")))
		return
	}
	if err := b.catalog.DeleteCategory(context.Background(), id); err != nil {
		b.logger.Error("delete category", "category_id", id, "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_category_delete_failed")))
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
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_categories_load_failed")))
		return
	}
	if len(categories) == 0 {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_categories_empty")))
		return
	}

	var sb strings.Builder
	sb.WriteString(b.t(lang, "admin_categories_title"))
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
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_orders_load_failed")))
		return
	}
	if len(orders) == 0 {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_orders_empty")))
		return
	}

	var sb strings.Builder
	sb.WriteString(b.t(lang, "admin_orders_title"))
	for _, order := range orders {
		status := storage.StatusDisplay[order.Status]
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
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_usage_setdelivered")))
		return
	}
	order, err := b.order.SetDelivered(context.Background(), id)
	if err != nil {
		b.logger.Error("set delivered", "order_id", id, "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_set_delivered_failed")))
		return
	}
	b.send(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf(b.t(lang, "admin_delivered_ok"), order.ID)))

	// Invite the buyer to rate the freshly delivered order (1..5 stars).
	b.sendReviewInvite(context.Background(), order)

	b.notifyAdmins(context.Background(), AdminEventOrderDelivered,
		fmt.Sprintf(b.t("en", "admin_order_delivered"), order.ID, order.UserID))

	b.outWebhook.Send(service.OutboundWebhookEvent{
		Event:      "order.delivered",
		OrderID:    order.ID,
		UserID:     order.UserID,
		TotalUSD:   order.TotalUSD,
		TotalStars: order.TotalStars,
	})
}

func (b *Bot) handleAnalytics(msg *tgbotapi.Message) {
	b.sendAnalytics(msg.Chat.ID, 0, analyticsDefaultDays, msg.From.LanguageCode)
}

func (b *Bot) handleAnalyticsCallback(chatID int64, msgID int, data, lang string) {
	days := analyticsDefaultDays
	if strings.HasPrefix(data, "analytics:") {
		if parsed, err := strconv.Atoi(strings.TrimPrefix(data, "analytics:")); err == nil && parsed > 0 {
			days = parsed
		}
	}
	b.sendAnalytics(chatID, msgID, days, lang)
}

const (
	// analyticsDefaultDays is the default reporting window: the spec asks for
	// a 14-day revenue chart on /analytics.
	analyticsDefaultDays = 14
	// revenueChartWidth is the bar length of the busiest day, in ▇ blocks.
	revenueChartWidth = 10
)

// renderRevenueChart renders one text-bar line per day for the `days` days
// ending at `today` (inclusive, oldest first). Bars are normalized so the
// busiest day spans revenueChartWidth ▇ blocks; days without revenue render
// as a single "·".
func renderRevenueChart(daily []storage.DailyRevenue, today time.Time, days int) string {
	byDate := make(map[string]float64, len(daily))
	var maxUSD float64
	for _, d := range daily {
		byDate[d.Date] = d.TotalUSD
		if d.TotalUSD > maxUSD {
			maxUSD = d.TotalUSD
		}
	}

	var sb strings.Builder
	for i := days - 1; i >= 0; i-- {
		day := today.AddDate(0, 0, -i)
		usd := byDate[day.Format("2006-01-02")]
		sb.WriteString(day.Format("01-02"))
		sb.WriteByte(' ')
		if usd <= 0 || maxUSD <= 0 {
			sb.WriteString("·")
		} else {
			bars := int(math.Round(usd / maxUSD * revenueChartWidth))
			if bars < 1 {
				bars = 1
			}
			sb.WriteString(strings.Repeat("▇", bars))
			fmt.Fprintf(&sb, " $%.2f", usd)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

func (b *Bot) sendAnalytics(chatID int64, msgID int, days int, lang string) {
	ctx := context.Background()
	fail := func(stage string, err error) {
		b.logger.Error("analytics "+stage, "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_analytics_error")))
	}

	summary, err := b.analytics.GetRevenueSummary(ctx)
	if err != nil {
		fail("summary", err)
		return
	}
	revenueByDays, err := b.analytics.GetRevenueByDays(ctx, days)
	if err != nil {
		fail("revenue by days", err)
		return
	}
	topProducts, err := b.analytics.GetTopProducts(ctx, 5)
	if err != nil {
		fail("top products", err)
		return
	}
	topBuyers, err := b.analytics.TopBuyers(ctx, 10)
	if err != nil {
		fail("top buyers", err)
		return
	}
	promoUsage, err := b.analytics.PromoUsage(ctx)
	if err != nil {
		fail("promo usage", err)
		return
	}
	paymentStats, err := b.analytics.GetPaymentMethodStats(ctx)
	if err != nil {
		fail("payment stats", err)
		return
	}

	none := b.t(lang, "admin_analytics_none") + "\n"

	var sb strings.Builder
	sb.WriteString(b.i18n.Tf(lang, "admin_analytics_title", days) + "\n\n")
	sb.WriteString(b.i18n.Tf(lang, "admin_analytics_total_orders", summary.TotalOrders) + "\n")
	sb.WriteString(b.i18n.Tf(lang, "admin_analytics_paid_orders", summary.PaidOrders) + "\n")

	var periodUSD float64
	var periodStars int
	var periodOrders int
	for _, day := range revenueByDays {
		periodUSD += day.TotalUSD
		periodStars += day.TotalStars
		periodOrders += day.OrderCount
	}
	sb.WriteString(b.i18n.Tf(lang, "admin_analytics_period_revenue", periodUSD, periodStars, periodOrders) + "\n\n")

	sb.WriteString(b.t(lang, "admin_analytics_chart_title") + "\n")
	sb.WriteString(renderRevenueChart(revenueByDays, time.Now().UTC(), days))
	sb.WriteString("\n")

	sb.WriteString(b.t(lang, "admin_analytics_top_products") + "\n")
	if len(topProducts) == 0 {
		sb.WriteString(none)
	} else {
		for _, product := range topProducts {
			sb.WriteString(b.i18n.Tf(lang, "admin_analytics_product_row", product.Name, product.TotalSold, product.TotalRevenue) + "\n")
		}
	}

	sb.WriteString("\n" + b.t(lang, "admin_analytics_top_buyers") + "\n")
	if len(topBuyers) == 0 {
		sb.WriteString(none)
	} else {
		for i, buyer := range topBuyers {
			sb.WriteString(b.i18n.Tf(lang, "admin_analytics_buyer_row", i+1, buyer.UserID, buyer.Orders, buyer.TotalUSD) + "\n")
		}
	}

	sb.WriteString("\n" + b.t(lang, "admin_analytics_promo_title") + "\n")
	if len(promoUsage) == 0 {
		sb.WriteString(none)
	} else {
		for _, promo := range promoUsage {
			if promo.DiscountKnown {
				sb.WriteString(b.i18n.Tf(lang, "admin_analytics_promo_row", promo.Code, promo.Uses, promo.DiscountUSD) + "\n")
			} else {
				sb.WriteString(b.i18n.Tf(lang, "admin_analytics_promo_row_unknown", promo.Code, promo.Uses) + "\n")
			}
		}
	}

	sb.WriteString("\n" + b.t(lang, "admin_analytics_payments_title") + "\n")
	if len(paymentStats) == 0 {
		sb.WriteString(none)
	} else {
		for _, stat := range paymentStats {
			sb.WriteString(b.i18n.Tf(lang, "admin_analytics_payment_row", stat.Method, stat.OrderCount, stat.TotalUSD) + "\n")
		}
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.i18n.Tf(lang, "admin_analytics_btn_days", 7), "analytics:7"),
			tgbotapi.NewInlineKeyboardButtonData(b.i18n.Tf(lang, "admin_analytics_btn_days", 14), "analytics:14"),
			tgbotapi.NewInlineKeyboardButtonData(b.i18n.Tf(lang, "admin_analytics_btn_days", 30), "analytics:30"),
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
	args := strings.Fields(msg.CommandArguments())
	if len(args) < 2 {
		return
	}
	discount, _ := strconv.Atoi(args[1])
	p := &storage.PromoCode{Code: strings.ToUpper(args[0]), Discount: discount, IsActive: true}
	_, _ = b.promos.CreatePromo(context.Background(), p)
	b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(msg.From.LanguageCode, "admin_promo_created")))
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
	id, _ := strconv.ParseInt(msg.CommandArguments(), 10, 64)
	_ = b.promos.DeactivatePromo(context.Background(), id)
	b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(msg.From.LanguageCode, "admin_promo_deactivated")))
}

// exportDateLayout is the accepted /export_orders argument format.
const exportDateLayout = "2006-01-02"

// parseExportRange parses the optional [from] [to] arguments of
// /export_orders. Nil means "unbounded". The returned `to` bound is
// exclusive: it points at midnight AFTER the requested inclusive end date.
// A malformed argument is returned verbatim in bad.
func parseExportRange(args []string) (from, to *time.Time, bad string) {
	if len(args) > 0 {
		t, err := time.Parse(exportDateLayout, args[0])
		if err != nil {
			return nil, nil, args[0]
		}
		from = &t
	}
	if len(args) > 1 {
		t, err := time.Parse(exportDateLayout, args[1])
		if err != nil {
			return nil, nil, args[1]
		}
		end := t.AddDate(0, 0, 1)
		to = &end
	}
	return from, to, ""
}

// filterOrdersByDate keeps orders with from <= CreatedAt < to. Nil bounds are
// unbounded, so (nil, nil) returns the input unchanged.
func filterOrdersByDate(orders []storage.Order, from, to *time.Time) []storage.Order {
	if from == nil && to == nil {
		return orders
	}
	filtered := make([]storage.Order, 0, len(orders))
	for _, order := range orders {
		if from != nil && order.CreatedAt.Before(*from) {
			continue
		}
		if to != nil && !order.CreatedAt.Before(*to) {
			continue
		}
		filtered = append(filtered, order)
	}
	return filtered
}

func (b *Bot) handleExportOrders(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	lang := msg.From.LanguageCode

	from, to, bad := parseExportRange(strings.Fields(msg.CommandArguments()))
	if bad != "" {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.i18n.Tf(lang, "admin_export_bad_date", bad)))
		return
	}

	orders, err := b.order.GetAllOrders(context.Background(), "")
	if err != nil {
		b.logger.Error("export orders", "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_export_failed")))
		return
	}
	orders = filterOrdersByDate(orders, from, to)

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
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_export_failed")))
		return
	}

	doc := tgbotapi.NewDocument(msg.Chat.ID, tgbotapi.FileBytes{
		Name:  fmt.Sprintf("orders_%s.csv", time.Now().Format("20060102_150405")),
		Bytes: buf.Bytes(),
	})
	doc.Caption = b.i18n.Tf(lang, "admin_export_caption", len(orders))
	b.send(doc)
}

func (b *Bot) onAdminToggleStock(chatID int64, data, lang string) {
	productID, err := parseIDFromCallback(data, "admin:togglestock:")
	if err != nil {
		b.logger.Error("parse admin:togglestock callback", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_product_parse_failed")))
		return
	}

	ctx := context.Background()
	product, err := b.products.GetProduct(ctx, productID)
	if err != nil {
		b.logger.Error("get product for stock toggle", "product_id", productID, "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_product_not_found")))
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
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_product_update_failed")))
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
	sb.WriteString(b.t(lang, "admin_btnstyle_title"))
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
		b.t(lang, "admin_btnstyle_picker"),
		ButtonKeyLabel(key),
		StyleEmoji(ButtonStyle(current)),
		styleLabel(ButtonStyle(current)),
	)

	styleOptions := []struct {
		label string
		style ButtonStyle
	}{
		{"🔵 Primary", StylePrimary},
		{"🟢 Success", StyleSuccess},
		{"🔴 Danger", StyleDanger},
		{b.t(lang, "admin_btnstyle_default"), StyleDefault},
	}

	var styleRow []StyledButton
	for _, opt := range styleOptions {
		styleRow = append(styleRow, Btn(opt.label, fmt.Sprintf("admin:setstyle:%s:%s", key, string(opt.style))))
	}

	kb := StyledKeyboard{
		styleRow,
		{Btn(b.t(lang, "admin_btnstyle_back"), "admin:btnlist")},
	}
	b.sendOrEditStyled(chatID, msgID, text, "HTML", kb)
}

// onAdminSetStyle persists the style choice and returns to the overview.
func (b *Bot) onAdminSetStyle(chatID int64, msgID int, data, lang string) {
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
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_btnstyle_save_failed")))
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
