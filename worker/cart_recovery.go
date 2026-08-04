package worker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/google/uuid"

	"shop_bot/internal/service"
	"shop_bot/internal/storage"
)

type CartRecoveryWorker struct {
	cart           storage.CartStore
	promos         storage.PromoStore
	users          storage.UserStore
	i18n           *service.I18nService
	bot            *tgbotapi.BotAPI
	metrics        *service.MetricsService
	interval       time.Duration
	abandonedAfter time.Duration
}

func NewCartRecoveryWorker(bot *tgbotapi.BotAPI, cart storage.CartStore, promos storage.PromoStore, users storage.UserStore, i18n *service.I18nService, metrics *service.MetricsService, interval time.Duration, abandonedAfter ...time.Duration) *CartRecoveryWorker {
	age := 24 * time.Hour
	if len(abandonedAfter) > 0 {
		age = abandonedAfter[0]
	}
	return &CartRecoveryWorker{
		bot:            bot,
		cart:           cart,
		promos:         promos,
		users:          users,
		i18n:           i18n,
		metrics:        metrics,
		interval:       interval,
		abandonedAfter: age,
	}
}

func (w *CartRecoveryWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	slog.Info("Cart Recovery Worker started", "interval", w.interval, "abandoned_after", w.abandonedAfter)

	for {
		select {
		case <-ctx.Done():
			slog.Info("Cart Recovery Worker stopped")
			return
		case <-ticker.C:
			w.runRecovery(ctx)
		}
	}
}

func (w *CartRecoveryWorker) runRecovery(ctx context.Context) {
	w.recountActiveCarts(ctx)

	// Find carts older than the configured abandonment threshold.
	userIDs, err := w.cart.GetAbandonedCarts(ctx, w.abandonedAfter)
	if err != nil {
		slog.Error("Failed to get abandoned carts", "error", err)
		return
	}

	for _, userID := range userIDs {
		w.processUser(ctx, userID)
	}
}

// recountActiveCarts refreshes the ActiveCarts gauge from the database on
// every tick so the metric survives restarts and out-of-band cart changes.
func (w *CartRecoveryWorker) recountActiveCarts(ctx context.Context) {
	if w.metrics == nil {
		return
	}
	n, err := w.cart.CountActiveCarts(ctx)
	if err != nil {
		slog.Error("Failed to count active carts", "error", err)
		return
	}
	w.metrics.ActiveCarts.Set(float64(n))
}

func (w *CartRecoveryWorker) processUser(ctx context.Context, userID int64) {
	// 1. Generate personal promo with unpredictable suffix.
	suffix := strings.ToUpper(uuid.New().String()[:8])
	code := fmt.Sprintf("RECOVER10-%s", suffix)
	promo := &storage.PromoCode{
		Code:     code,
		Discount: 10,
		MaxUses:  1,
		IsActive: true,
	}
	// Expires in 3 days
	expiresAt := time.Now().Add(72 * time.Hour)
	promo.ExpiresAt = &expiresAt

	_, err := w.promos.CreatePromo(ctx, promo)
	if err != nil {
		slog.Error("Failed to create recovery promo", "user_id", userID, "error", err)
		return
	}

	// 2. Send message in the user's language (default en).
	lang := "en"
	if user, err := w.users.GetByTelegramID(ctx, userID); err == nil && user.LanguageCode != "" {
		lang = user.LanguageCode
	}
	text := fmt.Sprintf(w.i18n.T(lang, "cart_recovery_message"), code)

	msg := tgbotapi.NewMessage(userID, text)
	msg.ParseMode = "Markdown"
	if _, err := w.bot.Send(msg); err != nil {
		slog.Error("Failed to send recovery message", "user_id", userID, "error", err)
		// We still mark as sent to avoid spamming if user blocked the bot
	}

	// 3. Mark as sent
	if err := w.cart.MarkRecoverySent(ctx, userID); err != nil {
		slog.Error("Failed to mark recovery as sent", "user_id", userID, "error", err)
	}

	// The reminder was dispatched (promo issued, message send attempted).
	if w.metrics != nil {
		w.metrics.CartsAbandoned.Inc()
	}
}
