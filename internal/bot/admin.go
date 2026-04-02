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

	text := "🔧 Панель администратора\n\n" +
		"Товары:\n" +
		"/addproduct — Добавить товар\n" +
		"/editproduct <id> — Редактировать товар\n" +
		"/deleteproduct <id> — Удалить товар\n\n" +
		"Категории:\n" +
		"/addcategory <emoji> <название> — Создать категорию\n" +
		"/editcategory <id> name|emoji <значение> — Изменить поле\n" +
		"/deletecategory <id> — Удалить категорию\n" +
		"/listcategories — Список категорий с ID\n\n" +
		"Заказы:\n" +
		"/orders_all [статус] — Все заказы\n" +
		"/setdelivered <id> — Отметить заказ доставленным\n" +
		"/export_orders — Экспорт заказов в CSV\n\n" +
		"Промокоды:\n" +
		"/addpromo <код> <скидка%> [макс_использований] [дней] [category_id] — Создать промокод\n" +
		"/listpromos — Активные промокоды\n" +
		"/deletepromo <id> — Деактивировать промокод\n\n" +
		"Аналитика:\n" +
		"/analytics — Статистика продаж"

	b.send(tgbotapi.NewMessage(msg.Chat.ID, text))
}

func (b *Bot) handleAddProduct(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	ctx := context.Background()
	_ = b.fsm.SetAddProductState(ctx, msg.From.ID, &storage.AddProductState{Step: storage.StepName, CreatedAt: time.Now()}, 30*time.Minute)
	b.send(tgbotapi.NewMessage(msg.Chat.ID, "📝 Введите название товара:"))
}

func (b *Bot) handleAddProductStep(msg *tgbotapi.Message) bool {
	ctx := context.Background()
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
	ctx := context.Background()
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

	product, err := b.products.GetProduct(context.Background(), id)
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

	ctx := context.Background()
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
	if err := b.products.DeleteProduct(context.Background(), id); err != nil {
		b.logger.Error("delete product", "product_id", id, "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Не удалось удалить товар."))
		return
	}
	b.send(tgbotapi.NewMessage(msg.Chat.ID, "✅ Товар удалён."))
}

func (b *Bot) handleAddCategory(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	args := strings.Fields(msg.CommandArguments())
	if len(args) < 2 {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "Использование: /addcategory <emoji> <название>"))
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
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Не удалось создать категорию."))
		return
	}
	b.send(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("✅ Категория создана: #%d %s %s", id, cat.Emoji, cat.Name)))
}

func (b *Bot) handleEditCategory(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	args := strings.Fields(msg.CommandArguments())
	if len(args) < 3 {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "Использование: /editcategory <id> name|emoji <значение>"))
		return
	}

	categoryID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Некорректный ID категории."))
		return
	}

	ctx := context.Background()
	category, err := b.catalog.GetCategory(ctx, categoryID)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Категория не найдена."))
		return
	}

	value := strings.Join(args[2:], " ")
	switch strings.ToLower(args[1]) {
	case "name":
		category.Name = value
	case "emoji":
		category.Emoji = value
	default:
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Поддерживаются только поля name и emoji."))
		return
	}

	if err := b.catalog.UpdateCategory(ctx, category); err != nil {
		b.logger.Error("update category", "category_id", categoryID, "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Не удалось обновить категорию."))
		return
	}

	b.send(tgbotapi.NewMessage(msg.Chat.ID, "✅ Категория обновлена."))
}

func (b *Bot) handleDeleteCategory(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(msg.CommandArguments()), 10, 64)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "Использование: /deletecategory <id>"))
		return
	}
	if err := b.catalog.DeleteCategory(context.Background(), id); err != nil {
		b.logger.Error("delete category", "category_id", id, "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Не удалось удалить категорию. Возможно, в ней ещё есть товары."))
		return
	}
	b.send(tgbotapi.NewMessage(msg.Chat.ID, "✅ Категория удалена."))
}

func (b *Bot) handleListCategories(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	categories, err := b.catalog.ListCategories(context.Background())
	if err != nil {
		b.logger.Error("list categories", "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Не удалось загрузить категории."))
		return
	}
	if len(categories) == 0 {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "Категории не найдены."))
		return
	}

	var sb strings.Builder
	sb.WriteString("📂 Категории:\n\n")
	for _, category := range categories {
		sb.WriteString(fmt.Sprintf("%d: %s %s\n", category.ID, category.Emoji, category.Name))
	}
	b.send(tgbotapi.NewMessage(msg.Chat.ID, sb.String()))
}

func (b *Bot) handleOrdersAll(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	statusFilter := strings.TrimSpace(msg.CommandArguments())
	orders, err := b.order.GetAllOrders(context.Background(), statusFilter)
	if err != nil {
		b.logger.Error("get all orders", "status", statusFilter, "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Не удалось загрузить заказы."))
		return
	}
	if len(orders) == 0 {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "Заказы не найдены."))
		return
	}

	var sb strings.Builder
	sb.WriteString("📦 Заказы:\n\n")
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
	id, err := strconv.ParseInt(strings.TrimSpace(msg.CommandArguments()), 10, 64)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "Использование: /setdelivered <id>"))
		return
	}
	order, err := b.order.SetDelivered(context.Background(), id)
	if err != nil {
		b.logger.Error("set delivered", "order_id", id, "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Не удалось отметить заказ доставленным."))
		return
	}
	b.send(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("✅ Заказ #%d отмечен как доставленный.", order.ID)))
}

