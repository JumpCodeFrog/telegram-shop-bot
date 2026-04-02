package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/service"
	"shop_bot/internal/storage"
)

type OnboardingWorker struct {
	bot         *tgbotapi.BotAPI
	userStore   *storage.SQLUserStore
	i18n        *service.I18nService
	botUsername string
	interval    time.Duration
}

func NewOnboardingWorker(bot *tgbotapi.BotAPI, userStore *storage.SQLUserStore, i18n *service.I18nService, botUsername string, interval time.Duration) *OnboardingWorker {
	return &OnboardingWorker{
		bot:         bot,
		userStore:   userStore,
		i18n:        i18n,
		botUsername: botUsername,
		interval:    interval,
	}
}

func (w *OnboardingWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	slog.Info("Onboarding Worker started")

	for {
		select {
		case <-ctx.Done():
			slog.Info("Onboarding Worker stopped")
			return
		case <-ticker.C:
			w.runOnboarding(ctx)
		}
	}
}

func (w *OnboardingWorker) runOnboarding(ctx context.Context) {
	slog.Info("Running onboarding check...")

	// Users created between 23h and 25h ago with no orders
	users, err := w.userStore.GetNewUsersWithoutOrders(ctx, 23*time.Hour, 25*time.Hour)
	if err != nil {
		slog.Error("Onboarding: failed to get new users", "error", err)
		return
	}

	for _, u := range users {
		lang := u.LanguageCode
		if lang == "" {
			lang = "en"
		}

		name := u.FirstName
		if name == "" {
			name = u.Username
		}
		if name == "" {
			name = w.i18n.T(lang, "onboarding_default_name")
		}

		var text string
		if u.ReferralCode.Valid && u.ReferralCode.String != "" {
			referralLink := fmt.Sprintf("https://t.me/%s?start=%s", w.botUsername, u.ReferralCode.String)
			text = fmt.Sprintf(w.i18n.T(lang, "onboarding_greeting_with_referral"), name, referralLink)
		} else {
			text = fmt.Sprintf(w.i18n.T(lang, "onboarding_greeting"), name)
		}

		msg := tgbotapi.NewMessage(u.TelegramID, text)
		msg.ParseMode = "HTML"
		if _, err := w.bot.Send(msg); err != nil {
			slog.Error("Onboarding: failed to send message", "user_id", u.TelegramID, "error", err)
		}
	}
}
