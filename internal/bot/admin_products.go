package bot

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/storage"
)

func (b *Bot) handleAddProduct(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	ctx, cancel := b.handlerCtx()
	defer cancel()
	_ = b.fsm.SetAddProductState(ctx, msg.From.ID, &storage.AddProductState{Step: storage.StepName, CreatedAt: time.Now()}, 30*time.Minute)
	b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(msg.From.LanguageCode, "admin_add_product_name")))
}

func (b *Bot) handleAddProductStep(msg *tgbotapi.Message) bool {
	ctx, cancel := b.handlerCtx()
	defer cancel()
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
	ctx, cancel := b.handlerCtx()
	defer cancel()
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

	ctx, cancel := b.handlerCtx()
	defer cancel()
	product, err := b.products.GetProduct(ctx, id)
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

	ctx, cancel := b.handlerCtx()
	defer cancel()
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
	ctx, cancel := b.handlerCtx()
	defer cancel()
	if err := b.products.DeleteProduct(ctx, id); err != nil {
		b.logger.Error("delete product", "product_id", id, "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_product_delete_failed")))
		return
	}
	b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_product_deleted")))
}

func (b *Bot) onAdminToggleStock(chatID int64, data, lang string) {
	productID, err := parseIDFromCallback(data, "admin:togglestock:")
	if err != nil {
		b.logger.Error("parse admin:togglestock callback", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_product_parse_failed")))
		return
	}

	ctx, cancel := b.handlerCtx()
	defer cancel()
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
