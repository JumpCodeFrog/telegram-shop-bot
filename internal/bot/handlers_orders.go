package bot

import (
"context"

tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// handleOrders displays the user's order history with formatted statuses.
func (b *Bot) handleOrders(msg *tgbotapi.Message) {
b.sendOrders(msg.Chat.ID, msg.From.ID, 0, msg.From.LanguageCode)
}

// sendOrders sends the order history. If msgID > 0, it edits the existing message.
func (b *Bot) sendOrders(chatID, userID int64, msgID int, lang string) {
ctx := context.Background()
orders, err := b.order.GetUserOrders(ctx, userID)
if err != nil {
b.logger.Error("get user orders", "error", err)
b.sendOrEditStyled(chatID, msgID, b.t(lang, "error_load_orders"), "", nil)
return
}

if len(orders) == 0 {
kb := StyledKeyboard{
{Btn(b.t(lang, "btn_back"), "back:catalog"), Btn(b.t(lang, "btn_menu"), "back:menu")},
}
b.sendOrEditStyled(chatID, msgID, b.t(lang, "orders_empty"), "", kb)
return
}

kb := StyledKeyboard{
{Btn(b.t(lang, "btn_back"), "back:catalog"), Btn(b.t(lang, "btn_menu"), "back:menu")},
}
b.sendOrEditStyled(chatID, msgID, b.formatOrdersText(lang, orders), "HTML", kb)
}
