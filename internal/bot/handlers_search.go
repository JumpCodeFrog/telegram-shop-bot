package bot

import (
	"context"
	"fmt"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/storage"
)

// sendSearchHint shows the search prompt screen (opened from the main menu
// "🔍 Поиск" button). It tells the user how to run /search.
func (b *Bot) sendSearchHint(chatID int64, msgID int, lang string) {
	kb := StyledKeyboard{
		{Btn(b.t(lang, "btn_back"), "back:menu"), Btn(b.t(lang, "btn_menu"), "back:menu")},
	}
	b.sendOrEditStyled(chatID, msgID, b.t(lang, "search_hint"), "HTML", kb)
}

// searchNavRow is the Назад|Меню row appended to every /search response.
func (b *Bot) searchNavRow(lang string) []StyledButton {
	return []StyledButton{
		Btn(b.t(lang, "btn_back"), "back:search"),
		Btn(b.t(lang, "btn_menu"), "back:menu"),
	}
}

// searchResultsKeyboard builds one product button per result plus a Назад|Меню row.
func (b *Bot) searchResultsKeyboard(lang string, products []storage.Product) StyledKeyboard {
	kb := make(StyledKeyboard, 0, len(products)+1)
	for _, p := range products {
		label := fmt.Sprintf("🛍 %s — $%.2f", p.Name, p.PriceUSD)
		kb = append(kb, []StyledButton{
			b.styledBtn(BtnKeyCatalogProduct, label, fmt.Sprintf("product:%d", p.ID), StylePrimary),
		})
	}
	return append(kb, b.searchNavRow(lang))
}

// handleSearch searches for in-stock products matching the query.
func (b *Bot) handleSearch(msg *tgbotapi.Message) {
	lang := msg.From.LanguageCode
	chatID := msg.Chat.ID
	navKB := StyledKeyboard{b.searchNavRow(lang)}

	query := strings.TrimSpace(msg.CommandArguments())
	if len([]rune(query)) < 2 {
		b.sendOrEditStyled(chatID, 0, b.t(lang, "search_too_short"), "", navKB)
		return
	}

	ctx := context.Background()
	products, err := b.products.SearchProducts(ctx, query)
	if err != nil {
		b.logger.Error("search products", "error", err)
		b.sendOrEditStyled(chatID, 0, b.t(lang, "error_short"), "", navKB)
		return
	}

	if len(products) == 0 {
		b.sendOrEditStyled(chatID, 0, fmt.Sprintf(b.t(lang, "search_not_found"), query), "", navKB)
		return
	}

	text := fmt.Sprintf(b.t(lang, "search_results_title"), query)
	b.sendOrEditStyled(chatID, 0, text, "", b.searchResultsKeyboard(lang, products))
}
