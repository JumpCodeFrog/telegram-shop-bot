package middleware

import (
	"context"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/storage"
)

type UserStore interface {
	Upsert(ctx context.Context, user *storage.User) error
}

// handlerCtx is a local copy of the Bot.handlerCtx logic to avoid circular deps
// or until middleware is moved to a package where it can access it.
func handlerCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func Auth(userStore UserStore) func(next func(update tgbotapi.Update)) func(update tgbotapi.Update) {
	return func(next func(update tgbotapi.Update)) func(update tgbotapi.Update) {
		return func(update tgbotapi.Update) {
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

				// Synchronize user in background or foreground?
				// For Auth middleware, usually foreground to have ID available
				ctx, cancel := handlerCtx()
				_ = userStore.Upsert(ctx, user)
				cancel()

				// We can attach the user object to a custom context if needed
				// For now, just ensure they exist in DB
			}

			next(update)
		}
	}
}
