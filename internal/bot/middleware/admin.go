package middleware

import (
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func AdminOnly(adminIDs []int64) func(next func(update tgbotapi.Update)) func(update tgbotapi.Update) {
	allowed := make(map[int64]struct{}, len(adminIDs))
	for _, id := range adminIDs {
		allowed[id] = struct{}{}
	}
	return func(next func(update tgbotapi.Update)) func(update tgbotapi.Update) {
		return func(update tgbotapi.Update) {
			var userID int64
			if update.Message != nil {
				userID = update.Message.From.ID
			} else if update.CallbackQuery != nil {
				userID = update.CallbackQuery.From.ID
			}
			if _, ok := allowed[userID]; ok {
				next(update)
			}
		}
	}
}
