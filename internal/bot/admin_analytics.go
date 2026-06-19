package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

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
