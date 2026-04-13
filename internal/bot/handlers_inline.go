package bot

import (
	"fmt"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/storage"
)

const inlineResultsLimit = 20

// handleInlineQuery handles inline queries by returning matching active products.
// Usage in Telegram: @bot_name <search query>
func (b *Bot) handleInlineQuery(iq *tgbotapi.InlineQuery) {
	ctx, cancel := handlerCtx()
	defer cancel()

	lang := iq.From.LanguageCode
	if lang == "" {
		lang = "en"
	}

	products, err := b.products.SearchProducts(ctx, iq.Query)
	if err != nil {
		b.logger.Error("inline query: search products", "query", iq.Query, "error", err)
		_, _ = b.api.Request(tgbotapi.InlineConfig{
			InlineQueryID: iq.ID,
			Results:       []interface{}{},
			CacheTime:     5,
		})
		return
	}

	if len(products) > inlineResultsLimit {
		products = products[:inlineResultsLimit]
	}

	results := make([]interface{}, 0, len(products))
	for i := range products {
		p := &products[i]
		if !p.IsActive || p.Stock <= 0 {
			continue
		}
		results = append(results, b.inlineResultForProduct(lang, p))
	}

	_, _ = b.api.Request(tgbotapi.InlineConfig{
		InlineQueryID: iq.ID,
		Results:       results,
		CacheTime:     30,
	})
}

func (b *Bot) inlineResultForProduct(lang string, p *storage.Product) interface{} {
	id := fmt.Sprintf("prod_%d", p.ID)
	caption := b.formatProductCaption(lang, p)

	if p.PhotoURL != "" {
		r := tgbotapi.NewInlineQueryResultCachedPhoto(id, p.PhotoURL)
		r.Title = p.Name
		r.Caption = caption
		r.ParseMode = "HTML"
		return r
	}

	starsText := ""
	if p.PriceStars > 0 {
		starsText = fmt.Sprintf(" / %d ⭐", p.PriceStars)
	}
	title := fmt.Sprintf("%s — $%.2f%s", p.Name, p.PriceUSD, starsText)

	r := tgbotapi.NewInlineQueryResultArticleHTML(id, title, caption)
	if len(p.Description) > 100 {
		r.Description = p.Description[:100] + "…"
	} else {
		r.Description = p.Description
	}
	return r
}

func (b *Bot) formatProductCaption(lang string, p *storage.Product) string {
	starsText := ""
	if p.PriceStars > 0 {
		starsText = fmt.Sprintf(" / %d ⭐", p.PriceStars)
	}
	return fmt.Sprintf(
		"<b>%s</b>\n%s\n\n💵 $%.2f%s\n📦 %s: %d",
		p.Name,
		p.Description,
		p.PriceUSD,
		starsText,
		b.t(lang, "stock"),
		p.Stock,
	)
}