func (b *Bot) handleAnalytics(msg *tgbotapi.Message) { b.sendAnalytics(msg.Chat.ID, 0, 7) }

func (b *Bot) handleAnalyticsCallback(chatID int64, msgID int, data string) {
	days := 7
	if strings.HasPrefix(data, "analytics:") {
		if parsed, err := strconv.Atoi(strings.TrimPrefix(data, "analytics:")); err == nil && parsed > 0 {
			days = parsed
		}
	}
	b.sendAnalytics(chatID, msgID, days)
}

func (b *Bot) sendAnalytics(chatID int64, msgID int, days int) {
	ctx := context.Background()

	summary, err := b.analytics.GetRevenueSummary(ctx)
	if err != nil {
		b.logger.Error("analytics summary", "error", err)
		b.send(tgbotapi.NewMessage(chatID, "❌ Не удалось загрузить аналитику."))
		return
	}
	revenueByDays, err := b.analytics.GetRevenueByDays(ctx, days)
	if err != nil {
		b.logger.Error("analytics revenue by days", "error", err)
		b.send(tgbotapi.NewMessage(chatID, "❌ Не удалось загрузить аналитику."))
		return
	}
	topProducts, err := b.analytics.GetTopProducts(ctx, 5)
	if err != nil {
		b.logger.Error("analytics top products", "error", err)
		b.send(tgbotapi.NewMessage(chatID, "❌ Не удалось загрузить аналитику."))
		return
	}
	paymentStats, err := b.analytics.GetPaymentMethodStats(ctx)
	if err != nil {
		b.logger.Error("analytics payment stats", "error", err)
		b.send(tgbotapi.NewMessage(chatID, "❌ Не удалось загрузить аналитику."))
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 Статистика за последние %d дней\n\n", days))
	sb.WriteString(fmt.Sprintf("Всего заказов (all time): %d\n", summary.TotalOrders))
	sb.WriteString(fmt.Sprintf("Оплаченных заказов (all time): %d\n", summary.PaidOrders))

	var periodUSD float64
	var periodStars int
	var periodOrders int
	for _, day := range revenueByDays {
		periodUSD += day.TotalUSD
		periodStars += day.TotalStars
		periodOrders += day.OrderCount
	}
	sb.WriteString(fmt.Sprintf("Выручка за период: $%.2f / %d ⭐ (%d заказов)\n\n", periodUSD, periodStars, periodOrders))

	if len(revenueByDays) > 0 {
		sb.WriteString("По дням:\n")
		for _, day := range revenueByDays {
			sb.WriteString(fmt.Sprintf("• %s — $%.2f / %d ⭐ (%d)\n", day.Date, day.TotalUSD, day.TotalStars, day.OrderCount))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Топ товаров:\n")
	if len(topProducts) == 0 {
		sb.WriteString("• Пока нет оплаченных заказов\n")
	} else {
		for _, product := range topProducts {
			sb.WriteString(fmt.Sprintf("• %s — %d шт. / $%.2f\n", product.Name, product.TotalSold, product.TotalRevenue))
		}
	}

	sb.WriteString("\nОплаты по методам:\n")
	if len(paymentStats) == 0 {
		sb.WriteString("• Пока нет оплаченных заказов\n")
	} else {
		for _, stat := range paymentStats {
			sb.WriteString(fmt.Sprintf("• %s — %d заказов / $%.2f\n", stat.Method, stat.OrderCount, stat.TotalUSD))
		}
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("7 дн.", "analytics:7"),
			tgbotapi.NewInlineKeyboardButtonData("30 дн.", "analytics:30"),
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
	b.send(tgbotapi.NewMessage(msg.Chat.ID, "✅ Промокод создан"))
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
	b.send(tgbotapi.NewMessage(msg.Chat.ID, "✅ Промокод деактивирован"))
}

func (b *Bot) handleExportOrders(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}

	orders, err := b.order.GetAllOrders(context.Background(), "")
	if err != nil {
		b.logger.Error("export orders", "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Не удалось выгрузить заказы."))
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
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Не удалось собрать CSV."))
		return
	}

	doc := tgbotapi.NewDocument(msg.Chat.ID, tgbotapi.FileBytes{
		Name:  fmt.Sprintf("orders_%s.csv", time.Now().Format("20060102_150405")),
		Bytes: buf.Bytes(),
	})
	doc.Caption = fmt.Sprintf("Экспорт заказов: %d строк", len(orders))
	b.send(doc)
}

func (b *Bot) onAdminToggleStock(chatID int64, data string) {
	productID, err := parseIDFromCallback(data, "admin:togglestock:")
	if err != nil {
		b.logger.Error("parse admin:togglestock callback", "error", err)
		b.send(tgbotapi.NewMessage(chatID, "❌ Не удалось определить товар."))
		return
	}

	ctx := context.Background()
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
