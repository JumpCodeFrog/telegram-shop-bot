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
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "error_load_profile")))
		return
	}

	orders, err := b.order.GetUserOrders(ctx, userID)
	if err != nil {
		b.logger.Error("get profile orders", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "error_load_profile")))
		return
	}
	text := b.formatProfileText(lang, user, len(orders))

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_cart"), "back:cart"),
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_orders"), "back:orders"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_catalog"), "back:catalog"),
		),
	)

	if msgID > 0 {
		edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
		edit.ParseMode = "HTML"
		edit.ReplyMarkup = &keyboard
		b.send(edit)
		return
	}

	reply := tgbotapi.NewMessage(chatID, text)
	reply.ParseMode = "HTML"
	reply.ReplyMarkup = keyboard
	b.send(reply)
}
