package middleware

import (
	"context"
	"log/slog"
	"shop_bot/internal/storage"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type UserStore interface {
	Upsert(ctx context.Context, user *storage.User) error
}

// Auth upserts the Telegram user into storage before the update proceeds,
// using the per-update context carried by the chain (roadmap 4.14). Upsert
// failures are logged as warnings (with the user id) instead of being
// swallowed: a missing user row breaks every later handler that reads it.
func Auth(userStore UserStore, logger *slog.Logger) func(next func(ctx context.Context, update tgbotapi.Update)) func(ctx context.Context, update tgbotapi.Update) {
	if logger == nil {
		logger = slog.Default()
	}
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
				if err := userStore.Upsert(ctx, user); err != nil {
					logger.Warn("auth: user upsert failed", "user_id", user.TelegramID, "error", err)
				}
			}

			next(ctx, update)
		}
	}
}
