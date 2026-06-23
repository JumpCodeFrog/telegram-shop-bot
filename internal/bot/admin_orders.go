package bot

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/service"
	"shop_bot/internal/storage"
)

func (b *Bot) handleOrdersAll(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	statusFilter := strings.TrimSpace(msg.CommandArguments())
	ctx, cancel := handlerCtx()
	defer cancel()
	orders, err := b.order.GetAllOrders(ctx, statusFilter)
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
	ctx, cancel := handlerCtx()
	defer cancel()
	order, err := b.order.SetDelivered(ctx, id)
	if err != nil {
		b.logger.Error("set delivered", "order_id", id, "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Не удалось отметить заказ доставленным."))
		return
	}
	b.send(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("✅ Заказ #%d отмечен как доставленный.", order.ID)))

	b.outWebhook.Send(service.OutboundWebhookEvent{
		Event:      "order.delivered",
		OrderID:    order.ID,
		UserID:     order.UserID,
		TotalUSD:   order.TotalUSD,
		TotalStars: order.TotalStars,
	})
}

func (b *Bot) handleExportOrders(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}

	ctx, cancel := handlerCtx()
	defer cancel()
	orders, err := b.order.GetAllOrders(ctx, "")
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
