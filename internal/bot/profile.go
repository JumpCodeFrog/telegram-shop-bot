package bot

import (
"context"

tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func (b *Bot) handleProfile(msg *tgbotapi.Message) {
b.sendProfile(msg.Chat.ID, msg.From.ID, 0, msg.From.LanguageCode)
}

func (b *Bot) sendProfile(chatID, userID int64, msgID int, lang string) {
ctx := context.Background()

user, err := b.users.GetByTelegramID(ctx, userID)
if err != nil {
b.logger.Error("get profile user", "error", err)
b.sendOrEditStyled(chatID, msgID, b.t(lang, "error_load_profile"), "", nil)
return
}

orders, err := b.order.GetUserOrders(ctx, userID)
if err != nil {
b.logger.Error("get profile orders", "error", err)
b.sendOrEditStyled(chatID, msgID, b.t(lang, "error_load_profile"), "", nil)
return
}
text := b.formatProfileText(lang, user, len(orders))

kb := StyledKeyboard{
{
b.styledBtn(BtnKeyMenuCatalog, b.t(lang, "btn_catalog"), "back:catalog", StylePrimary),
b.styledBtn(BtnKeyMenuCart, b.t(lang, "btn_cart"), "back:cart", StyleDefault),
},
{
Btn(b.t(lang, "btn_orders"), "back:orders"),
Btn(b.t(lang, "btn_menu"), "back:menu"),
},
}
b.sendOrEditStyled(chatID, msgID, text, "HTML", kb)
}
