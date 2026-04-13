package bot

import (
	"context"
	"fmt"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

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
