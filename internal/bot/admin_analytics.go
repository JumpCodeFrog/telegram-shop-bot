package bot

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/storage"
)

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
