package middleware

import (
	"context"
	"shop_bot/internal/storage"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type UserStore interface {
	Upsert(ctx context.Context, user *storage.User) error
}

// Auth upserts the Telegram user into storage before the update proceeds.
// ctxFn supplies a bounded per-update context for the DB call (e.g.
// Bot.handlerCtx); the middleware chain itself does not carry a context.
func Auth(userStore UserStore, ctxFn func() (context.Context, context.CancelFunc)) func(next func(update tgbotapi.Update)) func(update tgbotapi.Update) {
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
				ctx, cancel := ctxFn()
				_ = userStore.Upsert(ctx, user)
				cancel()

				// We can attach the user object to a custom context if needed
				// For now, just ensure they exist in DB
			}

			next(update)
		}
	}
}
