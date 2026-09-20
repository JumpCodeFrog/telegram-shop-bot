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
