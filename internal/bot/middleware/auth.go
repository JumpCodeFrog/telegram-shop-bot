package middleware

import (
	"context"
	"shop_bot/internal/storage"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type UserStore interface {
	Upsert(ctx context.Context, user *storage.User) error
}

// Auth upserts the Telegram user into storage before the update proceeds,
// using the per-update context carried by the chain (roadmap 4.14).
func Auth(userStore UserStore) func(next func(ctx context.Context, update tgbotapi.Update)) func(ctx context.Context, update tgbotapi.Update) {
	return func(next func(ctx context.Context, update tgbotapi.Update)) func(ctx context.Context, update tgbotapi.Update) {
		return func(ctx context.Context, update tgbotapi.Update) {
			var tgUser *tgbotapi.User

			if update.Message != nil {
				tgUser = update.Message.From
			} else if update.CallbackQuery != nil {
				tgUser = update.CallbackQuery.From
			}

			if tgUser != nil {
				user := &storage.User{
					TelegramID:   tgUser.ID,
					Username:     tgUser.UserName,
					FirstName:    tgUser.FirstName,
					LanguageCode: tgUser.LanguageCode,
				}

				// Foreground upsert so the user row exists for later handlers.
				_ = userStore.Upsert(ctx, user)
			}

			next(ctx, update)
		}
	}
}
