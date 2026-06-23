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
	ctx, cancel := handlerCtx()
	defer cancel()
	_ = b.fsm.SetAddProductState(ctx, msg.From.ID, &storage.AddProductState{Step: storage.StepName, CreatedAt: time.Now()}, 30*time.Minute)
	b.send(tgbotapi.NewMessage(msg.Chat.ID, "📝 Введите название товара:"))
}

func (b *Bot) handleAddProductStep(msg *tgbotapi.Message) bool {
	ctx, cancel := handlerCtx()
	defer cancel()
	if msg.Text == "/cancel" {
		state, _ := b.fsm.GetAddProductState(ctx, msg.From.ID)
		_ = b.fsm.DelAddProductState(ctx, msg.From.ID)
		if state != nil {
			b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Отменено"))
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
		b.send(tgbotapi.NewMessage(chatID, "Введите описание:"))
	case storage.StepDescription:
		state.Description = msg.Text
		state.Step = storage.StepPriceUSD
		_ = b.fsm.SetAddProductState(ctx, msg.From.ID, state, 30*time.Minute)
		b.send(tgbotapi.NewMessage(chatID, "Введите цену USD:"))
	case storage.StepPriceUSD:
		p, _ := strconv.ParseFloat(msg.Text, 64)
		state.PriceUSD = p
		state.Step = storage.StepStock
		_ = b.fsm.SetAddProductState(ctx, msg.From.ID, state, 30*time.Minute)
		b.send(tgbotapi.NewMessage(chatID, "Введите количество:"))
	case storage.StepStock:
		s, _ := strconv.Atoi(msg.Text)
		state.Stock = s
		state.Step = storage.StepPhoto
		_ = b.fsm.SetAddProductState(ctx, msg.From.ID, state, 30*time.Minute)
		b.send(tgbotapi.NewMessage(chatID, "Отправьте фото или /skip:"))
	case storage.StepPhoto:
		if msg.Photo != nil {
			state.PhotoURL = msg.Photo[len(msg.Photo)-1].FileID
		}
		state.Step = storage.StepCategory
		_ = b.fsm.SetAddProductState(ctx, msg.From.ID, state, 30*time.Minute)
		b.send(tgbotapi.NewMessage(chatID, "Введите ID категории:"))
	case storage.StepCategory:
		id, _ := strconv.ParseInt(msg.Text, 10, 64)
		b.finishAddProduct(chatID, msg.From.ID, id)
	}
	return true
}

func (b *Bot) finishAddProduct(chatID, userID, categoryID int64) {
	ctx, cancel := handlerCtx()
	defer cancel()
	state, _ := b.fsm.GetAddProductState(ctx, userID)
	_ = b.fsm.DelAddProductState(ctx, userID)
	if state == nil {
		return
	}
	p := &storage.Product{CategoryID: categoryID, Name: state.Name, Description: state.Description, PriceUSD: state.PriceUSD, Stock: state.Stock, PhotoURL: state.PhotoURL, IsActive: true}
	_, _ = b.products.CreateProduct(ctx, p)
	b.send(tgbotapi.NewMessage(chatID, "✅ Товар создан"))
}

func (b *Bot) sendAdminProductDetails(chatID int64, product *storage.Product) {
	toggleLabel := "⛔ Снять с наличия"
	if product.Stock <= 0 {
		toggleLabel = "✅ Вернуть в наличие"
	}

	text := fmt.Sprintf(
		"📦 Товар #%d\nНазвание: %s\nОписание: %s\nЦена: $%.2f\nОстаток: %d\nКатегория: %d\nАктивен: %t\n\n"+
			"Редактирование:\n/editproduct %d name <новое значение>\n/editproduct %d description <новое значение>\n/editproduct %d price <число>\n/editproduct %d stock <число>\n/editproduct %d category <id>\n/editproduct %d active true|false",
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
	id, err := strconv.ParseInt(strings.TrimSpace(msg.CommandArguments()), 10, 64)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "Использование: /editproduct <id>"))
		return
	}

	ctx, cancel := handlerCtx()
	defer cancel()
	product, err := b.products.GetProduct(ctx, id)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Товар не найден."))
		return
	}

	b.sendAdminProductDetails(msg.Chat.ID, product)
}

func (b *Bot) handleEditProductField(msg *tgbotapi.Message, prodID int64, field, value string) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	if strings.TrimSpace(value) == "" {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Укажите новое значение поля."))
		return
	}

	ctx, cancel := handlerCtx()
	defer cancel()
	product, err := b.products.GetProduct(ctx, prodID)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Товар не найден."))
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
			b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Некорректная цена."))
			return
		}
		product.PriceUSD = price
	case "stock":
		stock, err := strconv.Atoi(value)
		if err != nil {
			b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Некорректное количество."))
			return
		}
		product.Stock = stock
	case "category":
		categoryID, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Некорректный ID категории."))
			return
		}
		product.CategoryID = categoryID
	case "active":
		active, err := strconv.ParseBool(value)
		if err != nil {
			b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Используйте true или false."))
			return
		}
		product.IsActive = active
	default:
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Поддерживаются поля: name, description, price, stock, category, active."))
		return
	}

	if err := b.products.UpdateProduct(ctx, product); err != nil {
		b.logger.Error("update product", "product_id", prodID, "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Не удалось обновить товар."))
		return
	}

	b.send(tgbotapi.NewMessage(msg.Chat.ID, "✅ Товар обновлён."))
}

func (b *Bot) handleDeleteProduct(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(msg.CommandArguments()), 10, 64)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "Использование: /deleteproduct <id>"))
		return
	}
	ctx, cancel := handlerCtx()
	defer cancel()
	if err := b.products.DeleteProduct(ctx, id); err != nil {
		b.logger.Error("delete product", "product_id", id, "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Не удалось удалить товар."))
		return
	}
	b.send(tgbotapi.NewMessage(msg.Chat.ID, "✅ Товар удалён."))
}

func (b *Bot) onAdminToggleStock(chatID int64, data string) {
	productID, err := parseIDFromCallback(data, "admin:togglestock:")
	if err != nil {
		b.logger.Error("parse admin:togglestock callback", "error", err)
		b.send(tgbotapi.NewMessage(chatID, "❌ Не удалось определить товар."))
		return
	}

	ctx, cancel := handlerCtx()
	defer cancel()
	product, err := b.products.GetProduct(ctx, productID)
	if err != nil {
		b.logger.Error("get product for stock toggle", "product_id", productID, "error", err)
		b.send(tgbotapi.NewMessage(chatID, "❌ Товар не найден."))
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
		b.send(tgbotapi.NewMessage(chatID, "❌ Не удалось обновить товар."))
		return
	}

	b.sendAdminProductDetails(chatID, product)
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
